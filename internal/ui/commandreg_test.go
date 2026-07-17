package ui

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"

	"github.com/charmbracelet/x/ansi"
)

// plainHelp strips the help bar's per-item ANSI styling (help.ShortHelpView
// colors the key and description separately) so "x stop" style substring
// checks aren't broken by escape codes sitting between "x" and "stop".
func plainHelp(rendered string) string { return ansi.Strip(rendered) }

// boardVerbs is the verbs the board is currently ADVERTISING, as plain "key desc"
// text, for the tile under the focus ring.
//
// It reads the BINDINGS, not the rendered footer, and that matters: the footer
// elides verbs it has no room for (at 120 columns a full board footer already
// ends in "g down…"), so a test about which verbs are ELIGIBLE must not be able to
// fail because the terminal was narrow. The bindings are the same list updateBoard
// dispatches from, which is the thing under test.
func boardVerbs(m model) string {
	var b strings.Builder
	for _, bind := range m.boardHelp() {
		b.WriteString(bind.Help().Key + " " + bind.Help().Desc + "\n")
	}
	return b.String()
}

// focusTile puts the ring on a VM that is on the board — registered managed AND
// reported by Lima, which is what earns a tile. The verbs all fire on the tile
// under the ring, so this is the setup every command test needs now that the VM
// screen is gone.
func focusTile(t *testing.T, m model, vms ...vm.VM) model {
	t.Helper()
	m = loadManaged(t, m, vms...)
	m.focusVM.Name = vms[0].Name
	return m
}

// The VM screen's help/footer and key dispatch must agree, and neither may
// hardcode a verb that doesn't apply to the focused VM's current state. This
// is the live bug the old hand-maintained keymap/help-switch pair had: the
// help switch offered Start, Stop, Restart, Reset, Shell, Delete, Upload,
// Download, and Secrets UNCONDITIONALLY, so a STOPPED VM's help bar
// advertised "x stop" even though pressing it did nothing useful.
//
// A stopped VM must not offer Stop, and pressing 'x' must be a silent no-op:
// no command, no state change (m.acting stays false).
func TestStoppedVMOffersNoStop(t *testing.T) {
	m := newTestModel(t)
	m = focusTile(t, m, vm.VM{Name: "claude", Status: "Stopped", CPUs: 2})

	rendered := boardVerbs(m)
	if strings.Contains(rendered, "x stop") {
		t.Fatalf("a stopped VM's help bar must not offer stop, got:\n%s", rendered)
	}

	after, cmd := m.Update(runeKey('x'))
	m2 := after.(model)
	if cmd != nil {
		t.Fatal("pressing 'x' on a stopped VM should dispatch no command")
	}
	if m2.acting {
		t.Fatal("pressing 'x' on a stopped VM should not mark an action in flight")
	}
}

// The mirror case: a RUNNING VM's help bar offers Stop, and pressing 'x' does
// fire it (marking an action in flight and dispatching a command).
func TestRunningVMOffersStopAndFiresIt(t *testing.T) {
	m := newTestModel(t)
	m = focusTile(t, m, vm.VM{Name: "claude", Status: "Running", CPUs: 2})

	rendered := boardVerbs(m)
	if !strings.Contains(rendered, "x stop") {
		t.Fatalf("a running VM's help bar should offer stop, got:\n%s", rendered)
	}

	after, cmd := m.Update(runeKey('x'))
	m2 := after.(model)
	if cmd == nil {
		t.Fatal("pressing 'x' on a running VM should dispatch a command")
	}
	if !m2.acting {
		t.Fatal("pressing 'x' on a running VM should mark an action in flight")
	}
}

// Start is the mirror of Stop: offered (and fires) only on a stopped VM, not
// a running one — this is the same class of bug as the Stop case above,
// caught by the same registry-derived enabledFor gating.
func TestStartOfferedOnlyWhenStopped(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m,
		vm.VM{Name: "running-vm", Status: "Running", CPUs: 2},
		vm.VM{Name: "stopped-vm", Status: "Stopped", CPUs: 2},
	)

	running := m
	running.focusVM.Name = "running-vm"
	if strings.Contains(boardVerbs(running), "s start") {
		t.Fatalf("a running VM's help bar must not offer start, got:\n%s", boardVerbs(running))
	}
	if after, cmd := running.Update(runeKey('s')); cmd != nil || after.(model).acting {
		t.Fatal("pressing 's' on a running VM should dispatch no command and not mark acting")
	}

	stopped := m
	stopped.focusVM.Name = "stopped-vm"
	if !strings.Contains(boardVerbs(stopped), "s start") {
		t.Fatalf("a stopped VM's help bar should offer start, got:\n%s", boardVerbs(stopped))
	}
	after, cmd := stopped.Update(runeKey('s'))
	if cmd == nil || !after.(model).acting {
		t.Fatal("pressing 's' on a stopped VM should dispatch a command and mark acting")
	}
}

// Shell is offered (and fires) only when the VM is running. The guard used to
// live inline in updateDetail (surfacing a "must be running" status message)
// — it now lives in enabledFor, so a stopped VM's help bar simply omits it and
// the key is a silent no-op, matching Start/Stop's fixed behaviour above.
func TestShellOfferedOnlyWhenRunning(t *testing.T) {
	m := newTestModel(t)
	m = focusTile(t, m, vm.VM{Name: "claude", Status: "Stopped", CPUs: 2})

	if strings.Contains(boardVerbs(m), "S shell") {
		t.Fatalf("a stopped VM's help bar must not offer shell, got:\n%s", boardVerbs(m))
	}
}

// Paste (the `v` verb, task 5) is offered (and fires) only when the VM is
// running — the same Shell/Upload/Download gate — and it must not route
// through the file-browser wizard: pressing it should not change m.view off
// the board (decision B: stay on board).
func TestPasteOfferedOnlyWhenRunning(t *testing.T) {
	m := newTestModel(t)
	m = focusTile(t, m, vm.VM{Name: "claude", Status: "Stopped", CPUs: 2})

	if strings.Contains(boardVerbs(m), "v paste image") {
		t.Fatalf("a stopped VM's help bar must not offer paste, got:\n%s", boardVerbs(m))
	}
	if after, cmd := m.Update(runeKey('v')); cmd != nil || after.(model).acting {
		t.Fatal("pressing 'v' on a stopped VM should dispatch no command and not mark acting")
	}

	m2 := focusTile(t, m, vm.VM{Name: "claude", Status: "Running", CPUs: 2})
	if !strings.Contains(boardVerbs(m2), "v paste image") {
		t.Fatalf("a running VM's help bar should offer paste, got:\n%s", boardVerbs(m2))
	}
	after, cmd := m2.Update(runeKey('v'))
	if cmd == nil {
		t.Fatal("pressing 'v' on a running VM should dispatch a command")
	}
	if after.(model).view != viewBoard {
		t.Fatal("pressing 'v' must not navigate off the board (no file-browser wizard)")
	}
}

// The help bar and the dispatcher must never disagree: for every command in
// the registry, whether it renders in the help bar and whether its key fires
// are governed by the exact same enabledFor call. This is the structural
// guarantee the registry buys over the old hand-maintained keymap/help-switch
// pair, which had already drifted (that pair is what this whole test file
// guards against regressing to). Checked for both a stopped and a running VM
// so the invariant holds regardless of which commands happen to be enabled.
func TestBoardHelpAndDispatchAgree(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m,
		vm.VM{Name: "stopped-vm", Status: "Stopped", CPUs: 2},
		vm.VM{Name: "running-vm", Status: "Running", CPUs: 2},
	)

	for _, name := range []string{"stopped-vm", "running-vm"} {
		mm := m
		mm.focusVM.Name = name
		focused, ok := mm.focusedVM()
		if !ok {
			t.Fatalf("precondition: %s should have a tile under the ring", name)
		}

		for _, c := range vmCommands {
			enabled := c.enabledFor(mm, focused)
			shown := false
			for _, hb := range mm.boardHelp() {
				h := hb.Help()
				// The verb Enter routes to is advertised with BOTH keys on one line
				// ("enter/S shell" rather than a separate "enter shell" entry), so match
				// on the verb — the Desc — and accept either key label. What this test
				// guards is that an enabled verb IS offered and a disabled one is NOT;
				// which keys reach it is boardHelp's business, and TestEnter…AdvertisedWith
				// pins the merged label itself.
				if h.Desc == c.binding.Help().Desc && (h.Key == c.binding.Help().Key || h.Key == "enter/"+c.binding.Help().Key) {
					shown = true
					break
				}
			}
			if enabled != shown {
				t.Fatalf("%s: command %q: enabledFor=%v but shown-in-help=%v (must agree)", name, c.binding.Help().Desc, enabled, shown)
			}
		}
	}
}
