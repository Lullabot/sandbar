package ui

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"

	"github.com/charmbracelet/x/ansi"
)

// THE HIDDEN COUNT IS THE ENTIRE MITIGATION for task 08 filtering the board to
// managed clones only: a base image (heavy, and now unmanageable from the TUI)
// and an unrelated Lima VM both get no tile and no toggle to bring them back.
// Tested against a fleet holding all three kinds at once — a managed clone, a
// base image, and an unrelated VM — because that is exactly the case a header
// that quietly drops the count under space pressure would get wrong.
func TestHiddenCountAgainstAMixedFleet(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)
	// "web" clones "claude-base": this both manages web AND is what makes
	// IsBase("claude-base") true (see registry.Registry.IsBase).
	if err := m.reg.Add(vm.CreateConfig{Name: "web", BaseName: "claude-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "web", Status: "Running"},              // managed clone: gets a tile
		{Name: "claude-base", Status: "Stopped"},      // base image: hidden
		{Name: "someone-elses-vm", Status: "Running"}, // unrelated VM: hidden
	}})
	m = loaded.(model)

	base, external := m.hiddenCounts()
	if base != 1 || external != 1 {
		t.Fatalf("hiddenCounts() = (base=%d, external=%d), want (1, 1)", base, external)
	}

	// The header's own honesty clause, in the plan's own wording.
	counts := m.headerCounts(m.layout.ContentWidth)
	if !strings.Contains(counts, "1 base") || !strings.Contains(counts, "1 external") || !strings.Contains(counts, "hidden") {
		t.Fatalf("headerCounts() = %q, want it to name 1 hidden base image and 1 hidden external VM", counts)
	}

	// And it actually reaches the rendered board, not just the string builder.
	view := ansi.Strip(m.boardView())
	if !strings.Contains(view, "1 base") || !strings.Contains(view, "1 external") {
		t.Fatalf("the rendered board must show the hidden count, got:\n%s", view)
	}
	// Neither hidden VM gets a tile — the board is still managed-clones-only.
	if strings.Contains(view, "claude-base") || strings.Contains(view, "someone-elses-vm") {
		t.Fatalf("hidden VMs must get no tile, got:\n%s", view)
	}
}

// A fleet with nothing hidden must not grow a permanent "0 hidden" clause —
// the header only speaks up when it has something to disclose.
func TestHiddenCountIsSilentWhenNothingIsHidden(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m, vm.VM{Name: "web", Status: "Running"})

	if base, external := m.hiddenCounts(); base != 0 || external != 0 {
		t.Fatalf("hiddenCounts() = (%d, %d), want (0, 0) with nothing hidden", base, external)
	}
	if counts := m.headerCounts(m.layout.ContentWidth); strings.Contains(counts, "hidden") {
		t.Fatalf("headerCounts() = %q, must not claim anything is hidden when nothing is", counts)
	}
}

// AT 80x24 — the plan's own required golden size, and the narrowest realistic
// terminal — the hidden count must still be present. Task 08 sheds the
// messages strip first (layout.go); the header itself never disappears, and
// hostCapacityText (the least essential clause) is the one that gives way if
// anything must, never the hidden count — see headerCounts.
func TestHiddenCountSurvivesAt80x24(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 80, 24)
	if err := m.reg.Add(vm.CreateConfig{Name: "web", BaseName: "claude-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "web", Status: "Running"},
		{Name: "claude-base", Status: "Stopped"},
		{Name: "someone-elses-vm", Status: "Running"},
	}})
	m = loaded.(model)

	view := ansi.Strip(m.boardView())
	if !strings.Contains(view, "1 base") || !strings.Contains(view, "1 external") {
		t.Fatalf("80x24 must still show the hidden count, got:\n%s", view)
	}
}
