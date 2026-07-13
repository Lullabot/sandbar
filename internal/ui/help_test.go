package ui

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// EVERY VERB IS ON THE ? SCREEN, and it cannot be otherwise: the screen is
// generated from the same registry the footer and the dispatcher walk. A verb added
// without a sentence explaining it fails here rather than shipping undocumented.
func TestHelpScreenDescribesEveryVerb(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 100, 40)
	m = putOnBoard(t, m, vm.VM{Name: "web", Status: "Running", CPUs: 2})
	m.openHelp()

	view := ansi.Strip(m.helpView())
	for _, c := range vmCommands {
		if c.about == "" {
			t.Errorf("command %q has no sentence for the ? screen", c.binding.Help().Key)
			continue
		}
		if !strings.Contains(view, c.binding.Help().Key) {
			t.Errorf("the ? screen does not list %q", c.binding.Help().Key)
		}
	}
	for _, k := range m.boardKeys() {
		if !strings.Contains(view, k.keys) {
			t.Errorf("the ? screen does not list the board key %q", k.keys)
		}
	}
	// It is a REFERENCE, not a footer: a verb that does not apply to the focused VM
	// right now is still listed. `s start` does not apply to a running VM, and a user
	// asking "can sand start a VM" must not have to stop one first to find out.
	if !strings.Contains(boardVerbs(m), "x stop") {
		t.Fatal("precondition: a running VM offers stop, not start")
	}
	if !strings.Contains(view, "Boot the VM") {
		t.Fatal("the ? screen must describe start even when the focused VM is already running")
	}

	// EVERY SENTENCE APPEARS IN FULL, to its last word. The closing note took only
	// the first wrapped line, so it stopped mid-sentence — a silent truncation, with
	// no ellipsis to give it away, on the one screen whose whole job is to say things
	// in full. Checking the LAST words of the longest sentences is what catches a
	// dropped tail; checking that the text is "present" does not.
	// Against the CONTENT, not the rendered view: the view is a scrolling window and
	// the closing note is legitimately below the fold at most sizes.
	flat := strings.Join(strings.Fields(ansi.Strip(strings.Join(m.helpLines(), " "))), " ")
	for _, tail := range []string{
		"The key does nothing when it is not offered.",                // the closing note
		"form opens pre-filled so you can change the settings first.", // R, the longest verb
		"X still stops every managed VM.",                             // the / entry
	} {
		if !strings.Contains(flat, tail) {
			t.Errorf("a sentence was cut before its end — %q is missing", tail)
		}
	}
}

// ? opens the screen and closes it again, and esc closes it. It never leaves the
// user somewhere they cannot get out of.
func TestHelpScreenTogglesAndCloses(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 100, 40)
	m = putOnBoard(t, m, vm.VM{Name: "web", Status: "Running"})

	opened, _ := press(t, m, runeKey('?'))
	if opened.view != viewHelp {
		t.Fatalf("? should open the keys screen, got view %v", opened.view)
	}
	closed, _ := press(t, opened, runeKey('?'))
	if closed.view != viewBoard {
		t.Fatalf("? should close it again, got view %v", closed.view)
	}
	esc, _ := press(t, opened, tea.KeyPressMsg{Code: tea.KeyEsc})
	if esc.view != viewBoard {
		t.Fatalf("esc should close the keys screen, got view %v", esc.view)
	}
}

// The screen fits the terminal at every size, and scrolls when it cannot.
func TestHelpScreenFitsAndScrolls(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{80, 24}, {100, 30}, {60, 12}, {200, 50}} {
		m := newTestModel(t)
		m = resized(m, sz.w, sz.h)
		m = putOnBoard(t, m, vm.VM{Name: "web", Status: "Running"})
		m.openHelp()

		// No sentence may be clipped: the whole point of the screen is to say the thing
		// in full. titleStyle carries its own padding, so the wrap has to MEASURE the
		// key column rather than assume it — it did not, and every sentence with room
		// to spare ended in an ellipsis one cell early.
		for _, l := range m.helpLines() {
			if got := ansi.StringWidth(ansi.Strip(l)); got > m.layout.ContentWidth {
				t.Errorf("%dx%d: a help line is %d cells, content width is %d: %q",
					sz.w, sz.h, got, m.layout.ContentWidth, ansi.Strip(l))
			}
			if strings.Contains(ansi.Strip(l), "…") {
				t.Errorf("%dx%d: a sentence was clipped rather than wrapped: %q", sz.w, sz.h, ansi.Strip(l))
			}
		}

		lines := strings.Split(ansi.Strip(m.helpView()), "\n")
		if len(lines) > sz.h {
			t.Errorf("%dx%d: the keys screen rendered %d lines into %d rows", sz.w, sz.h, len(lines), sz.h)
		}
		for _, l := range lines {
			if got := ansi.StringWidth(strings.TrimRight(l, " ")); got > sz.w {
				t.Errorf("%dx%d: a line overflows (%d cells): %q", sz.w, sz.h, got, l)
			}
		}
		// Scrolling is clamped: paging past the end does not blank the screen.
		for i := 0; i < 50; i++ {
			next, _ := press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
			m = next
		}
		if body := ansi.Strip(m.helpView()); strings.TrimSpace(body) == "" {
			t.Errorf("%dx%d: scrolling to the end blanked the keys screen", sz.w, sz.h)
		}
	}
}
