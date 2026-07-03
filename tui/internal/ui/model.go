// Package ui holds the Bubble Tea model, views, and commands for the claude-vm
// TUI. It is a thin interactive surface over the lima.Client (VM lifecycle) and
// provision.Provisioner (create/recreate) packages: all blocking I/O happens in
// tea.Cmds so Update never stalls, and the long-running provisioner streams its
// output into a scrollable progress pane.
package ui

import (
	"context"
	"strconv"

	"github.com/deviantintegral/claude-code-ansible/tui/internal/lima"
	"github.com/deviantintegral/claude-code-ansible/tui/internal/provision"
	"github.com/deviantintegral/claude-code-ansible/tui/internal/registry"
	"github.com/deviantintegral/claude-code-ansible/tui/internal/vm"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// view is the active screen the model renders and routes keys to.
type view int

const (
	viewList view = iota
	viewDetail
	viewForm
	viewProgress
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
	managedOnly bool // when true, the list shows only claude-vm-managed VMs

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

	// Delete/recreate confirmation overlay on the list.
	confirming  bool
	confirmName string
	confirmBase string // recreate base for a managed VM; "" disables recreate

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
	projectToggleEnabled bool // false when the VM has no CloneURL (nothing to preserve)
	toggleFocus          int  // -1 = focus is in the text inputs; 0/1 = a toggle is focused

	// Progress / streaming.
	viewport      viewport.Model
	spinner       spinner.Model
	progressTitle string
	output        string
	running       bool
	doneErr       error
	reader        *readPipe
	provCfg       vm.CreateConfig    // config of the instance being provisioned (for the managed registry)
	cancel        context.CancelFunc // cancels the in-flight provisioner (ctrl+c on the progress view)
	canceled      bool               // the current/last run was canceled by the user
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

	m := model{
		cli:      cli,
		prov:     prov,
		reg:      reg,
		keys:     defaultKeys(),
		help:     help.New(),
		view:     viewList,
		table:    newTable(),
		viewport: viewport.New(80, 18),
		spinner:  sp,
	}
	if loadErr != nil {
		m.status = "warning: " + loadErr.Error()
	}
	return m
}

// Init kicks off the first list load.
func (m model) Init() tea.Cmd {
	return listCmd(m.cli)
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
		m.vms = msg.vms
		// Measure each VM's real disk consumption once per load (a single stat per
		// VM, microseconds, no file contents read). A non-positive result means the
		// disk couldn't be measured; leave DiskUsed empty so the cell renders blank.
		for i := range m.vms {
			if n := diskUsedBytes(m.vms[i].Dir); n > 0 {
				m.vms[i].DiskUsed = strconv.FormatInt(n, 10)
			}
		}
		// Reconcile the managed index against reality so a VM deleted outside the
		// TUI stops being flagged managed (and recreate-able).
		present := make(map[string]bool, len(msg.vms))
		for _, v := range msg.vms {
			present[v.Name] = true
		}
		if _, err := m.reg.Reconcile(present); err != nil {
			m.status = "warning: could not update managed index: " + err.Error()
		}
		m.refreshRows()
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
			// A deleted VM is no longer managed; drop it from the index.
			if err := m.reg.Remove(msg.name); err != nil {
				m.status = label + " ok (warning: managed index not updated: " + err.Error() + ")"
			} else {
				m.status = label + " ok"
			}
		default:
			m.status = label + " ok"
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
		// A successful create/recreate yields a claude-vm-managed VM; record it
		// (with its config, for a faithful future recreate) so the list marks it
		// and recreate stays available for it.
		if msg.err == nil && m.provCfg.Name != "" {
			if err := m.reg.Add(m.provCfg); err != nil {
				m.status = "VM ready, but recording it as managed failed: " + err.Error()
			}
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
	default:
		m.table, cmd = m.table.Update(msg)
		return m, cmd
	}
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
	default:
		return m.listView()
	}
}
