package ui

import "testing"

// Fleet refresh arms one timer per member and stops when leaving the board.
func TestTickRefreshStartsOnceAndStopsWhenNotOnBoard(t *testing.T) {
	m := newTestModel(t)
	if !m.shouldTick() {
		t.Fatal("precondition: a fresh, focused, just-used model on the board should tick")
	}

	cmd := m.tickRefresh()
	if cmd == nil {
		t.Fatal("tickRefresh should start the loop while shouldTick holds")
	}
	if !m.members[0].arming {
		t.Fatal("tickRefresh should mark the member's loop in flight")
	}

	// A second call while one is already running must not stack a duplicate
	// loop for that member.
	if again := m.tickRefresh(); again != nil {
		t.Fatal("a second tickRefresh while the member is already arming must not start a duplicate loop")
	}

	// The board is no longer the active screen: the gate closes. tickRefresh
	// must decline to re-arm AND clear the per-member arming flag, so coming back
	// to the board can start a fresh loop rather than being stuck behind a stale
	// "already arming" flag.
	m.view = viewForm
	if cmd := m.tickRefresh(); cmd != nil {
		t.Fatal("tickRefresh must not re-arm once the board is not the active screen")
	}
	if m.members[0].arming {
		t.Fatal("tickRefresh should clear the arming flag once the gate closes")
	}
}

// A tick that fires while shouldTick no longer holds must not re-list — the
// gate is honoured by the loop's own iteration, not merely by tickRefresh's
// re-arm decision (an in-flight tea.Tick started while the gate was open can
// still land after it has closed).
func TestRefreshTickMsgHonoursTheGateWhenNotOnBoard(t *testing.T) {
	m := newTestModel(t)
	m.view = viewForm // off the board: the gate is closed

	_, cmd := m.dispatch(refreshTickMsg{})
	if cmd != nil {
		t.Fatal("a refresh tick off the board must dispatch no command")
	}
}

// And while the gate IS open, the tick actually re-lists the fleet — the
// whole point of the feature: without this, the board is a snapshot from
// whenever it was last touched, and every claim about it being "live" is
// false.
func TestRefreshTickMsgRelistsWhileOnBoard(t *testing.T) {
	m := newTestModel(t)

	_, cmd := m.dispatch(refreshTickMsg{})
	if cmd == nil {
		t.Fatal("a refresh tick on the board should dispatch a re-list")
	}
	msg := cmd()
	if _, ok := msg.(vmsLoadedMsg); !ok {
		t.Fatalf("the refresh tick's command produced %T, want vmsLoadedMsg (listCmd)", msg)
	}
}
