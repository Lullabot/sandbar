package ui

// commandreg.go is the single command registry for every verb sand offers on a
// focused VM: one entry carries its key binding, its help text, the enabledFor
// predicate that gates whether it applies to that VM, and the action it runs.
// updateBoard (the dispatcher) and boardHelp (the footer) both derive from this
// one list — see board.go — so they can no longer silently disagree the way the
// old hand-maintained keymap-constructor / per-view-help-switch pair did: the old
// help switch offered Start, Stop, Restart, Reset, Shell, Delete, Upload,
// Download, and Secrets unconditionally, so a STOPPED VM's help bar advertised
// "x stop" even though pressing it did nothing.
//
// Every verb fires on THE TILE UNDER THE RING, straight from the board. There is
// no VM screen to open first — it was deleted, because the tile already showed
// everything it did.
//
// This file stays narrow on purpose: it is exactly the verbs sand has today,
// nothing more. It is not a fuzzy command palette or a general plugin
// framework — see the task's scope note before adding to it.

import (
	"fmt"

	"github.com/lullabot/sandbar/internal/manage"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// vmCommand is one per-VM verb on the detail screen. enabledFor is handed the
// model (not just the VM) so a predicate can consult state the VM record
// itself doesn't carry — e.g. the job-registry seam below.
type vmCommand struct {
	// id names the verb for the ONE caller that needs to reach a specific one
	// rather than walk them all: enterTarget, which routes Enter to whichever of
	// these a VM's current state calls for. Set only on the verbs Enter can pick;
	// "" everywhere else. Enter runs the entry it finds here — it does not carry
	// its own copy of the action — so the two can never drift.
	id      string
	binding key.Binding
	// about is one sentence explaining what this verb does, for the `?` screen
	// (help.go). The binding's own help text is a two-word label for the footer —
	// "u upload" tells you the key, not what it copies where. This is the sentence a
	// user reads once and then never needs again.
	about      string
	enabledFor func(m model, v boardVM) bool
	action     func(m *model, v boardVM) tea.Cmd
}

// vmBuilding reports whether (scope, name) is mid-build. Nil-safe: a model with
// no job registry has nothing building. Scoped, so a fleet's same-named VMs are
// gated independently.
func (m model) vmBuilding(scope registry.Scope, name string) bool {
	return m.jobs.Building(scope, name)
}

// vmHasRetainedRun reports whether (scope, name) has a run whose log can be
// reopened. Nil-safe, for the same reason.
func (m model) vmHasRetainedRun(scope registry.Scope, name string) bool {
	return m.jobs.HasRetainedRun(scope, name)
}

// alwaysEnabled is the enabledFor for vmCommands that genuinely have nothing
// to gate: Restart always performs a real stop-then-start regardless of the
// VM's current status (a stopped VM's "stop" half is a harmless no-op, but
// the "start" half is real work), and Secrets is legitimate to open and save
// on both a running VM (task 06: applies live to the guest) and a stopped
// one (it applies on next start) — see vmCommands below. Every other
// command that used to sit here (Reset, Upload, Download) had a real
// decline branch that advertised the verb and then did nothing but set a
// status message; those now gate in enabledFor instead.
func alwaysEnabled(model, boardVM) bool { return true }

// notBuilding gates every verb that would DISRUPT A BUILD, and it is the guard the
// board's own liveness made necessary.
//
// A VM mid-provision is `Running` TO LIMA — Ansible is executing inside a booted
// guest — so a gate that reads only vm.Status cheerfully offers "x stop" on a VM
// whose build it would kill, and start's `Status != Running` even offers "s start"
// on a VM Lima has never heard of (a create's clone has not landed; lookupVM hands
// back a synthetic record with an empty Status). A RESET is worse still: it has
// deleted its own instance and will clone it back, so a copy launched into it in
// that window streams files into a VM that is about to be destroyed.
//
// Only Delete consulted the registry, because it was the only verb that could
// obviously destroy a build. The others were protected by ACCIDENT: the old
// full-screen progress view froze the keyboard for the whole build, so no key
// could reach them. This plan removed that freeze — deliberately, it is the
// headline feature — and these gates are what has to replace it.
func notBuilding(m model, v boardVM) bool { return !m.vmBuilding(v.scope, v.Name) }

// enterTarget is what Enter does to the tile under the ring: the ONE obvious
// thing for the state that tile is in.
//
//	building  -> log    (show me what it is doing)
//	running   -> shell  (let me in)
//	otherwise -> start  (wake it up)
//
// Enter is a shortcut, not a fourth verb: it returns an existing entry from
// vmCommands and the caller runs THAT entry's action, so Enter cannot drift from
// the key it stands in for, and it inherits that verb's enabledFor for free —
// which is what makes the gates hold. Pressing Enter on a VM mid-build cannot
// start or shell into it, because the verb it would route to is disabled and
// this returns false.
//
// The building case is checked FIRST and deliberately: a VM mid-provision is
// `Running` to Lima (Ansible is executing inside a booted guest), so asking about
// Status first would route Enter to shell and drop the user into a guest that a
// build — or a reset that is about to force-delete it — currently owns.
//
// ok=false means Enter does nothing here, and boardHelp must not advertise it.
// That happens when the routed verb is gated off: a building VM whose run is not
// retained (nothing to show yet), for instance.
func (m model) enterTarget(v boardVM) (vmCommand, bool) {
	var want string
	switch {
	case m.vmBuilding(v.scope, v.Name):
		want = "log"
	case v.Status == limaRunning:
		want = "shell"
	default:
		want = "start"
	}
	for _, c := range vmCommands {
		if c.id == want {
			if !c.enabledFor(m, v) {
				return vmCommand{}, false
			}
			return c, true
		}
	}
	return vmCommand{}, false
}

// vmCommands is every per-VM verb, in help-bar display order. They all fire on the
// tile under the board's focus ring.
var vmCommands = []vmCommand{
	{
		id:         "start",
		binding:    key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "start")),
		about:      "Boot the VM. Its host-stored secrets are written into the guest as it comes up.",
		enabledFor: func(m model, v boardVM) bool { return notBuilding(m, v) && v.Status != limaRunning },
		action: func(m *model, v boardVM) tea.Cmd {
			m.logMsg("starting " + v.Name + "…")
			user, scopes := m.secretsFor(v.scope, v.Name)
			return m.beginAction(startCmd(m.provFor(v.scope), v.scope, v.Name, user, scopes))
		},
	},
	{
		binding:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "stop")),
		about:      "Shut the VM down cleanly. Its disk and its secrets are kept.",
		enabledFor: func(m model, v boardVM) bool { return notBuilding(m, v) && v.Status == limaRunning },
		action: func(m *model, v boardVM) tea.Cmd {
			m.logMsg("stopping " + v.Name + "…")
			return m.beginAction(stopCmd(m.provFor(v.scope), v.scope, v.Name))
		},
	},
	{
		binding:    key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restart")),
		about:      "Stop the VM and start it again, applying any secrets you have changed since it booted.",
		enabledFor: notBuilding,
		action: func(m *model, v boardVM) tea.Cmd {
			m.logMsg("restarting " + v.Name + "…")
			user, scopes := m.secretsFor(v.scope, v.Name)
			return m.beginAction(restartCmd(m.provFor(v.scope), v.scope, v.Name, user, scopes))
		},
	},
	{
		binding: key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "reset")),
		about:   "Delete this VM and clone it fresh from its base image, keeping its name and sizing. Everything inside the guest is lost; the form opens pre-filled so you can change the settings first.",
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
		enabledFor: func(m model, v boardVM) bool {
			if !notBuilding(m, v) {
				return false
			}
			_, ok := manage.RecreateBase(m.reg, v.Name, v.scope)
			return ok
		},
		action: func(m *model, v boardVM) tea.Cmd {
			name := v.Name
			base, ok := manage.RecreateBase(m.reg, name, v.scope)
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
			cfg, found := m.reg.ConfigInScope(name, v.scope)
			if !found || cfg.Name == "" {
				cfg = vm.DefaultCreateConfig()
				cfg.Name = name
				cfg.User = hostUser()
				cfg.GitName = hostGit("user.name")
				cfg.GitEmail = hostGit("user.email")
			}
			cfg.BaseName = base
			// A reset targets the VM's OWN member — the form's provider, host
			// defaults and bookkeeping resolve through v.scope, not the active one.
			return m.openResetForm(v.scope, name, cfg)
		},
	},
	{
		id:      "shell",
		binding: key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "shell")),
		about: "Attach a shell to the guest's persistent tmux session. Work keeps running after " +
			"you detach (C-a d) or close the terminal; C-a c opens another window.",
		// Not while a build or a reset owns the VM, for the same reason as every other
		// verb here — and shell is the worst of them to get wrong: on a VM a reset is
		// about to force-delete, it would drop the user into a session whose guest is
		// destroyed under them, while the build they can no longer see streams into a
		// (possibly suspended) terminal. It was the one verb this gate was not applied
		// to. This holds regardless of which of shellCmd's two branches fires — even
		// the host-tmux fast path, which does not suspend the TUI, still attaches a
		// live session to a VM the reset is about to delete — so kept exactly as-is
		// (considered and deliberately retained, not an oversight to "fix").
		enabledFor: func(m model, v boardVM) bool { return notBuilding(m, v) && v.Status == limaRunning },
		action: func(m *model, v boardVM) tea.Cmd {
			// Same predicate shellCmd branches on, so the copy always describes the
			// branch that actually fires.
			if hostInTmux() {
				m.logMsg("opened " + v.Name + " in a new tmux window")
			} else {
				m.logMsg("attaching to " + v.Name + " — C-a d detaches; the TUI resumes when you detach or exit")
			}
			return shellCmd(m.provFor(v.scope), v.scope, v.VM)
		},
	},
	{
		binding: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
		about:   "Delete the VM and its disk, after a confirmation. Its host-stored secrets go with it.",
		// Delete raises the confirm overlay unconditionally today; the job
		// registry (task 04) will additionally disable Delete while a VM is
		// mid-build via vmBuilding.
		enabledFor: func(m model, v boardVM) bool { return !m.vmBuilding(v.scope, v.Name) },
		action: func(m *model, v boardVM) tea.Cmd {
			name := v.Name
			// Delete's action RAISES the confirm; it never deletes directly — the
			// actual deleteCmd only runs once the user presses 'y' (updateConfirm
			// in model.go). deleteCmd carries the owning scope so the actionDoneMsg
			// handler prunes THIS profile's job/secrets, never a same-named VM's
			// under another.
			m.confirm = &confirmState{
				prompt:  fmt.Sprintf("Delete %q?", name),
				run:     deleteCmd(m.provFor(v.scope), v.scope, name),
				working: "deleting " + name + "…",
			}
			return nil
		},
	},
	{
		binding: key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "upload")),
		about:   "Copy a file or directory from this machine into the guest. You pick the source, then the destination directory.",
		// Both directions require a running VM (limactl copy needs the guest up).
		// This used to be an in-action check that surfaced a "must be running"
		// status message and did nothing else — exactly the lying-footer pattern
		// Shell was already fixed for. Gated the same way here.
		//
		// And NOT while a build owns the VM. A reset deletes its instance and clones
		// it back, so a copy launched into it mid-reset streams the user's files into
		// a VM that is about to be destroyed — and reports success. submitReset already
		// refuses a reset while a copy runs; this is the other half of that guard.
		enabledFor: func(m model, v boardVM) bool { return notBuilding(m, v) && v.Status == limaRunning },
		action: func(m *model, v boardVM) tea.Cmd {
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
		about:      "Copy a file or directory out of the guest onto this machine.",
		enabledFor: func(m model, v boardVM) bool { return notBuilding(m, v) && v.Status == limaRunning },
		action: func(m *model, v boardVM) tea.Cmd {
			next, cmd := m.startTransfer(v, false) // guest → host
			*m = next.(model)
			return cmd
		},
	},
	{
		binding: key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "paste image")),
		about:   "Stage the host clipboard's image on the guest's single-slot clip file, ready for `S` then Ctrl-V into whatever the guest shell is running.",
		// Same guard as Shell/Upload/Download: writing into the guest needs it up,
		// and not while a build or a reset owns it (see notBuilding's doc comment) —
		// a reset deletes its instance and clones it back, so a paste mid-reset would
		// write into a guest about to be destroyed.
		enabledFor: func(m model, v boardVM) bool { return notBuilding(m, v) && v.Status == limaRunning },
		action: func(m *model, v boardVM) tea.Cmd {
			return pasteCmd(m.provFor(v.scope), v.VM)
		},
	},
	{
		binding:    key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "secrets")),
		about:      "Edit this VM's secrets. Saving writes them into a running guest immediately; a stopped one gets them on its next start.",
		enabledFor: alwaysEnabled, // secrets live on the host, editable whether or not the VM is up
		action: func(m *model, v boardVM) tea.Cmd {
			return m.openSecrets(v.scope, v.Name)
		},
	},
	{
		id:      "log",
		binding: key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "log")),
		about:   "Reopen the log of this VM's last build or file transfer, including one still running, and one that failed.",
		// Reopen the VM's last run in the progress viewport. Ansible's output used
		// to be ephemeral — it streamed into that viewport and was gone the moment
		// you left the screen — so a provision that failed while you looked away was
		// simply unexplainable. The job registry retains the run anyway (that is
		// what makes a failed VM's Failed status sticky), so this verb is the
		// difference between a red tile that reports a problem and one that can
		// explain it. Offered only when there is a run to show.
		enabledFor: func(m model, v boardVM) bool { return m.vmHasRetainedRun(v.scope, v.Name) },
		action: func(m *model, v boardVM) tea.Cmd {
			return m.showJobLog(v.scope, v.Name)
		},
	},
}
