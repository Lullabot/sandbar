package ui

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"

	"github.com/charmbracelet/x/ansi"
)

// NO_COLOR / a monochrome terminal must not lose any status: every surface
// this task added — the header (with the hidden count), the messages strip,
// the footer — must stay distinguishable by GLYPH AND TEXT LABEL alone,
// colour or not. The ansi.Stripped goldens (teatest_test.go) are partial
// evidence of this already, since they render legibly with every ANSI code
// removed; this test makes the property explicit rather than leaving it
// implicit in two golden files.
func TestBoardSurfacesAreMonochromeSafe(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)
	if err := m.reg.Add(vm.CreateConfig{Name: "web", BaseName: "claude-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "web", Status: "Running"},
		{Name: "claude-base", Status: "Stopped"}, // hidden: exercises the header's hidden count
	}})
	m = loaded.(model)
	m.logMsg("stopping web…") // exercises the messages strip's TEXT, not just its colour

	colored := m.boardView()
	stripped := ansi.Strip(colored)

	// Precondition: the unstripped view actually carries ANSI styling, or
	// stripping it proves nothing.
	if colored == stripped {
		t.Fatal("precondition: the unstripped board view should carry ANSI colour codes, or this test proves nothing")
	}
	if stripped == "" {
		t.Fatal("the board must render SOMETHING with colour stripped — colour can never be the only carrier of a status")
	}
	// The header's hidden count — the plan's one honesty requirement for this
	// task — survives stripping in full: it is plain text, never a colour swatch.
	if !strings.Contains(stripped, "1 base") || !strings.Contains(stripped, "hidden") {
		t.Fatalf("the hidden count must survive with colour stripped, got:\n%s", stripped)
	}
	// The messages strip's logged text survives stripping too.
	if !strings.Contains(stripped, "stopping web") {
		t.Fatalf("the messages strip's text must survive with colour stripped, got:\n%s", stripped)
	}
	// And the footer's verbs (key + label, e.g. "x stop") are plain text.
	if !strings.Contains(stripped, "x stop") {
		t.Fatalf("the footer's verbs must survive with colour stripped, got:\n%s", stripped)
	}
}

// The VM screen's footer gets the same check: its verbs are the ones the old
// hand-maintained help switch used to get wrong (advertising a verb that did
// nothing), and the fix must not be a colour-only fix.
func TestDetailFooterIsMonochromeSafe(t *testing.T) {
	m := newTestModel(t)
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{{Name: "claude", Status: "Stopped", CPUs: 2}}})
	m = loaded.(model)
	m.view = viewDetail
	m.detail, _ = m.lookupVM("claude")

	colored := m.detailView()
	stripped := ansi.Strip(colored)

	if colored == stripped {
		t.Fatal("precondition: the unstripped detail view should carry ANSI colour codes")
	}
	if !strings.Contains(stripped, "s start") {
		t.Fatalf("a stopped VM's footer must offer start in plain text, got:\n%s", stripped)
	}
	if strings.Contains(stripped, "x stop") {
		t.Fatalf("a stopped VM's footer must not offer stop even in plain text, got:\n%s", stripped)
	}
}
