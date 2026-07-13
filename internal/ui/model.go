// Package ui holds the Bubble Tea model, views, and commands for the sand
// TUI. It is a thin interactive surface over the lima.Client (VM lifecycle),
// provision.Provisioner (create/reset), registry.Registry (which VMs are ours),
// and secrets.Store (per-VM host-side secrets) packages.
//
// Screens divide by responsibility: the BOARD (board.go) is the home surface and
// the only roster — a grid of tiles, one per managed clone, with a focus ring and
// the single-key verbs that act on the tile under it; the VM screen zooms one
// tile to full screen and owns the same verbs there. All blocking I/O happens in
// tea.Cmds so Update never stalls, and the long-running provisioner streams its
// output into a scrollable progress pane.
package ui

import (
	"strings"
	"time"

	"github.com/lullabot/sandbar/internal/browse"
	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/manage"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// confirmState is a pending destructive action awaiting the user's `y`. It is
// screen-agnostic: the board raises it for stop-all and for a quit that would
// abandon a run, and the VM screen raises it for delete — neither screen needs to
// know what the other confirms. A nil *confirmState on the model means no
// confirmation is pending.
type confirmState struct {
	prompt  string  // e.g. `Delete "web"?`
	run     tea.Cmd // dispatched (via beginAction) when the user presses Confirm
	working string  // status shown (with the live spinner) while run is in flight; "" leaves the status line untouched
}

// view is the active screen the model renders and routes keys to. viewBoard is
// the default and the only roster: the table-backed list it replaced is gone,
// with no toggle back to it.
type view int

const (
	viewBoard view = iota
	viewDetail
	viewForm
	viewProgress
	viewBrowse
	viewDest
	viewSecrets
)

// model is the root Bubble Tea model. It is passed by value through Update, so
// every field must be safe to copy — no strings.Builder, and nothing mutable that
// has to outlive one Update call. State that does (a running job's log, its
// cancel func, its parsed progress) lives behind the jobs POINTER below, in the
// one registry every model copy shares. That is the whole reason the registry is
// a pointer: a copied model must not fork the work it is watching.
type model struct {
	cli  *lima.Client
	prov *provision.Provisioner
	reg  *registry.Registry
	keys keyMap
	help help.Model

	view   view
	width  int
	height int
	layout layoutMode // budgets derived from width/height by classify (see layout.go); set by applySize

	// messages is the session-only, bounded activity log (messages.go) that
	// replaced the single overwritten status string this field used to be:
	// logMsg appends, lastMessage/recentMessages read. It is a plain slice,
	// not a pointer, because — unlike jobs/heartbeats — nothing outside the
	// Update goroutine ever writes to it.
	messages []message

	// Board + detail. vms is every VM Lima reported; the board (board.go) derives
	// its roster from it — managed clones only, always — and detail is the VM the
	// full-screen VM screen is showing.
	vms    []vm.VM
	detail vm.VM

	// focusName is the VM under the board's focus ring: AN IDENTITY, NEVER A SLOT
	// INDEX. A refresh, an insertion, a deletion or a filter keystroke reorders the
	// grid; the ring stays on the same VM, because a ring that tracked the index
	// would silently slide onto a different VM and hand the next destructive key to
	// it. scrollRow is the first tile ROW the grid viewport shows, and only
	// ensureFocusVisible moves it — so the viewport cannot drift away from the ring.
	focusName string
	scrollRow int

	// jobs is the job registry (jobs.go), keyed by VM AND KIND: every provision and
	// transfer in flight, plus the last run of each kind a VM retained — a failed
	// build and a later file copy are two runs, and the copy may not evict the
	// build. It is a POINTER because the model is passed by value — a registry
	// embedded here would fork on the first copy —
	// and it satisfies the jobLookup seam in commandreg.go, which gates Delete
	// while a VM builds and the reopen-log verb on a VM having a run to show. A nil
	// registry is safe to call and reports "no jobs", so a model built by hand
	// behaves exactly as it did before this existed.
	jobs *jobRegistry

	// heartbeats is the VM-keyed guest heartbeat registry (heartbeat.go): one live
	// `limactl shell` per RUNNING VM, streaming real cpu and memory out of the guest.
	// A POINTER for the same reason jobs is, and nil-safe for the same reason.
	heartbeats *heartbeatRegistry

	// focused tracks whether the terminal has focus, and lastInput when the user
	// last touched a key. Together with the active view they are the idle gate
	// (shouldTick, heartbeat.go) that decides whether sand may hold SSH connections
	// open into the guests. focused starts TRUE: a terminal that does not support
	// focus reporting never sends a FocusMsg, and a gate that waited for one would
	// leave the board permanently blank.
	focused   bool
	lastInput time.Time

	// Incremental name search. When searching is true, typed keys edit
	// searchQuery instead of firing actions; searchQuery is a case-insensitive
	// name substring filter over the board's tiles (visibleVMs, board.go). It
	// narrows what is SHOWN and nothing else: 'X' still stops every managed VM.
	searching   bool
	searchQuery string

	// acting is true while a quick lifecycle action (start/stop/restart/delete) is
	// in flight. It drives the spinner beside the status line so these blocking
	// limactl calls show live feedback, and is cleared by the matching
	// actionDoneMsg.
	acting bool

	// Pending destructive-action confirmation (nil = inactive). Raised by
	// whichever screen dispatches the action; routed and rendered from the board,
	// the VM screen and a finished progress screen via updateConfirm/confirmView
	// below.
	confirm *confirmState

	// Create form.
	inputs       []textinput.Model
	focusIdx     int
	formErr      error
	hostDiskFree int64 // free bytes on the Lima volume, sampled when the form opens (0 = unknown)

	// Reset mode reuses the create form to reset a managed VM: the Name is locked
	// to the target and two preserve toggles follow the inputs.
	resetMode            bool
	resetName            string // locked Name when in reset mode
	resetBaseName        string // base image the reset clones from
	preserveClaude       bool
	preserveProject      bool
	projectToggleEnabled bool   // false when OrgRelDir(cfg.CloneURL) has no org segment (nothing to preserve)
	projectToggleLabel   string // "Preserve ~/<org-rel-dir>", computed once in openResetForm
	toggleFocus          int    // -1 = focus is in the text inputs; 0 = Claude toggle; 1 = project toggle (only reachable when projectToggleEnabled)

	// Progress / streaming. Everything that used to be a single job's state on the
	// model — its title, back view, output buffer, running flag, error, reader,
	// config and cancel func — now lives on that job in the registry above. What is
	// left here is which job the progress SCREEN is showing, which is a property of
	// the screen, not of the work: the job goes on whether or not anyone is looking.
	viewport viewport.Model
	spinner  spinner.Model

	// progressJob is the RUN the progress screen displays — a VM plus which of its
	// runs, because a VM can have both a build and a file copy in flight (jobs.go).
	// A zero-value vm name means none.
	progressJob jobKey

	// spinning is true while exactly one spinner.Tick loop is in flight. Jobs and
	// quick actions can now overlap (they could not while the old model-wide
	// running flag froze the keyboard), and each one kicking its own tick would
	// stack loops and spin the animation at double speed. tickSpinner owns this
	// flag.
	spinning bool

	// refreshing is true while exactly one board refresh-tick loop (refresh.go)
	// is in flight — tickRefresh's guard against stacking loops, the same
	// problem spinning solves for the spinner.
	refreshing bool

	// File transfer (Upload/Download). The browser and dest prompt are copy-safe
	// (only a list.Model / textinput.Model plus small scalars), matching the
	// value-passed model. destination is always a directory; the source is placed
	// inside it, so the result is identical across the rsync/scp copy backends.
	browser           browse.Browser
	dest              browse.DestInput
	transferVM        string // VM the transfer targets
	transferUpload    bool   // true = upload (host→guest); false = download (guest→host)
	transferSrc       string // chosen source (absolute; a host path, or a guest path without the "vm:" prefix)
	transferRecursive bool   // the source is a directory (copied with -r)

	// Secrets editor. sec is the host-side store (a pointer, so the value-passed
	// model stays cheap to copy). secretsArea holds the KEY=VALUE buffer and
	// secretsVM the VM it belongs to.
	sec         *secrets.Store
	secretsArea textarea.Model
	secretsVM   string
	secretsErr  error
}

// New wires the dependencies into a ready-to-run tea.Model.
func New(cli *lima.Client, prov *provision.Provisioner) tea.Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	// Load the managed-VM index. LoadFrom always returns a usable (non-nil)
	// registry; a corrupt/unreadable file surfaces as a warning rather than
	// silently demoting every managed VM.
	reg, loadErr := registry.Load()
	if reg == nil {
		reg = registry.NewEmpty()
	}

	// The secrets store gets the same tolerant posture as the registry: a
	// corrupt file surfaces as a warning rather than crashing or silently
	// discarding the VM's secrets.
	sec, secErr := secrets.Load()
	if sec == nil {
		sec = secrets.NewEmpty()
	}

	m := model{
		cli:        cli,
		prov:       prov,
		reg:        reg,
		sec:        sec,
		jobs:       newJobRegistry(),
		heartbeats: newHeartbeats(cli),
		keys:       newKeyMap(),
		help:       help.New(),
		view:       viewBoard,
		viewport:   viewport.New(),
		spinner:    sp,
		// The session starts focused and freshly used; anything else and the idle
		// gate would be shut before the first frame.
		focused:   true,
		lastInput: time.Now(),
	}
	// Seed a sane pre-resize default (mirrors the terminal's classic 80x24) so
	// the model renders sensibly before the first real WindowSizeMsg arrives.
	m.applySize(80, 24)
	// Neither load failure may silently shadow the other.
	switch {
	case loadErr != nil && secErr != nil:
		m.logMsg("warning: " + loadErr.Error() + "; " + secErr.Error())
	case loadErr != nil:
		m.logMsg("warning: " + loadErr.Error())
	case secErr != nil:
		m.logMsg("warning: " + secErr.Error())
	}
	return m
}

// Init kicks off the first list load.
func (m model) Init() tea.Cmd {
	return listCmd(m.cli)
}

// applySize recomputes the layout mode for a new terminal size and pushes its
// budgets onto every sub-component the active (or potentially active) view
// owns. This is the single place classify is invoked — nothing else may call
// it, so every pane's size traces back to one decision. Called both from New
// (to seed a pre-resize default) and the WindowSizeMsg handler.
func (m *model) applySize(w, h int) {
	m.width, m.height = w, h
	m.layout = classify(w, h)

	// The help bar's OWN width-based truncation is disabled (0): bubbles'
	// ShortHelpView only stops adding items when it ALSO has room left for its
	// own ellipsis — lacking that room, it silently renders one more item
	// unclipped instead of stopping, which is what let the VM screen's longest
	// footer overflow the terminal by ~2 cells. Rather than fight that logic
	// with a width budget, ShortHelpView is left to render its full,
	// UNCLIPPED list, and footerView (model.go) does the actual, honest clip
	// itself with an ANSI-aware truncation to ContentWidth.
	m.help.SetWidth(0)
	m.viewport.SetWidth(m.layout.ContentWidth)
	m.viewport.SetHeight(m.layout.GridHeight)

	// A resize changes the grid's columns and its visible rows, so the focused tile
	// can land outside the viewport without the ring moving at all. Re-park the
	// scroll on it.
	m.ensureFocusVisible()

	// Resize an active file browser too (its inner list is only initialized
	// while a transfer is in flight, so guard on the view to avoid touching a
	// zero-value browser).
	if m.view == viewBrowse {
		m.browser.SetSize(m.layout.ContentWidth, m.layout.GridHeight)
	}
	// Keep an open secrets editor sized to the terminal (its height budget
	// must match openSecrets, or the editor plus its footer would overflow and
	// scroll the title off the top).
	if m.view == viewSecrets {
		w, h := secretsEditorSize(m.layout)
		m.secretsArea.SetWidth(w)
		m.secretsArea.SetHeight(h)
	}
	// Reflow the shown job's streamed output to the new width so it stays wrapped.
	if m.progressJob.vm != "" {
		m.setOutput()
	}
}

// tickSpinner starts the spinner's tick loop, and returns nil if one is already
// in flight. Exactly one loop may exist: jobs and quick lifecycle actions can now
// overlap — two builds, or a build plus a start — and a tick loop per caller
// would animate the spinner at two, three, N times its speed. The loop ends
// itself (clearing m.spinning) when the last of them finishes; see the
// spinner.TickMsg handler in Update.
func (m *model) tickSpinner() tea.Cmd {
	if m.spinning {
		return nil
	}
	m.spinning = true
	return m.spinner.Tick
}

// footerView renders bindings through the shared help.Model and then clips
// the result HONESTLY to ContentWidth with an ANSI-aware truncation of its
// own — see the note on applySize above for why it may not lean on
// help.Model's own width truncation. Shared by the board (board.go) and the
// VM screen (detail.go) so the fix lands on both footers, not just one.
func (m model) footerView(bindings []key.Binding) string {
	return m.clipLine(m.help.ShortHelpView(bindings))
}

// clipLine truncates one rendered line to ContentWidth, ANSI-aware. Every line a
// screen spends goes through here (or through a pane that already does it): a
// board whose height fits but whose status line runs off the right edge is the
// same clipping bug with the axes swapped.
func (m model) clipLine(s string) string {
	return ansi.Truncate(s, m.layout.ContentWidth, "…")
}

// present indexes a freshly loaded VM list by name, for jobRegistry.reconcile.
func present(vms []vm.VM) map[string]bool {
	set := make(map[string]bool, len(vms))
	for _, v := range vms {
		set[v.Name] = true
	}
	return set
}

// Update dispatches the message, then reconciles two things against whatever
// state that left behind: the board's focus ring, and the guest heartbeats.
//
// Both reconcile after EVERY message rather than at the few places that obviously
// matter, and that is deliberate.
//
// The heartbeat's gate (shouldTick) turns on the active view, the terminal's
// focus, and how long ago the user last typed — three things that change under a
// dozen different messages, in five different files. A gate re-checked at "the
// places that change it" is a gate that is one forgotten `m.view = …` away from
// holding an SSH connection open into every guest on the machine, forever, with
// nobody watching.
//
// The board's roster changes under exactly as many messages: a refresh, a delete,
// a build starting on a VM Lima has never heard of, a filter keystroke. syncBoard
// is what guarantees the ring is always on a VM that is actually on the board —
// and a ring pointing anywhere else is a destructive key pointed at the wrong VM.
// Both cost a map walk over at most a handful of VMs, and neither can be forgotten.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.dispatch(msg)
	nm, ok := next.(model)
	if !ok {
		return next, cmd
	}
	nm.syncBoard()
	return nm, tea.Batch(cmd, nm.syncHeartbeats(), nm.tickRefresh())
}

// dispatch is the single message-handling point. Key messages route by active view;
// all other messages (async results, ticks, blinks) are handled or forwarded to the
// active sub-component.
func (m model) dispatch(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.applySize(msg.Width, msg.Height)
		return m, nil

	case tea.FocusMsg:
		// The terminal came back to the foreground. That is the user returning, so it
		// reopens the idle gate on both counts — and syncHeartbeats, above, reopens
		// the connections.
		m.focused = true
		m.lastInput = time.Now()
		return m, nil

	case tea.BlurMsg:
		// Backgrounded. Every heartbeat is an SSH connection into a guest, and nobody
		// is looking at the gauges they feed.
		m.focused = false
		return m, nil

	case heartbeatSampleMsg:
		// The channel closed: this VM's stream ended. Against a real VM that is what a
		// `limactl stop` looks like from here (the shell dies within ~300ms with `exit
		// status 255`), so it is the ordinary path for a VM going down, not an
		// exceptional one. ended() drops the reading, so the gauge goes with the VM
		// instead of freezing at whatever it last said.
		if !msg.ok {
			m.heartbeats.ended(msg.vm, msg.epoch)
			return m, nil
		}
		// fold returns nil for a sample from a connection that has since been replaced,
		// which ends that stale read loop rather than letting it double up on the live
		// one.
		return m, heartbeatReadCmd(msg.vm, msg.epoch, m.heartbeats.fold(msg.vm, msg.epoch, msg.sample))

	case refreshTickMsg:
		// This loop iteration is done; tickRefresh (called centrally after every
		// message — see Update) re-arms the next one iff shouldTick still allows
		// it, which is what makes the loop stop on its own once the board is no
		// longer the active, focused, recently-used screen.
		m.refreshing = false
		if !m.shouldTick() {
			return m, nil
		}
		return m, listCmd(m.cli)

	case spinner.TickMsg:
		// The spinner animates for any job in flight (on any VM, whether or not its
		// screen is showing) and for a quick lifecycle action; when neither is
		// happening, the loop ends and spinning goes false so the next action can
		// start a fresh one.
		if !m.acting && !m.jobs.anyRunning() {
			m.spinning = false
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case vmsLoadedMsg:
		if msg.err != nil {
			m.logMsg("list failed: " + msg.err.Error())
			return m, nil
		}
		m.vms = msg.vms // DiskUsed is already measured in listCmd, off the Update goroutine
		// Reconcile the managed index against reality so a VM deleted outside the
		// TUI stops being flagged managed (and recreate-able). Shared with the
		// headless `sand create` path (internal/manage) so the two entrypoints
		// cannot drift.
		dropped, err := manage.Reconcile(m.reg, msg.vms)
		if err != nil {
			m.logMsg("warning: could not update managed index: " + err.Error())
		}
		// A VM that vanished outside the TUI (and so got dropped above) also loses
		// its host-stored secrets — there is no guest left to apply them to, and
		// keeping them around risks silently reattaching stale secrets to an
		// unrelated VM that later reuses the name. Best-effort: a failure here
		// isn't worth displacing the reconcile status above.
		for _, name := range dropped {
			_ = m.sec.Remove(name)
		}
		// Fold the fresh list into the job registry: a job whose VM has disappeared
		// (deleted here or outside sand) is cancelled and reaped rather than left
		// running against a VM that no longer exists. reconcile knows not to reap a
		// build whose VM does not exist YET, nor a reset that deleted its own VM.
		if reaped := m.jobs.reconcile(present(msg.vms)); len(reaped) > 0 {
			m.logMsg("canceled the run for " + strings.Join(reaped, ", ") + ": the VM disappeared")
			// Nothing left to show for the run the user was watching.
			if m.view == viewProgress && !m.jobs.exists(m.progressJob) {
				m.view = viewBoard
			}
		}
		// The board's tiles come straight off m.vms + the registry + the job
		// registry, so there is nothing to rebuild — only the focus ring has to be
		// re-pinned against a fleet that may have gained or lost VMs, which Update
		// does centrally (syncBoard) after this returns.
		//
		// The VM screen acts on the VM it displays, so its snapshot goes stale after
		// every start/stop/restart. Re-seed it from the reloaded list; if the VM is
		// gone (deleted, or removed outside the TUI), fall back to the board rather
		// than rendering a zero-value record.
		if m.view == viewDetail {
			if v, ok := m.lookupVM(m.detail.Name); ok {
				m.detail = v
			} else {
				m.view = viewBoard
			}
		}
		return m, nil

	case actionDoneMsg:
		m.acting = false // the action finished; stop the list spinner
		label := msg.action + " " + msg.name
		var text string // built below, then logged ONCE at the end (see the warn append)
		switch {
		case msg.err != nil:
			text = label + " failed: " + msg.err.Error()
		case msg.action == "shell":
			// returned from the interactive shell; nothing to report — logMsg("")
			// below is a deliberate no-op, not a clear (a log has nothing to clear).
		case msg.action == "delete":
			// The record the VM screen was displaying no longer exists; only on
			// this success path (msg.err != nil already returned above) — a failed
			// delete leaves the VM in place, so the user should stay on its screen
			// to see the error. (Deleting from the BOARD leaves the view alone: the
			// board is already the screen, and syncBoard hands the ring to a
			// neighbour once the refresh drops the tile.)
			if m.view == viewDetail {
				m.view = viewBoard
			}
			// The user acted on this VM in the most final way there is, so its
			// retained run goes with it: a failed build's sticky Failed status exists
			// to be acted on, and deleting the VM is acting on it. (A run still in
			// flight is cancelled by remove — Delete is gated on !vmBuilding, so that
			// only happens if the VM was deleted from outside sand.)
			m.jobs.remove(msg.name)
			// A deleted VM is no longer managed, and its host-stored secrets no
			// longer have a guest to apply to; drop it from both indexes. Neither
			// failure may silently shadow the other.
			regErr := m.reg.Remove(msg.name)
			secErr := m.sec.Remove(msg.name)
			switch {
			case regErr != nil && secErr != nil:
				text = label + " ok (warning: managed index not updated: " + regErr.Error() + "; secrets not pruned: " + secErr.Error() + ")"
			case regErr != nil:
				text = label + " ok (warning: managed index not updated: " + regErr.Error() + ")"
			case secErr != nil:
				text = label + " ok (warning: secrets not pruned: " + secErr.Error() + ")"
			default:
				text = label + " ok"
			}
		default:
			text = label + " ok"
		}
		// A non-fatal ApplySecrets failure (start/restart/apply-secrets) is
		// appended to whatever success text the switch above built; it must never
		// run on the failure branch, which already carries its own error message.
		if msg.err == nil && msg.warn != "" {
			text += " (warning: " + msg.warn + ")"
		}
		m.logMsg(text)
		return m, listCmd(m.cli) // refresh after every action

	case provisionOutputMsg:
		// The chunk is keyed by RUN — the VM and which of its runs — so N jobs can
		// stream at once without their output crossing streams, including a VM's
		// build and a copy against that same VM. addOutput reports false for a job
		// that was reaped mid-flight: its late chunks are dropped and its reader is
		// not re-issued, which is what ends that job's read loop.
		if !m.jobs.addOutput(msg.job, msg.chunk) {
			return m, nil
		}
		if m.view == viewProgress && m.progressJob == msg.job {
			m.setOutput()
		}
		return m, readNextCmd(msg.job, m.jobs.reader(msg.job))

	case provisionDoneMsg:
		// finish retains the job — with its log — whether it succeeded or failed:
		// that is what makes the tile's Failed status sticky and its log
		// reopenable. It reports false only for a job already reaped, whose done
		// message is stale.
		job, ok := m.jobs.finish(msg.job, msg.err)
		if !ok {
			return m, nil
		}
		if m.view == viewProgress && m.progressJob == msg.job {
			m.setOutput()
		}
		// A user-canceled run leaves partial state behind; don't record it as
		// managed and don't surface its (kill-induced) error as a failure.
		if job.Canceled {
			return m, listCmd(m.cli)
		}
		// A successful create/recreate yields a sand-managed VM; record it
		// (with its config, for a faithful future recreate) so the list marks it
		// and recreate stays available for it. Shared with the headless
		// `sand create` path (internal/manage) so the two entrypoints cannot drift.
		//
		// A COPY IS NOT A BUILD, and the KIND is what says so — not the presence of a
		// config, which is a property of the VM's build and outlives it. A copy
		// finishing against a VM that also holds a retained build would otherwise
		// re-record that build's config (and re-seed its GH_TOKEN) as if the copy had
		// produced it.
		cfg, isProvision := m.jobs.config(msg.job.vm)
		var applyCmd tea.Cmd
		if msg.err == nil && msg.job.kind == kindProvision && isProvision && cfg.Name != "" {
			if err := manage.RecordSuccess(m.reg, cfg); err != nil {
				m.logMsg("VM ready, but recording it as managed failed: " + err.Error())
			}
			// The create form's token becomes the VM's GH_TOKEN secret, so it can
			// be edited later without a rebuild. It never enters the managed
			// registry, which strips CloneToken by design (registry.Add). Seeding
			// it here — in the TUI, on provisionDoneMsg — rather than inside
			// internal/provision keeps the host secrets store a TUI concern and
			// needs no new provision→secrets import; the headless `sand create`
			// path has no store to seed from, so doing this in the provisioner
			// would silently diverge the two entrypoints (Option A, rejected).
			// m.sec.Get already returns a defensive copy, so mutating pairs here
			// cannot corrupt the store ahead of Set validating it.
			if cfg.CloneToken != "" {
				pairs := m.sec.Get(cfg.Name)
				pairs["GH_TOKEN"] = cfg.CloneToken
				if err := m.sec.Set(cfg.Name, pairs); err != nil {
					m.logMsg("VM ready, but the token could not be saved as a secret: " + err.Error())
				}
			}
			// createVM/Reset each end with their own StartStreaming, which runs
			// BEFORE this handler and before GH_TOKEN lands in the store above —
			// so the guest would have no secrets.env until the VM's *next* start.
			// Dispatch the apply now (batched with the list refresh) so a user who
			// creates a VM and immediately shells in finds GH_TOKEN already set.
			user, scopes := m.secretsFor(cfg.Name)
			applyCmd = applySecretsCmd(m.cli, cfg.Name, user, scopes)
		}
		if applyCmd != nil {
			return m, tea.Batch(listCmd(m.cli), applyCmd)
		}
		return m, listCmd(m.cli) // refresh the list the user returns to

	case tea.KeyPressMsg:
		// Any key is proof someone is still there, which is half of the idle gate (see
		// shouldTick). It is also what WAKES a session that went idle: no timer runs
		// while sand is idle — that is the point of being idle — so the keypress that
		// says "I'm back" is the message that reopens the heartbeats.
		m.lastInput = time.Now()

		if msg.String() == "ctrl+c" {
			// On the progress screen, ctrl+c cancels the job being SHOWN — and only
			// that one, killing the limactl subprocess it is blocked on — rather than
			// quitting the whole TUI and orphaning a half-built VM. Cancellation is
			// per-job now: with several builds in flight, the one on screen is the one
			// the user is aiming at. Everywhere else — including a finished progress
			// screen — ctrl+c quits.
			if m.view == viewProgress && m.jobs.cancelJob(m.progressJob) {
				m.setOutput()
				return m, nil
			}
			return m, tea.Quit
		}
		switch m.view {
		case viewBoard:
			return m.updateBoard(msg)
		case viewDetail:
			return m.updateDetail(msg)
		case viewForm:
			return m.updateForm(msg)
		case viewProgress:
			return m.updateProgress(msg)
		case viewBrowse:
			return m.updateBrowse(msg)
		case viewDest:
			return m.updateDest(msg)
		case viewSecrets:
			return m.updateSecrets(msg)
		}
	}

	return m.forward(msg)
}

// setOutput wraps the SHOWN job's accumulated output to the viewport width and
// pins the view to the bottom. The buffer comes from the job registry, not from
// the model, which is what lets the user leave a build and come back to a log
// that kept filling while they were away. The bubbles viewport truncates lines
// wider than its width; wrapping first keeps long lines — notably Ansible error
// paths — fully readable as the user scrolls. ansi.Wrap breaks over-long
// unbreakable tokens (e.g. file paths) and preserves the output's ANSI colour
// codes.
func (m *model) setOutput() {
	w := m.viewport.Width()
	if w < 1 {
		w = 80
	}
	out := ""
	if job, ok := m.shownJob(); ok {
		out = job.Output
	}
	m.viewport.SetContent(ansi.Wrap(out, w, ""))
	m.viewport.GotoBottom()
}

// forward delegates non-key, non-handled messages (blinks, internal ticks) to
// whichever sub-component the active view owns. The board owns none: its tiles are
// rendered from model state on every frame, not by a stateful sub-component with
// its own message loop — which is exactly what the table it replaced was.
func (m model) forward(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.view {
	case viewForm:
		cmds := make([]tea.Cmd, len(m.inputs))
		for i := range m.inputs {
			m.inputs[i], cmds[i] = m.inputs[i].Update(msg)
		}
		return m, tea.Batch(cmds...)
	case viewProgress:
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	case viewBrowse:
		// Deliver the browser's own async dirLoadedMsg (an internal browse type)
		// and any list ticks by forwarding every non-key message to it.
		m.browser, cmd = m.browser.Update(msg)
		return m, cmd
	case viewDest:
		m.dest, cmd = m.dest.Update(msg)
		return m, cmd
	case viewSecrets:
		m.secretsArea, cmd = m.secretsArea.Update(msg)
		return m, cmd
	default:
		return m, nil
	}
}

// updateConfirm routes keys while a destructive-action confirmation is
// pending. updateBoard, updateDetail and updateProgress each hand off to it first
// whenever m.confirm != nil, so no view carries its own overlay key-handling.
// Only the bound Confirm key ('y') dispatches the pending run; every other
// key — including a stray repeat of whatever key raised the overlay — is
// swallowed, so an accidental double-tap can never fire the action twice.
func (m model) updateConfirm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Confirm): // y
		run := m.confirm.run
		// Log the in-flight label so the spinner (raised by beginAction) has a
		// message to sit beside — otherwise a confirmed stop-all/delete would spin
		// against an empty or stale status line.
		if m.confirm.working != "" {
			m.logMsg(m.confirm.working)
		}
		m.confirm = nil
		return m, m.beginAction(run)
	case key.Matches(msg, m.keys.Cancel): // n / esc
		m.confirm = nil
		return m, nil
	}
	return m, nil
}

// confirmView renders the pending confirmation prompt. Shared by boardView,
// detailView and progressView so no screen formats its own overlay text — and
// clipped to ContentWidth like every other line, since a prompt that wrapped
// would cost the screen a row it never budgeted.
func (m model) confirmView() string {
	return m.clipLine(errStyle.Render(m.confirm.prompt + "  [y] yes   [n] cancel"))
}

// View renders the active screen. v2 moved the alt-screen toggle from a
// program option (tea.WithAltScreen(), the v1 entrypoint) into this View
// field, so it is set here instead of in cmd/sand/main.go.
func (m model) View() tea.View {
	var content string
	switch m.view {
	case viewDetail:
		content = m.detailView()
	case viewForm:
		content = m.formView()
	case viewProgress:
		content = m.progressView()
	case viewBrowse:
		content = m.browser.View()
	case viewDest:
		content = m.destView()
	case viewSecrets:
		content = m.secretsView()
	default:
		content = m.boardView()
	}
	v := tea.NewView(content)
	v.AltScreen = true
	// Ask the terminal to report focus, which is what delivers the FocusMsg/BlurMsg
	// that half the heartbeat's idle gate rests on (see shouldTick). A terminal that
	// does not support it simply never sends one, and the gate falls back to "the
	// user is here" — the only safe default, since the alternative is a board that
	// never fills in.
	v.ReportFocus = true
	return v
}
