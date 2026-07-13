package ui

// commandreg.go is the VM (detail) screen's single command registry: for
// every verb sand offers on a focused VM, one entry carries its key binding,
// its help text, the enabledFor predicate that gates whether it applies to
// that VM, and the action it runs. updateDetail (the dispatcher) and
// detailHelp (the footer) both derive from this one list — see detail.go —
// so they can no longer silently disagree the way the old hand-maintained
// keymap-constructor / per-view-help-switch pair did: the old help switch
// offered Start, Stop, Restart, Reset, Shell, Delete, Upload, Download, and
// Secrets unconditionally, so a STOPPED VM's help bar advertised "x stop"
// even though pressing it did nothing.
//
// Two kinds of keys exist on this screen: per-VM verbs, gated by
// enabledFor(model, vm.VM), and screen-local chrome (Back, Quit), which has
// no VM to gate on. They are modeled as two distinct types (vmCommand,
// chromeCommand) rather than passing a zero vm.VM to a chrome predicate.
//
// This file stays narrow on purpose: it is exactly the verbs sand has today,
// nothing more. It is not a fuzzy command palette or a general plugin
// framework — see the task's scope note before adding to it.

import (
	"fmt"

	"github.com/lullabot/sandbar/internal/manage"
	"github.com/lullabot/sandbar/internal/vm"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// vmCommand is one per-VM verb on the detail screen. enabledFor is handed the
// model (not just the VM) so a predicate can consult state the VM record
// itself doesn't carry — e.g. the job-registry seam below.
type vmCommand struct {
	binding    key.Binding
	help       string // short name, for diagnostics/tests only (the binding carries the real help text)
	enabledFor func(m model, v vm.VM) bool
	action     func(m *model, v vm.VM) tea.Cmd
}

// chromeCommand is a detail-screen key with no VM to gate on.
type chromeCommand struct {
	binding    key.Binding
	help       string
	enabledFor func(m model) bool
	action     func(m *model) tea.Cmd
}

// jobLookup is the narrow view of the job registry (jobs.go) that this file's
// predicates need. *jobRegistry is its only implementation; keeping the
// interface makes the two things the registry gates here explicit, and keeps
// them the only things it gates. A nil registry answers false to both, so a
// model built without one behaves exactly as it did before jobs existed.
type jobLookup interface {
	// Building reports whether name has a build/provision in flight. It gates
	// Delete below (a VM mid-build must not be deleted out from under itself).
	// A file transfer is not a build, and does not gate it.
	Building(name string) bool
	// HasRetainedRun reports whether name has a run whose log can be reopened,
	// INCLUDING one still in flight: now that leaving the progress screen no
	// longer abandons a build, "show me this VM's log" has to work while it is
	// still being written, not only after it stops. It gates the log verb below.
	HasRetainedRun(name string) bool
}

// vmBuilding reports whether name is mid-build. Nil-safe: a model with no job
// registry has nothing building.
func (m model) vmBuilding(name string) bool {
	return m.jobs.Building(name)
}

// vmHasRetainedRun reports whether name has a run whose log can be reopened.
// Nil-safe, for the same reason.
func (m model) vmHasRetainedRun(name string) bool {
	return m.jobs.HasRetainedRun(name)
}

// alwaysEnabled is the enabledFor for vmCommands that genuinely have nothing
// to gate: Restart always performs a real stop-then-start regardless of the
// VM's current status (a stopped VM's "stop" half is a harmless no-op, but
// the "start" half is real work), and Secrets is legitimate to open and save
// on both a running VM (task 06: applies live to the guest) and a stopped
// one (it applies on next start) — see detailCommands below. Every other
// command that used to sit here (Reset, Upload, Download) had a real
// decline branch that advertised the verb and then did nothing but set a
// status message; those now gate in enabledFor instead.
func alwaysEnabled(model, vm.VM) bool { return true }

// chromeAlwaysEnabled is alwaysEnabled's chromeCommand counterpart.
func chromeAlwaysEnabled(model) bool { return true }

// detailCommands is the VM screen's per-VM verbs, in help-bar display order.
var detailCommands = []vmCommand{
	{
		binding:    key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "start")),
		help:       "start",
		enabledFor: func(_ model, v vm.VM) bool { return v.Status != limaRunning },
		action: func(m *model, v vm.VM) tea.Cmd {
			m.logMsg("starting " + v.Name + "…")
			user, scopes := m.secretsFor(v.Name)
			return m.beginAction(startCmd(m.cli, v.Name, user, scopes))
		},
	},
	{
		binding:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "stop")),
		help:       "stop",
		enabledFor: func(_ model, v vm.VM) bool { return v.Status == limaRunning },
		action: func(m *model, v vm.VM) tea.Cmd {
			m.logMsg("stopping " + v.Name + "…")
			return m.beginAction(stopCmd(m.cli, v.Name))
		},
	},
	{
		binding:    key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restart")),
		help:       "restart",
		enabledFor: alwaysEnabled,
		action: func(m *model, v vm.VM) tea.Cmd {
			m.logMsg("restarting " + v.Name + "…")
			user, scopes := m.secretsFor(v.Name)
			return m.beginAction(restartCmd(m.cli, v.Name, user, scopes))
		},
	},
	{
		binding: key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "reset")),
		help:    "reset",
		// Reset clones from a Claude base, so it is only offered for VMs we
		// created — otherwise it would replace an unrelated VM with a sandbox.
		// Shared with the headless `sand create` path (internal/manage) so the
		// two entrypoints cannot drift on the gate. This used to be an in-action
		// check that explained itself via the status line when it declined, but
		// that is exactly the "advertise it, then no-op with an explanation"
		// pattern the registry exists to eliminate (see Upload/Download above):
		// pressing R on an unmanaged VM opened no form and changed nothing. The
		// "why" is already visible in the VM record's Managed field, the same
		// way Status explains a hidden Start/Stop, so gating here loses no
		// information a user could act on.
		enabledFor: func(m model, v vm.VM) bool {
			_, ok := manage.RecreateBase(m.reg, v.Name)
			return ok
		},
		action: func(m *model, v vm.VM) tea.Cmd {
			name := v.Name
			base, ok := manage.RecreateBase(m.reg, name)
			if !ok {
				// Unreachable via normal dispatch — enabledFor above already excludes
				// this VM. Guarded anyway so a direct call never opens a form with a
				// blank BaseName.
				return nil
			}
			// Pre-fill the reset form from the VM's recorded config (sizing,
			// hostname, identity) rather than resetting to defaults. The clone
			// token is not stored, so a VM that cloned a private repo will need it
			// re-supplied. Fall back to a minimal config if no snapshot exists
			// (e.g. a pre-snapshot index entry).
			cfg, found := m.reg.Config(name)
			if !found || cfg.Name == "" {
				cfg = vm.DefaultCreateConfig()
				cfg.Name = name
				cfg.User = hostUser()
				cfg.GitName = hostGit("user.name")
				cfg.GitEmail = hostGit("user.email")
			}
			cfg.BaseName = base
			return m.openResetForm(name, cfg)
		},
	},
	{
		binding:    key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "shell")),
		help:       "shell",
		enabledFor: func(_ model, v vm.VM) bool { return v.Status == limaRunning },
		action: func(m *model, v vm.VM) tea.Cmd {
			m.logMsg("opening a shell in " + v.Name + " — the TUI resumes when you exit")
			return shellCmd(v.Name)
		},
	},
	{
		binding: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
		help:    "delete",
		// Delete raises the confirm overlay unconditionally today; the job
		// registry (task 04) will additionally disable Delete while a VM is
		// mid-build via vmBuilding — see jobLookup above.
		enabledFor: func(m model, v vm.VM) bool { return !m.vmBuilding(v.Name) },
		action: func(m *model, v vm.VM) tea.Cmd {
			name := v.Name
			// Delete's action RAISES the confirm; it never deletes directly — the
			// actual deleteCmd only runs once the user presses 'y' (updateConfirm
			// in model.go).
			m.confirm = &confirmState{
				prompt:  fmt.Sprintf("Delete %q?", name),
				run:     deleteCmd(m.cli, name),
				working: "deleting " + name + "…",
			}
			return nil
		},
	},
	{
		binding: key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "upload")),
		help:    "upload",
		// Both directions require a running VM (limactl copy needs the guest up).
		// This used to be an in-action check that surfaced a "must be running"
		// status message and did nothing else — exactly the lying-footer pattern
		// Shell was already fixed for. Gated the same way here.
		enabledFor: func(_ model, v vm.VM) bool { return v.Status == limaRunning },
		action: func(m *model, v vm.VM) tea.Cmd {
			next, cmd := m.startTransfer(v, true) // host → guest
			*m = next.(model)
			return cmd
		},
	},
	{
		// 'd' stays delete on every screen: it is the most destructive key and its
		// meaning must never change under the user's fingers. Download took the
		// rename to 'g'. 'g' deliberately collides with bubbles/table's GotoTop —
		// harmless here since the detail screen has no table.
		binding:    key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "download")),
		help:       "download",
		enabledFor: func(_ model, v vm.VM) bool { return v.Status == limaRunning },
		action: func(m *model, v vm.VM) tea.Cmd {
			next, cmd := m.startTransfer(v, false) // guest → host
			*m = next.(model)
			return cmd
		},
	},
	{
		binding:    key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "secrets")),
		help:       "secrets",
		enabledFor: alwaysEnabled, // secrets live on the host, editable whether or not the VM is up
		action: func(m *model, v vm.VM) tea.Cmd {
			return m.openSecrets(v.Name)
		},
	},
	{
		binding: key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "log")),
		help:    "log",
		// Reopen the VM's last run in the progress viewport. Ansible's output used
		// to be ephemeral — it streamed into that viewport and was gone the moment
		// you left the screen — so a provision that failed while you looked away was
		// simply unexplainable. The job registry retains the run anyway (that is
		// what makes a failed VM's Failed status sticky), so this verb is the
		// difference between a red tile that reports a problem and one that can
		// explain it. Offered only when there is a run to show.
		enabledFor: func(m model, v vm.VM) bool { return m.vmHasRetainedRun(v.Name) },
		action: func(m *model, v vm.VM) tea.Cmd {
			return m.showJobLog(v.Name)
		},
	},
}

// detailChrome is the detail screen's non-per-VM keys: back to the board, and
// quit. Rendered/dispatched after detailCommands (see updateDetail/detailHelp
// in detail.go) so a verb key never gets swallowed by chrome.
var detailChrome = []chromeCommand{
	{
		// esc/backspace/enter all return to the board; only "esc" is shown in the
		// help bar (matching the pre-registry behaviour, where Enter worked as an
		// undocumented alias). The board's focus ring is pinned to the VM's NAME,
		// so coming back lands on the same tile the user zoomed in from.
		binding:    key.NewBinding(key.WithKeys("esc", "backspace", "enter"), key.WithHelp("esc", "back")),
		help:       "back",
		enabledFor: chromeAlwaysEnabled,
		action: func(m *model) tea.Cmd {
			m.view = viewBoard
			return nil
		},
	},
	{
		binding:    key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		help:       "quit",
		enabledFor: chromeAlwaysEnabled,
		// requestQuit (board.go), not a bare tea.Quit: builds now run in the
		// background, so the reflex that ends a session must not silently orphan one.
		action: func(m *model) tea.Cmd { return m.requestQuit() },
	},
}
