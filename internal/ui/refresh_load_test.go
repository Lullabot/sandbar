package ui

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/pve"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
)

// Leaving the board clears the member's arming flag but cannot cancel the
// tea.Tick already scheduled. Coming back before it fires arms a second one.
// Both used to re-arm themselves on firing, so every quick trip to help, the
// log or a form added a permanent extra refresh loop — the request rate grew
// with how much the user had navigated, every response a 200. Only the tick
// armed LAST may drive the loop.
func TestStaleRefreshTicksDoNotStackLoopsAcrossBoardRoundTrips(t *testing.T) {
	isolateHostState(t)
	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	sc := m.members[0].scope

	var stale []uint64
	for range 20 {
		if cmd := m.tickRefresh(); cmd == nil {
			t.Fatal("precondition: tickRefresh should arm on the board")
		}
		stale = append(stale, m.members[0].tickGen)
		m.view = viewHelp // a quick look at help…
		_ = m.tickRefresh()
		m.view = viewBoard // …and straight back, before the tick fires
	}
	if cmd := m.tickRefresh(); cmd == nil {
		t.Fatal("tickRefresh should arm the current loop")
	}
	current := m.members[0].tickGen

	// Every tick left behind fires now. None may refresh or re-arm.
	for _, gen := range stale {
		if gen == current {
			t.Fatal("a re-armed loop must get a fresh generation")
		}
		_, cmd := m.dispatch(refreshTickMsg{scope: sc, gen: gen})
		if cmd != nil {
			t.Fatalf("stale tick (gen %d) started a refresh; only the current loop may", gen)
		}
		if !m.members[0].arming {
			t.Fatal("a stale tick must leave the current loop's arming flag alone")
		}
		if again := m.tickRefresh(); again != nil {
			t.Fatal("a stale tick must not lead to a second loop being armed")
		}
	}

	// The current one does its job.
	if _, cmd := m.dispatch(refreshTickMsg{scope: sc, gen: current}); cmd == nil {
		t.Fatal("the current loop's tick must refresh")
	}
}

// However many ticks and actions ask for a refresh, a member never has more
// than one in flight: ticks that find one running skip their turn, and an
// action's refresh waits and runs right after it, exactly once.
func TestOnlyOneRefreshIsEverInFlightPerMember(t *testing.T) {
	isolateHostState(t)
	var calls, inFlight, peak atomic.Int32
	release := make(chan struct{})
	fake := &providerfake.Provider{ListFunc: func() ([]vm.VM, error) {
		calls.Add(1)
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		return nil, nil
	}}
	m := New(singleFleet(fake, registry.LocalScope)).(model)
	sc := m.members[0].scope

	results := make(chan tea.Msg, 64)
	run := func(cmd tea.Cmd) {
		if cmd != nil {
			go func() { results <- cmd() }()
		}
	}

	// A slow backend: thirty ticks and five actions arrive while the first
	// refresh is still listing.
	for range 30 {
		_, cmd := m.dispatch(refreshTickMsg{scope: sc})
		run(cmd)
	}
	for range 5 {
		run(m.refreshMemberCmd(sc))
	}
	time.Sleep(50 * time.Millisecond) // let any wrongly-started refresh reach List
	if got := calls.Load(); got != 1 {
		t.Fatalf("%d refreshes reached the provider while one was in flight, want 1", got)
	}
	if !m.members[0].refreshQueued {
		t.Fatal("an action's refresh during an in-flight one must be queued, not dropped")
	}

	// The in-flight refresh lands (through Update, as in the program); the
	// ONE queued follow-up starts.
	close(release)
	first := (<-results).(vmsLoadedMsg)
	if first.seq == 0 {
		t.Fatal("a requested refresh must carry its sequence number")
	}
	follow := m.refreshLanded(sc, first.seq)
	if follow == nil {
		t.Fatal("the queued action refresh must start when the in-flight one lands")
	}
	run(follow)
	second := (<-results).(vmsLoadedMsg)

	next, _ := m.Update(second)
	nm := next.(model)
	if nm.members[0].refreshing {
		t.Fatal("Update must close the in-flight slot when the refresh lands")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("provider listed %d times, want 2 (the in-flight one, then the queued one)", got)
	}
	if got := peak.Load(); got != 1 {
		t.Fatalf("peak concurrent refreshes = %d, want 1", got)
	}
}

// A refresh that never reports back must not freeze the member's board
// forever: past refreshStuckAfter it is superseded, and its late result, if
// it ever comes, changes nothing.
func TestAWedgedRefreshIsSupersededAndItsLateResultIgnored(t *testing.T) {
	isolateHostState(t)
	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	sc := m.members[0].scope

	if cmd := m.requestRefresh(0, false, false); cmd == nil {
		t.Fatal("first refresh should start")
	}
	wedged := m.members[0].refreshSeq
	if cmd := m.requestRefresh(0, false, false); cmd != nil {
		t.Fatal("a second refresh must not start while the first is in flight")
	}

	m.members[0].refreshSince = time.Now().Add(-refreshStuckAfter - time.Second)
	if cmd := m.requestRefresh(0, false, false); cmd == nil {
		t.Fatal("a refresh past refreshStuckAfter must be superseded")
	}
	if cmd := m.refreshLanded(sc, wedged); cmd != nil || !m.members[0].refreshing {
		t.Fatal("the superseded refresh's late result must not close the new one's slot")
	}
}

// A member whose API is not answering reads as degraded and retrying — the
// client is already backing off — not as a configuration error.
func TestUnavailableBackendShowsAsDegraded(t *testing.T) {
	isolateHostState(t)
	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	mem := m.members[0]
	mem.state = connErrored

	for _, err := range []error{
		fmt.Errorf("proxmox: listing pool: %w", &pve.APIError{Status: 502, Reason: "Bad Gateway"}),
		fmt.Errorf("proxmox: listing pool: %w", &pve.UnavailableError{Host: "silo:8006", RetryAfter: 12 * time.Second}),
	} {
		mem.lastErr = err
		line, _ := m.memberStatusLine(mem)
		if !strings.Contains(line, "degraded") {
			t.Errorf("status for %v = %q, want it marked degraded", err, line)
		}
	}

	mem.lastErr = errors.New("proxmox: 403 Forbidden")
	if line, _ := m.memberStatusLine(mem); strings.Contains(line, "degraded") || !strings.Contains(line, "error") {
		t.Errorf("a permission failure is an error, not degradation: %q", line)
	}
}
