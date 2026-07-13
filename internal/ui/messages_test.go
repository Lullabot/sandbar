package ui

import (
	"fmt"
	"testing"
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
