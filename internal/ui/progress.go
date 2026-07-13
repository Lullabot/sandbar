package ui

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/lullabot/sandbar/internal/vm"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// readPipe bundles the read end of one job's pipe. It is held by pointer (on the
// job, inside the registry) so successive readNextCmds keep reading the same
// stream.
type readPipe struct {
	r *io.PipeReader
}

// close tears the read end down. Every subsequent write to the paired writer
// fails immediately, which is what unblocks a run function that is mid-write —
// or one that ignores its context entirely — so a reaped job cannot leak its
// goroutine. See stop() in jobs.go.
func (rp *readPipe) close() {
	if rp != nil && rp.r != nil {
		rp.r.CloseWithError(errJobReaped)
	}
}

// errJobReaped is what a reaped job's pipe reports. It never reaches the user:
// the job is already out of the registry by the time it surfaces, so its
// provisionDoneMsg is dropped.
var errJobReaped = errors.New("job reaped: its VM disappeared")

// provisionFunc matches Provisioner.CreateVM / Recreate.
type provisionFunc func(ctx context.Context, cfg vm.CreateConfig, out io.Writer) error

// streamFunc is a generic streaming operation writing to out and honouring ctx.
// It backs both provisioning and file transfers.
type streamFunc func(ctx context.Context, out io.Writer) error

// beginStream registers the job identified by key and sets up the io.Pipe →
// registry → viewport → spinner plumbing shared by provisioning and file
// transfers. It spawns run in a goroutine feeding the pipe and returns the
// commands that stream it, AND WHETHER IT ACTUALLY STARTED THE JOB. This is the
// non-blocking pattern: Update never runs the operation — it only reacts to the
// keyed messages this produces.
//
// That second return value is not decoration. beginJob used to attach a provision
// config after calling this REGARDLESS, so a refused create mutated the run that
// was already in flight — see markProvision. A caller that means to touch the job
// it asked for has to know whether it got one.
//
// If a run of the SAME KIND is already in flight for this VM, no second one is
// started: two jobs sharing a key would orphan the first one's cancel func and
// leak its goroutine. The user is shown the running one instead. A run of the
// OTHER kind is left strictly alone — that is the point of keying by kind, and it
// is what stops a file copy from evicting a failed build's tile and log.
//
// It deliberately does NOT change the view. Which screen a run lands on is the
// CALLER's decision, and the two callers want opposite things: a build streams
// onto its own tile while the board stays live, whereas a file transfer — launched
// from the VM screen, with nothing on the board to watch — still opens its log.
// Flipping to the full-screen log HERE is what made every run take the terminal
// hostage, which is the takeover this registry exists to end.
func (m *model) beginStream(key jobKey, title string, run streamFunc) (tea.Cmd, bool) {
	if m.jobs.running(key) {
		m.logMsg(key.vm + " already has a run in flight — showing it")
		return m.showJob(key), false
	}

	pr, pw := io.Pipe()
	// A cancellable context lets ctrl+c (in Update) abort this job — and only this
	// job — mid-flight, killing the limactl subprocess it is blocked on.
	ctx, cancel := context.WithCancel(context.Background())

	j := &job{
		key:    key,
		title:  title,
		state:  jobRunning,
		cancel: cancel,
		reader: &readPipe{r: pr},
	}
	if !m.jobs.begin(j) {
		// Unreachable: the guard above already refused a live job, and only the
		// Update goroutine begins jobs. Guarded anyway — the alternative is a leaked
		// goroutine with no cancel func.
		cancel()
		return nil, false
	}

	go func() {
		// CloseWithError(nil) closes the writer cleanly, surfacing io.EOF to the
		// reader; a non-nil err surfaces as that error on the final Read.
		pw.CloseWithError(run(ctx, pw))
	}()

	return tea.Batch(readNextCmd(key, j.reader), m.tickSpinner()), true
}

// beginProvision launches a provisioner call through beginStream and records its
// config on the job, so a successful run can be marked managed (and reproduced
// faithfully on a future recreate).
func (m *model) beginProvision(title string, run provisionFunc, cfg vm.CreateConfig) tea.Cmd {
	return m.beginJob(title, run, cfg, false)
}

// beginReset is beginProvision for a Reset/Recreate: the same provisioning job,
// flagged as one that deletes and re-creates its own VM. That flag is what stops
// the disappeared-VM reaper from killing a reset the moment it deletes the VM it
// is about to clone back (see jobRegistry.reconcile).
func (m *model) beginReset(title string, run provisionFunc, cfg vm.CreateConfig) tea.Cmd {
	return m.beginJob(title, run, cfg, true)
}

// beginJob is the shared body of beginProvision/beginReset.
//
// A VM that is ALREADY BUSY — building, or being copied to — gets no build started
// against it. A create for a name that is mid-build is the user naming the wrong
// VM (nothing in vm.CreateConfig.Validate rejects a duplicate name; it is a pure
// value check and cannot see the registry), and a reset of a VM a copy is
// streaming into would delete the copy's target out from under it. The form
// catches both first, with the name still on screen to edit (submitForm); this is
// the invariant behind that, for every other caller. It refuses honestly and shows
// the run that is in the way rather than silently doing nothing.
//
// markProvision runs ONLY if beginStream started the job. Calling it regardless is
// the bug this signature exists to prevent: it would attach this form's config —
// its cpus, its clone URL, its GitHub token — to whatever run was already in
// flight under that name.
func (m *model) beginJob(title string, run provisionFunc, cfg vm.CreateConfig, recreates bool) tea.Cmd {
	if key, busy := m.jobs.runningKey(cfg.Name); busy {
		m.logMsg(cfg.Name + " already has a run in flight — wait for it to finish, or cancel it from its log")
		return m.showJob(key)
	}
	cmd, started := m.beginStream(provisionKey(cfg.Name), title, func(ctx context.Context, out io.Writer) error {
		return run(ctx, cfg, out)
	})
	if !started {
		return cmd
	}
	m.jobs.markProvision(cfg.Name, cfg, recreates)
	// The signature moment: submitting the create form drops the user back on the
	// BOARD, where a new tile is already showing a building badge and a filling
	// progress bar, and where they can arrow away and start a second VM. Landing on
	// the full-screen Ansible dump instead is the screen-takeover this whole plan
	// exists to remove; the log is still one `l` away from the tile.
	//
	// The ring goes to the VM the user just asked for. Focus is otherwise pinned to
	// identity and never moves on its own — a refresh tick must never drag it — but
	// this is not the board moving focus, it is the user creating the thing they are
	// now looking at. Without it a create started from the empty slot leaves the ring
	// on the empty slot, and `l` would not open the log of the build they just began.
	m.focusName = cfg.Name
	m.view = viewBoard
	return cmd
}

// showJobLog puts a VM's run — live or retained — back on the progress screen.
// It backs the reopen-log verb (commandreg.go): the registry holds the buffer
// anyway, so a failed provision can be interrogated instead of merely announced.
// logKey decides WHICH run a VM holding two of them shows; a failed build wins,
// because that is the tile the user is pressing `l` on.
func (m *model) showJobLog(name string) tea.Cmd {
	key, ok := m.jobs.logKey(name)
	if !ok {
		return nil
	}
	return m.showJob(key)
}

// showJob puts one specific run on the progress screen.
func (m *model) showJob(key jobKey) tea.Cmd {
	if !m.jobs.exists(key) {
		return nil
	}
	m.focusJob(key)
	// A live job is still animating (the user may have walked away from it and
	// come back); a retained one has nothing left to spin for.
	if m.jobs.running(key) {
		return m.tickSpinner()
	}
	return nil
}

// focusJob puts a run on the progress screen without starting a spinner — the
// caller that has just begun a job already batched one, and a second tick loop
// would run the spinner at double speed forever.
func (m *model) focusJob(key jobKey) {
	m.progressJob = key
	m.view = viewProgress
	m.setOutput()
}

// readNextCmd reads one chunk from a job's pipe. It emits a KEYED
// provisionOutputMsg per chunk (Update re-issues it to read the next one) and a
// provisionDoneMsg at EOF or on error. Reading happens off the Update goroutine,
// and the key is what lets N of these run at once without their output crossing
// streams — the message used to be a bare string, which is precisely why only one
// job could ever exist, and a bare VM name, which is why a VM's copy and its
// build could not both stream at once.
func readNextCmd(key jobKey, rp *readPipe) tea.Cmd {
	return func() tea.Msg {
		if rp == nil {
			return provisionDoneMsg{job: key}
		}
		buf := make([]byte, 4096)
		n, err := rp.r.Read(buf)
		if n > 0 {
			return provisionOutputMsg{job: key, chunk: string(buf[:n])}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return provisionDoneMsg{job: key}
			}
			return provisionDoneMsg{job: key, err: err}
		}
		return provisionOutputMsg{job: key}
	}
}

// shownJob is the job the progress screen is displaying, if any.
func (m model) shownJob() (jobSnapshot, bool) {
	return m.jobs.snapshot(m.progressJob)
}

// updateProgress handles keys on the progress view. The screen no longer traps
// the user: esc/enter ALWAYS return to the job's back view and the job keeps
// running behind them — that is the un-freezing this task exists for, and it is
// what lets a user start a second VM while the first one builds. ctrl+c (handled
// in Update) cancels the job being shown, and only that one. There is no q here at
// all — quit belongs to the board (see requestQuit).
func (m model) updateProgress(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.confirm != nil {
		return m.updateConfirm(msg)
	}
	if key.Matches(msg, m.keys.Back) || key.Matches(msg, m.keys.Enter) {
		// Every run returns to the BOARD. A job used to carry its own `back` view,
		// because a transfer's log returned to the VM screen and a build's to the
		// board; with the VM screen deleted there is one destination, so the field
		// went with it rather than lingering as a constant.
		m.view = viewBoard
		return m, nil
	}
	// No `q` here. Quit belongs to the board alone — this is a child screen, and the
	// key that leaves it is `esc` (which, note, does NOT cancel the run). A `q` next
	// to a still-scrolling build log is one mistyped key between the user and the end
	// of their session.

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// progressHelp returns the bindings shown in the progress screen's help bar.
// While the shown job runs, ctrl+c cancels it and esc leaves it running; once it
// has finished, esc returns. Quit is not offered — it is the board's, and only the
// board's.
func (m model) progressHelp() []key.Binding {
	if m.confirm != nil {
		return []key.Binding{m.keys.Confirm, m.keys.Cancel}
	}
	if job, ok := m.shownJob(); ok && job.Running() {
		return []key.Binding{m.keys.Interrupt, m.keys.Background}
	}
	return []key.Binding{m.keys.Back}
}

// progressView renders the spinner/title, the scrollable output box, the final
// status, and the help bar — all for the job the screen is showing.
func (m model) progressView() string {
	var b strings.Builder

	job, ok := m.shownJob()
	if !ok {
		// The job was reaped: its VM disappeared while the user watched it.
		b.WriteString(titleStyle.Render("Run unavailable"))
		b.WriteString("\n\n")
		b.WriteString(statusStyle.Render("This VM is gone, and its run went with it. Press esc to return to the board."))
		if m.confirm != nil {
			b.WriteString("\n\n" + m.confirmView())
		}
		b.WriteString("\n\n" + m.footerView(m.progressHelp()))
		return appStyle.Render(b.String())
	}

	head := job.Title
	if job.Running() {
		head = m.spinner.View() + " " + head
	}
	b.WriteString(titleStyle.Render(head))
	b.WriteString("\n\n")
	b.WriteString(boxStyle.Render(m.viewport.View()))
	b.WriteString("\n")

	if !job.Running() {
		switch {
		case job.Canceled:
			b.WriteString("\n" + statusStyle.Render("Canceled — press esc to return to the board."))
		case job.Err != nil:
			b.WriteString("\n" + errStyle.Render("Failed: "+job.Err.Error()))
		default:
			b.WriteString("\n" + okStyle.Render("Done — press esc to return to the "+"board."))
		}
	}
	// A quit that would abandon another VM's build confirms first (requestQuit).
	if m.confirm != nil {
		b.WriteString("\n" + m.confirmView())
	}

	b.WriteString("\n" + m.footerView(m.progressHelp()))
	return appStyle.Render(b.String())
}
