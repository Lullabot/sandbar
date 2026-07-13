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
		{Name: "claude-base", Status: "Stopped"}, // no tile: the board is managed-clones-only
	}})
	m = loaded.(model)
	seedSample(&m, "web", guestSample{CPUPct: 25, HasCPU: true, MemUsed: 2 << 30, MemTotal: 8 << 30})
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
	// The header's live host readout survives stripping in full: it is plain text,
	// never a colour swatch. Same for the tile's gauges — a bar whose fill was only
	// a colour would vanish here, and the number beside it is what carries the value.
	if !strings.Contains(stripped, "cpu") || !strings.Contains(stripped, "25%") {
		t.Fatalf("the live readout and the tile's gauge must survive colour stripping, got:\n%s", stripped)
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

// The per-VM verbs get the same check. They are the ones the old hand-maintained
// help switch used to get wrong (advertising a verb that did nothing), and the fix
// must not be a colour-only fix: eligibility has to be legible with every escape
// code stripped. They render on the BOARD's footer now — the VM screen that used
// to carry them is gone.
func TestBoardFooterVerbsAreMonochromeSafe(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 200, 40) // wide enough that the footer elides nothing
	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: "Stopped", CPUs: 2})

	colored := m.boardView()
	stripped := ansi.Strip(colored)

	if colored == stripped {
		t.Fatal("precondition: the unstripped board should carry ANSI colour codes")
	}
	if !strings.Contains(stripped, "s start") {
		t.Fatalf("a stopped VM's footer must offer start in plain text, got:\n%s", stripped)
	}
	if strings.Contains(stripped, "x stop") {
		t.Fatalf("a stopped VM's footer must not offer stop even in plain text, got:\n%s", stripped)
	}
}
