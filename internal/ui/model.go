// Package ui holds the Bubble Tea model, views, and commands for the sand
// TUI. It is a thin interactive surface over the lima.Client (VM lifecycle),
// provision.Provisioner (create/reset), registry.Registry (which VMs are ours),
// and secrets.Store (per-VM host-side secrets) packages.
//
// Screens divide by responsibility: the list selects a VM and owns the global
// actions (new, filter, search, stop all); the VM screen owns every action that
// targets one VM. All blocking I/O happens in tea.Cmds so Update never stalls,
// and the long-running provisioner streams its output into a scrollable
// progress pane.
package ui

import (
	"context"

	"github.com/lullabot/sandbar/internal/browse"
	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/manage"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// confirmState is a pending destructive action awaiting the user's `y`. It is
// screen-agnostic: the VM screen raises it for delete, and a future stop-all
// (list screen) can raise it the same way — neither screen needs to know what
// the other confirms. A nil *confirmState on the model means no confirmation
// is pending.
type confirmState struct {
	prompt  string  // e.g. `Delete "web"?`
	run     tea.Cmd // dispatched (via beginAction) when the user presses Confirm
	working string  // status shown (with the live spinner) while run is in flight; "" leaves the status line untouched
}

// view is the active screen the model renders and routes keys to.
type view int

const (
	viewList view = iota
	viewDetail
	viewForm
	viewProgress
	viewBrowse
	viewDest
	viewSecrets
)

// model is the root Bubble Tea model. It is passed by value through Update, so
// all fields must be safe to copy (note: no strings.Builder — output is a plain
// string for that reason).
type model struct {
	cli  *lima.Client
	prov *provision.Provisioner
	reg  *registry.Registry
	keys keyMap
	help help.Model

	view   view
	width  int
	height int
	status string

	// List + detail.
	table       table.Model
	vms         []vm.VM
	detail      vm.VM
	managedOnly bool // when true, the list shows only sand-managed VMs

	// Incremental name search. When searching is true, typed keys edit
	// searchQuery instead of firing actions; searchQuery is a case-insensitive
	// name substring filter applied in refreshRows (composes with managedOnly).
	searching   bool
	searchQuery string

	// acting is true while a quick list lifecycle action (start/stop/restart/
	// delete) is in flight. It drives the spinner beside the list status line so
	// these blocking limactl calls show live feedback, and is cleared by the
	// matching actionDoneMsg.
	acting bool

	// Pending destructive-action confirmation (nil = inactive). Raised by
	// whichever screen dispatches the action; routed and rendered from both
	// viewList and viewDetail via updateConfirm/confirmView below.
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

	// Progress / streaming.
	viewport      viewport.Model
	spinner       spinner.Model
	progressTitle string
	progressBack  view // screen to return to when the finished progress view is dismissed
	output        string
	running       bool
	doneErr       error
	reader        *readPipe
	provCfg       vm.CreateConfig    // config of the instance being provisioned (for the managed registry)
	cancel        context.CancelFunc // cancels the in-flight provisioner (ctrl+c on the progress view)
	canceled      bool               // the current/last run was canceled by the user

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
		cli:      cli,
		prov:     prov,
		reg:      reg,
		sec:      sec,
		keys:     defaultKeys(),
		help:     help.New(),
		view:     viewList,
		table:    newTable(),
		viewport: viewport.New(80, 18),
		spinner:  sp,
	}
	// Neither load failure may silently shadow the other.
	switch {
	case loadErr != nil && secErr != nil:
		m.status = "warning: " + loadErr.Error() + "; " + secErr.Error()
	case loadErr != nil:
		m.status = "warning: " + loadErr.Error()
	case secErr != nil:
		m.status = "warning: " + secErr.Error()
	}
	return m
}

// Init kicks off the first list load.
func (m model) Init() tea.Cmd {
	return listCmd(m.cli)
}

// contentWidth is the usable text width inside appStyle's horizontal padding (2
// columns each side). Dynamic help/warning text is wrapped to it (via a style's
// Width) so a long line reflows down the screen instead of being clipped at the
// terminal's right edge. It floors so a not-yet-sized model still renders.
func (m model) contentWidth() int {
	if w := m.width - 4; w >= 20 {
		return w
	}
	return 20
}

// Update is the single dispatch point. Key messages route by active view; all
// other messages (async results, ticks, blinks) are handled or forwarded to the
// active sub-component.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.Width = msg.Width
		m.viewport.Width = max(20, msg.Width-8)
		m.viewport.Height = max(5, msg.Height-12)
		m.table.SetWidth(max(40, msg.Width-6))
		m.table.SetHeight(max(3, msg.Height-12))
		// Resize an active file browser too (its inner list is only initialized
		// while a transfer is in flight, so guard on the view to avoid touching a
		// zero-value browser).
		if m.view == viewBrowse {
			m.browser.SetSize(max(20, msg.Width-6), max(5, msg.Height-8))
		}
		// Keep an open secrets editor sized to the terminal (its height budget
		// must match openSecrets, or the editor plus its footer would overflow and
		// scroll the title off the top).
		if m.view == viewSecrets {
			m.secretsArea.SetWidth(max(20, msg.Width-8))
			m.secretsArea.SetHeight(max(5, msg.Height-secretsChrome))
		}
		// Reflow any streamed output to the new width so it stays wrapped.
		if m.output != "" {
			m.setOutput()
		}
		return m, nil

	case spinner.TickMsg:
		// The spinner animates both for a long provisioner run and for a quick
		// list lifecycle action; when neither is in flight, stop ticking.
		if !m.running && !m.acting {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case vmsLoadedMsg:
		if msg.err != nil {
			m.status = "list failed: " + msg.err.Error()
			return m, nil
		}
		m.vms = msg.vms // DiskUsed is already measured in listCmd, off the Update goroutine
		// Reconcile the managed index against reality so a VM deleted outside the
		// TUI stops being flagged managed (and recreate-able). Shared with the
		// headless `sand create` path (internal/manage) so the two entrypoints
		// cannot drift.
		dropped, err := manage.Reconcile(m.reg, msg.vms)
		if err != nil {
			m.status = "warning: could not update managed index: " + err.Error()
		}
		// A VM that vanished outside the TUI (and so got dropped above) also loses
		// its host-stored secrets — there is no guest left to apply them to, and
		// keeping them around risks silently reattaching stale secrets to an
		// unrelated VM that later reuses the name. Best-effort: a failure here
		// isn't worth displacing the reconcile status above.
		for _, name := range dropped {
			_ = m.sec.Remove(name)
		}
		m.refreshRows()
		// The VM screen acts on the VM it displays, so its snapshot goes stale after
		// every start/stop/restart. Re-seed it from the reloaded list; if the VM is
		// gone (deleted, or removed outside the TUI), fall back to the list rather
		// than rendering a zero-value record.
		if m.view == viewDetail {
			if v, ok := m.lookupVM(m.detail.Name); ok {
				m.detail = v
			} else {
				m.view = viewList
			}
		}
		return m, nil

	case actionDoneMsg:
		m.acting = false // the action finished; stop the list spinner
		label := msg.action + " " + msg.name
		switch {
		case msg.err != nil:
			m.status = label + " failed: " + msg.err.Error()
		case msg.action == "shell":
			m.status = "" // returned from the interactive shell; nothing to report
		case msg.action == "delete":
			// The record the VM screen was displaying no longer exists; only on
			// this success path (msg.err != nil already returned above) — a failed
			// delete leaves the VM in place, so the user should stay on its screen
			// to see the error.
			m.view = viewList
			// A deleted VM is no longer managed, and its host-stored secrets no
			// longer have a guest to apply to; drop it from both indexes. Neither
			// failure may silently shadow the other.
			regErr := m.reg.Remove(msg.name)
			secErr := m.sec.Remove(msg.name)
			switch {
			case regErr != nil && secErr != nil:
				m.status = label + " ok (warning: managed index not updated: " + regErr.Error() + "; secrets not pruned: " + secErr.Error() + ")"
			case regErr != nil:
				m.status = label + " ok (warning: managed index not updated: " + regErr.Error() + ")"
			case secErr != nil:
				m.status = label + " ok (warning: secrets not pruned: " + secErr.Error() + ")"
			default:
				m.status = label + " ok"
			}
		default:
			m.status = label + " ok"
		}
		// A non-fatal ApplySecrets failure (start/restart/apply-secrets) is
		// appended to whatever success status the switch above set; it must never
		// run on the failure branch, which already carries its own error message.
		if msg.err == nil && msg.warn != "" {
			m.status += " (warning: " + msg.warn + ")"
		}
		return m, listCmd(m.cli) // refresh after every action

	case provisionOutputMsg:
		if msg != "" {
			m.output += string(msg)
			m.setOutput()
		}
		return m, readNextCmd(m.reader)

	case provisionDoneMsg:
		m.running = false
		// A user-canceled run leaves partial state behind; don't record it as
		// managed and don't surface its (kill-induced) error as a failure.
		if m.canceled {
			m.doneErr = nil
			return m, listCmd(m.cli)
		}
		m.doneErr = msg.err
		// A successful create/recreate yields a sand-managed VM; record it
		// (with its config, for a faithful future recreate) so the list marks it
		// and recreate stays available for it. Shared with the headless
		// `sand create` path (internal/manage) so the two entrypoints cannot drift.
		var applyCmd tea.Cmd
		if msg.err == nil && m.provCfg.Name != "" {
			if err := manage.RecordSuccess(m.reg, m.provCfg); err != nil {
				m.status = "VM ready, but recording it as managed failed: " + err.Error()
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
			if m.provCfg.CloneToken != "" {
				pairs := m.sec.Get(m.provCfg.Name)
				pairs["GH_TOKEN"] = m.provCfg.CloneToken
				if err := m.sec.Set(m.provCfg.Name, pairs); err != nil {
					m.status = "VM ready, but the token could not be saved as a secret: " + err.Error()
				}
			}
			// createVM/Reset each end with their own StartStreaming, which runs
			// BEFORE this handler and before GH_TOKEN lands in the store above —
			// so the guest would have no secrets.env until the VM's *next* start.
			// Dispatch the apply now (batched with the list refresh) so a user who
			// creates a VM and immediately shells in finds GH_TOKEN already set.
			user, scopes := m.secretsFor(m.provCfg.Name)
			applyCmd = applySecretsCmd(m.cli, m.provCfg.Name, user, scopes)
		}
		if applyCmd != nil {
			return m, tea.Batch(listCmd(m.cli), applyCmd)
		}
		return m, listCmd(m.cli) // refresh the list the user returns to

	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			// While a build is in flight, ctrl+c cancels it (killing the underlying
			// limactl via the provisioner's context) and shows the result, rather
			// than quitting the whole TUI and orphaning a half-built VM. Everywhere
			// else — including the finished progress screen — ctrl+c quits.
			if m.view == viewProgress && m.running {
				if m.cancel != nil {
					m.cancel()
				}
				m.canceled = true
				m.output += "\n^C — canceling, cleaning up…\n"
				m.setOutput()
				return m, nil
			}
			return m, tea.Quit
		}
		switch m.view {
		case viewList:
			return m.updateList(msg)
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

// setOutput wraps the accumulated provisioner output to the viewport width and
// pins the view to the bottom. The bubbles viewport truncates lines wider than
// its width; wrapping first keeps long lines — notably Ansible error paths —
// fully readable as the user scrolls. ansi.Wrap breaks over-long unbreakable
// tokens (e.g. file paths) and preserves the output's ANSI colour codes.
func (m *model) setOutput() {
	w := m.viewport.Width
	if w < 1 {
		w = 80
	}
	m.viewport.SetContent(ansi.Wrap(m.output, w, ""))
	m.viewport.GotoBottom()
}

// forward delegates non-key, non-handled messages (blinks, internal ticks) to
// whichever sub-component the active view owns.
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
		m.table, cmd = m.table.Update(msg)
		return m, cmd
	}
}

// updateConfirm routes keys while a destructive-action confirmation is
// pending. Both updateList and updateDetail hand off to it first whenever
// m.confirm != nil, so neither view carries its own overlay key-handling.
// Only the bound Confirm key ('y') dispatches the pending run; every other
// key — including a stray repeat of whatever key raised the overlay — is
// swallowed, so an accidental double-tap can never fire the action twice.
func (m model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Confirm): // y
		run := m.confirm.run
		// Seed the in-flight status so the spinner (raised by beginAction) has a
		// label to sit beside — otherwise a confirmed stop-all/delete would spin
		// against an empty or stale status line.
		if m.confirm.working != "" {
			m.status = m.confirm.working
		}
		m.confirm = nil
		return m, m.beginAction(run)
	case key.Matches(msg, m.keys.Cancel): // n / esc
		m.confirm = nil
		return m, nil
	}
	return m, nil
}

// confirmView renders the pending confirmation prompt. Shared by listView and
// detailView so neither screen formats its own overlay text.
func (m model) confirmView() string {
	return errStyle.Render(m.confirm.prompt + "  [y] yes   [n] cancel")
}

// View renders the active screen.
func (m model) View() string {
	switch m.view {
	case viewDetail:
		return m.detailView()
	case viewForm:
		return m.formView()
	case viewProgress:
		return m.progressView()
	case viewBrowse:
		return m.browser.View()
	case viewDest:
		return m.destView()
	case viewSecrets:
		return m.secretsView()
	default:
		return m.listView()
	}
}
