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

// logWarn marks its entry with the warn flag — distinct from a plain logMsg
// entry — which is what the render sites below key their "⚠ " + amber
// treatment on. A warn entry is otherwise an ordinary message: it counts
// toward the ring, appears in recentMessages/lastMessage, and is bounded by
// maxMessages exactly like any other.
func TestLogWarnSetsTheWarnFlagLogMsgDoesNot(t *testing.T) {
	m := newTestModel(t)
	m.logMsg("connected to ci-host")
	m.logWarn("disk low")

	if m.messages[0].warn {
		t.Fatalf("a plain logMsg entry must not carry the warn flag: %+v", m.messages[0])
	}
	if !m.messages[1].warn {
		t.Fatalf("a logWarn entry must carry the warn flag: %+v", m.messages[1])
	}
	if got := m.lastMessage(); got != "disk low" {
		t.Fatalf("lastMessage() = %q, want the logWarn text like any other logged line", got)
	}
}

// logWarn("") must be the same no-op logMsg("") already is: nothing to report
// must never append a blank warning entry.
func TestLogWarnEmptyTextIsANoOp(t *testing.T) {
	m := newTestModel(t)
	m.logWarn("")
	if len(m.messages) != 0 {
		t.Fatalf("logWarn(\"\") appended %d entries, want a no-op", len(m.messages))
	}
}

// The docked Messages strip renders a warn entry with the ⚠ marker and in
// warnStyle (ANSI 214, the repo's one amber) — while a PLAIN entry (like
// "connected to ci-host", which is not a warning) renders exactly as it
// always has: no marker, no colour change. This is the exact scenario the
// header-band golden fixtures pin ("connected to ci-host" lines), so a plain
// entry must never pick up the warn treatment.
func TestMessagesStripRendersWarnEntriesWithMarkerAndAmber(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)
	m = loadManaged(t, m, vm.VM{Name: "db", Status: "Running"})
	if m.layout.MessagesHeight < 1 {
		t.Fatal("precondition: this terminal must be tall enough for the strip")
	}

	m.logMsg("connected to ci-host")
	m.logWarn("silo disk low — 4.7 GiB free of 48 GiB (<10%)")

	strip := m.messagesStripView() // NOT ansi-stripped: the marker/colour IS what's under test
	if !strings.Contains(strip, "⚠ silo disk low") {
		t.Fatalf("a warn entry must render with the ⚠ marker, got:\n%s", strip)
	}
	if !strings.Contains(strip, "214") {
		t.Fatalf("a warn entry must render in warnStyle (ANSI 214), got:\n%s", strip)
	}
	if strings.Contains(ansi.Strip(strip), "⚠ connected to ci-host") {
		t.Fatalf("a plain entry must never get the warn marker, got:\n%s", strip)
	}

	// Isolate the plain line's own rendering (a fresh model logging ONLY the
	// plain entry) to confirm it carries no warn colour at all — the strip
	// above legitimately contains "214" from the OTHER (warn) line, so that
	// check alone cannot tell the two apart.
	plainOnly := newTestModel(t)
	plainOnly = resized(plainOnly, 120, 40)
	plainOnly = loadManaged(t, plainOnly, vm.VM{Name: "db", Status: "Running"})
	plainOnly.logMsg("connected to ci-host")
	plainStrip := plainOnly.messagesStripView()
	if strings.Contains(plainStrip, "214") {
		t.Fatalf("a plain entry alone must render with no warn colour, got:\n%s", plainStrip)
	}
	if !strings.Contains(ansi.Strip(plainStrip), "connected to ci-host") {
		t.Fatalf("the plain entry text must still render, got:\n%s", plainStrip)
	}
}

// The single-line activity view (shown on a terminal too short for the
// docked strip) applies the same ⚠ + amber treatment to a warn entry, and
// renders a plain entry exactly as before.
func TestActivityLineRendersWarnEntryWithMarkerAndAmber(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 24) // below messagesMinHeight: no strip, so activityLineView carries it
	m = loadManaged(t, m, vm.VM{Name: "db", Status: "Running"})
	if m.layout.MessagesHeight >= 1 {
		t.Fatal("precondition: this terminal must be too short for the strip")
	}

	m.logWarn("silo disk low — 4.7 GiB free of 48 GiB (<10%)")
	line := m.activityLineView()
	if !strings.Contains(line, "⚠ silo disk low") {
		t.Fatalf("activityLineView() = %q, want the ⚠ marker on a warn entry", line)
	}
	if !strings.Contains(line, "214") {
		t.Fatalf("activityLineView() = %q, want warnStyle (ANSI 214) on a warn entry", line)
	}
}

// A plain entry on the activity line renders exactly as it always has: no
// marker, no amber.
func TestActivityLineRendersPlainEntryUnchanged(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 24)
	m = loadManaged(t, m, vm.VM{Name: "db", Status: "Running"})
	if m.layout.MessagesHeight >= 1 {
		t.Fatal("precondition: this terminal must be too short for the strip")
	}

	m.logMsg("connected to ci-host")
	line := m.activityLineView()
	if strings.Contains(ansi.Strip(line), "⚠") {
		t.Fatalf("activityLineView() = %q, want no warn marker on a plain entry", line)
	}
	if strings.Contains(line, "214") {
		t.Fatalf("activityLineView() = %q, want no warnStyle colour on a plain entry", line)
	}
	if !strings.Contains(ansi.Strip(line), "connected to ci-host") {
		t.Fatalf("activityLineView() = %q, want the plain message text", line)
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

// The box must line up with the TILES, not with the terminal. TileWidth is an
// integer division of the space the columns share, so the remainder (up to
// Columns-1 cells) is left over — and a box drawn to ContentWidth overhangs the
// grid by exactly that much. At 160 and 200 columns that is a visible two cells,
// which is what this pins.
func TestMessagesBoxLinesUpWithTheTiles(t *testing.T) {
	widest := func(s string) int {
		w := 0
		for _, line := range strings.Split(ansi.Strip(s), "\n") {
			if n := ansi.StringWidth(strings.TrimRight(line, " ")); n > w {
				w = n
			}
		}
		return w
	}

	for _, w := range []int{80, 100, 120, 137, 160, 200} {
		m := newTestModel(t)
		m = resized(m, w, 40)
		// Enough VMs to fill a full row at every width under test, so the widest
		// rendered tile row IS the grid's full width.
		m = loadManaged(t, m,
			vm.VM{Name: "api", Status: "Running"}, vm.VM{Name: "db", Status: "Running"},
			vm.VM{Name: "web", Status: "Running"}, vm.VM{Name: "x1", Status: "Running"},
		)
		if m.layout.MessagesHeight < 1 {
			t.Fatalf("w=%d: precondition: the box must be shown", w)
		}

		gridW, boxW := widest(m.gridView()), widest(m.messagesStripView())
		if gridW != boxW {
			t.Errorf("w=%d: the tiles are %d wide but the Messages box is %d — they must line up", w, gridW, boxW)
		}
	}
}
