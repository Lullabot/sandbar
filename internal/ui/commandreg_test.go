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
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Stopped", CPUs: 2},
	}})
	m = loaded.(model)
	m.view = viewDetail
	m.detail, _ = m.lookupVM("claude")

	rendered := plainHelp(m.detailView())
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
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Running", CPUs: 2},
	}})
	m = loaded.(model)
	m.view = viewDetail
	m.detail, _ = m.lookupVM("claude")

	rendered := plainHelp(m.detailView())
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
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "running-vm", Status: "Running", CPUs: 2},
		{Name: "stopped-vm", Status: "Stopped", CPUs: 2},
	}})
	m = loaded.(model)

	running := m
	running.view = viewDetail
	running.detail, _ = running.lookupVM("running-vm")
	if strings.Contains(plainHelp(running.detailView()), "s start") {
		t.Fatalf("a running VM's help bar must not offer start, got:\n%s", running.detailView())
	}
	if after, cmd := running.Update(runeKey('s')); cmd != nil || after.(model).acting {
		t.Fatal("pressing 's' on a running VM should dispatch no command and not mark acting")
	}

	stopped := m
	stopped.view = viewDetail
	stopped.detail, _ = stopped.lookupVM("stopped-vm")
	if !strings.Contains(plainHelp(stopped.detailView()), "s start") {
		t.Fatalf("a stopped VM's help bar should offer start, got:\n%s", stopped.detailView())
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
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Stopped", CPUs: 2},
	}})
	m = loaded.(model)
	m.view = viewDetail
	m.detail, _ = m.lookupVM("claude")

	if strings.Contains(plainHelp(m.detailView()), "S shell") {
		t.Fatalf("a stopped VM's help bar must not offer shell, got:\n%s", m.detailView())
	}
}

// The help bar and the dispatcher must never disagree: for every command in
// the registry, whether it renders in the help bar and whether its key fires
// are governed by the exact same enabledFor call. This is the structural
// guarantee the registry buys over the old hand-maintained keymap/help-switch
// pair, which had already drifted (that pair is what this whole test file
// guards against regressing to). Checked for both a stopped and a running VM
// so the invariant holds regardless of which commands happen to be enabled.
func TestDetailHelpAndDispatchAgree(t *testing.T) {
	m := newTestModel(t)
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "stopped-vm", Status: "Stopped", CPUs: 2},
		{Name: "running-vm", Status: "Running", CPUs: 2},
	}})
	m = loaded.(model)

	for _, name := range []string{"stopped-vm", "running-vm"} {
		mm := m
		mm.view = viewDetail
		mm.detail, _ = mm.lookupVM(name)

		for _, c := range detailCommands {
			enabled := c.enabledFor(mm, mm.detail)
			shown := false
			for _, hb := range mm.detailHelp() {
				if hb.Help() == c.binding.Help() {
					shown = true
					break
				}
			}
			if enabled != shown {
				t.Fatalf("%s: command %q: enabledFor=%v but shown-in-help=%v (must agree)", name, c.help, enabled, shown)
			}
		}
	}
}
