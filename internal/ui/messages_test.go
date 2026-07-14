package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"

	"github.com/charmbracelet/x/ansi"
)

// THE RING IS BOUNDED: session-only, capped at maxMessages, so a long-lived
// session cannot grow it without limit — the memory-leak twin of the
// invisibility this plan otherwise exists to remove. The OLDEST entries are
// what get dropped as the ring fills, never the newest.
func TestMessageRingIsBounded(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < maxMessages+10; i++ {
		m.logMsg(fmt.Sprintf("message %d", i))
	}

	if got := len(m.messages); got != maxMessages {
		t.Fatalf("len(m.messages) = %d, want the ring capped at %d", got, maxMessages)
	}

	wantNewest := fmt.Sprintf("message %d", maxMessages+10-1)
	if got := m.lastMessage(); got != wantNewest {
		t.Fatalf("lastMessage() = %q, want the newest message %q to survive", got, wantNewest)
	}

	wantOldestSurvivor := fmt.Sprintf("message %d", 10)
	if got := m.messages[0].text; got != wantOldestSurvivor {
		t.Fatalf("m.messages[0].text = %q, want %q — the oldest entries must be the ones dropped, not the newest", got, wantOldestSurvivor)
	}
}

// logMsg("") is a deliberate no-op: several call sites (a shell returning
// cleanly, a browse opening) have nothing to report, and used to clear the
// old status field to say so. A log has no "current value" to clear —
// saying nothing must not append a blank entry.
func TestLogMsgEmptyTextIsANoOp(t *testing.T) {
	m := newTestModel(t)
	m.logMsg("something happened")
	m.logMsg("")

	if got := len(m.messages); got != 1 {
		t.Fatalf("len(m.messages) = %d after logging an empty string, want 1 (a no-op)", got)
	}
	if got := m.lastMessage(); got != "something happened" {
		t.Fatalf("lastMessage() = %q, want the empty logMsg call to leave it untouched", got)
	}
}

// recentMessages returns oldest-first, capped at n, and never invents entries
// that were never logged.
func TestRecentMessagesOrderAndCap(t *testing.T) {
	m := newTestModel(t)
	m.logMsg("a")
	m.logMsg("b")
	m.logMsg("c")

	got := m.recentMessages(2)
	if len(got) != 2 || got[0].text != "b" || got[1].text != "c" {
		t.Fatalf("recentMessages(2) = %+v, want [b c] (oldest first, capped)", got)
	}
	if got := m.recentMessages(10); len(got) != 3 {
		t.Fatalf("recentMessages(10) with only 3 logged = %d entries, want 3", len(got))
	}
	if got := m.recentMessages(0); got != nil {
		t.Fatalf("recentMessages(0) = %v, want nil", got)
	}
}

// A logged message must appear ONCE on the board, not twice. The docked strip
// (above the grid) and the activity line (below it) were both rendering the most
// recent message, so every message printed twice on any terminal tall enough to
// show the strip — which reads as a rendering bug, not as two panes doing
// different jobs. The strip owns message history; the activity line keeps only
// what must interrupt (a confirm, the spinner).
func TestALoggedMessageIsNotPrintedTwice(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40) // tall enough for the strip (see messagesMinHeight)
	m = loadManaged(t, m, vm.VM{Name: "db", Status: "Running"})
	if m.layout.MessagesHeight < 1 {
		t.Fatal("precondition: this terminal must be tall enough to show the messages strip")
	}

	m.logMsg("stopping db…")

	view := ansi.Strip(m.boardView())
	if got := strings.Count(view, "stopping db…"); got != 1 {
		t.Errorf("the message rendered %d times, want exactly 1 (the strip shows it; the activity line must not repeat it):\n%s", got, view)
	}
}

// …but on a terminal too SHORT for the strip, the activity line is the only
// place a message can go, so it must still show it. Shedding the strip must not
// mean shedding the message with it.
func TestAMessageStillShowsWhenTheStripIsShed(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 24) // below messagesMinHeight: no strip
	m = loadManaged(t, m, vm.VM{Name: "db", Status: "Running"})
	if m.layout.MessagesHeight >= 1 {
		t.Fatal("precondition: this terminal must be too short for the messages strip")
	}

	m.logMsg("stopping db…")

	view := ansi.Strip(m.boardView())
	if got := strings.Count(view, "stopping db…"); got != 1 {
		t.Errorf("the message rendered %d times, want exactly 1 (with no strip, the activity line must carry it):\n%s", got, view)
	}
}

// The strip is a titled box, and the title sits IN its top edge.
func TestMessagesStripIsFramedAndTitled(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)
	m = loadManaged(t, m, vm.VM{Name: "db", Status: "Running"})
	if m.layout.MessagesHeight < 1 {
		t.Fatal("precondition: the terminal must be tall enough to show the strip")
	}

	m.logMsg("stopping db…")
	strip := ansi.Strip(m.messagesStripView())

	if !strings.Contains(strip, "╭─ Messages ") {
		t.Errorf("the strip must be a frame titled Messages, got:\n%s", strip)
	}
	if !strings.Contains(strip, "stopping db…") {
		t.Errorf("the frame must hold the messages, got:\n%s", strip)
	}
	// Exactly MessagesHeight rows, frame included — the grid below must not shift
	// as the log fills in.
	if got := len(strings.Split(strip, "\n")); got != m.layout.MessagesHeight {
		t.Errorf("the strip rendered %d rows, want exactly MessagesHeight=%d", got, m.layout.MessagesHeight)
	}
}

// THE STRIP MAY NOT COST A TILE ROW — the invariant messagesMinHeight exists for,
// and the one the frame could quietly have broken by costing two rows more than
// the strip it replaced. At the shortest terminal that shows the strip, the grid
// must still afford TWO rows of tiles.
func TestTheFramedStripStillNeverCostsATileRow(t *testing.T) {
	lm := classify(120, messagesMinHeight)
	if lm.MessagesHeight < 1 {
		t.Fatalf("precondition: the strip must be shown at messagesMinHeight=%d", messagesMinHeight)
	}
	if lm.GridHeight < 2*tileHeight {
		t.Errorf("at %d rows the strip leaves GridHeight=%d, which is less than the %d two tile rows need — the strip has taken a tile row",
			messagesMinHeight, lm.GridHeight, 2*tileHeight)
	}
	// And one row shorter, the strip is shed rather than the tile row.
	if short := classify(120, messagesMinHeight-1); short.MessagesHeight != 0 {
		t.Errorf("at %d rows the strip must be shed, got MessagesHeight=%d", messagesMinHeight-1, short.MessagesHeight)
	}
}

// The box asks for ten lines of history but must never buy them with a tile row.
// It takes what is spare: ten on a terminal tall enough, fewer as the terminal
// shrinks, and none at all below the floor — while two rows of tiles survive at
// EVERY height. A fixed ten-line box would instead have been shed outright on
// every terminal under ~36 rows, which is most of them.
func TestMessagesBoxTakesOnlySpareRows(t *testing.T) {
	for h := messagesMinHeight - 4; h <= 60; h++ {
		lm := classify(120, h)

		if lm.GridHeight < 2*tileHeight {
			t.Fatalf("h=%d: grid=%d, less than the %d two tile rows need — the box has taken a tile row",
				h, lm.GridHeight, 2*tileHeight)
		}
		if lm.MessagesHeight == 0 {
			continue // shed: legal at any height, and the only option below the floor
		}
		lines := lm.MessagesHeight - messagesStripChrome
		if lines < messagesStripMinLines || lines > messagesStripMaxLines {
			t.Fatalf("h=%d: box shows %d lines, want between %d and %d (or be shed)",
				h, lines, messagesStripMinLines, messagesStripMaxLines)
		}
	}

	// Tall enough: the full ten.
	if lines := classify(120, 40).MessagesHeight - messagesStripChrome; lines != messagesStripMaxLines {
		t.Errorf("at 40 rows the box shows %d lines, want the full %d", lines, messagesStripMaxLines)
	}
	// At the floor: the smallest box worth drawing, not a sliver.
	if lines := classify(120, messagesMinHeight).MessagesHeight - messagesStripChrome; lines != messagesStripMinLines {
		t.Errorf("at the floor (%d rows) the box shows %d lines, want %d", messagesMinHeight, lines, messagesStripMinLines)
	}
	// Below it: shed, rather than squeezed.
	if got := classify(120, messagesMinHeight-1).MessagesHeight; got != 0 {
		t.Errorf("below the floor the box must be shed, got MessagesHeight=%d", got)
	}
}
