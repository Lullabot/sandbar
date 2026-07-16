package ui

// fleet_test.go covers the async per-profile aggregation this task introduces:
// members list independently, a slow/unreachable member never blocks the board,
// and an errored member self-heals with per-member backoff. Everything here is
// driven with providerfake (func-field double) — NO real backend.

import (
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"
)

// remoteScope stands in for a second connection profile's target.
var remoteScope = registry.Scope{Provider: "lima-remote", RemoteTarget: "user@build-host:22"}

// twoMemberFleet builds a local + remote member fleet over the given fakes.
func twoMemberFleet(local, remote provider.Provider) provider.Fleet {
	localProf := profiles.Profile{ID: profiles.LocalProfileID, Name: "local", Type: profiles.TypeLocal, Enabled: true}
	remoteProf := profiles.Profile{ID: "remote", Name: "build-host", Type: profiles.TypeRemoteSSH, Enabled: true}
	return provider.Fleet{
		{Profile: localProf, Prov: local, Scope: registry.LocalScope},
		{Profile: remoteProf, Prov: remote, Scope: remoteScope},
	}
}

// seedManagedScoped records name as managed under scope in the on-disk registry
// New will load (XDG_DATA_HOME is already isolated).
func seedManagedScoped(t *testing.T, scope registry.Scope, name string) {
	t.Helper()
	reg, err := registry.Load()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	if err := reg.AddScoped(vm.CreateConfig{Name: name, BaseName: "sandbar-base"}, scope); err != nil {
		t.Fatalf("seed %s as managed under %v: %v", name, scope, err)
	}
}

// TestFleetAggregatesConnectedMembersWithoutWaiting proves the board is the UNION
// of members that HAVE reported, and never waits on one that hasn't: the remote
// member's tile renders (and the board stays interactive) with the local member
// still connecting — no message from it delivered at all.
func TestFleetAggregatesConnectedMembersWithoutWaiting(t *testing.T) {
	isolateHostState(t)
	seedManagedScoped(t, remoteScope, "remote-web")

	m := New(twoMemberFleet(&providerfake.Provider{}, &providerfake.Provider{})).(model)
	m = resized(m, 100, 30)

	// ONLY the remote member reports (the local member is still connecting — its
	// vmsLoadedMsg is deliberately never delivered).
	next, _ := m.Update(vmsLoadedMsg{scope: remoteScope, vms: []vm.VM{{Name: "remote-web", Status: "Running"}}})
	m = next.(model)

	names := boardNames(m)
	if len(names) != 1 || names[0] != "remote-web" {
		t.Fatalf("board = %v, want just the connected remote member's tile [remote-web] — a still-connecting member must not gate the board", names)
	}
	if m.members[0].state != connConnecting {
		t.Fatalf("the local member should still be connecting, got %v", m.members[0].state)
	}
	if m.members[1].state != connConnected {
		t.Fatalf("the remote member should be connected after its list, got %v", m.members[1].state)
	}

	// The board is interactive while the local member is still connecting.
	opened, _ := m.Update(runeKey('n'))
	if opened.(model).view != viewForm {
		t.Fatal("the board must stay interactive with a member still connecting: 'n' should open the create form")
	}
}

// TestErroredMemberSelfHealsWithBackoff drives one member fails-then-succeeds and
// asserts the per-member backoff schedule (5s → 30s → 60s, capped) on repeated
// failure, a reset to the normal cadence on the first success, and that its tiles
// appear only once it reconnects (dormant while errored, not before).
func TestErroredMemberSelfHealsWithBackoff(t *testing.T) {
	isolateHostState(t)
	seedManagedScoped(t, remoteScope, "remote-web")

	m := New(twoMemberFleet(&providerfake.Provider{}, &providerfake.Provider{})).(model)
	m = resized(m, 100, 30)

	fail := func() {
		t.Helper()
		next, _ := m.Update(vmsLoadedMsg{scope: remoteScope, err: errAnsibleBoom})
		m = next.(model)
	}

	// Consecutive failures back off 5 → 30 → 60, capped at 60.
	wantDelays := []time.Duration{5 * time.Second, 30 * time.Second, 60 * time.Second, 60 * time.Second}
	for i, want := range wantDelays {
		fail()
		if m.members[1].state != connErrored {
			t.Fatalf("failure %d: member should be errored, got %v", i+1, m.members[1].state)
		}
		if got := m.members[1].refreshDelay(); got != want {
			t.Fatalf("failure %d: refreshDelay = %v, want %v (5→30→60, capped)", i+1, got, want)
		}
	}

	// While errored the member contributes NO tiles even though it is managed —
	// nothing has successfully listed it yet.
	if got := boardNames(m); len(got) != 0 {
		t.Fatalf("an errored member with no successful list must show no tiles, got %v", got)
	}

	// A success resets the cadence AND brings the tiles in automatically.
	next, _ := m.Update(vmsLoadedMsg{scope: remoteScope, vms: []vm.VM{{Name: "remote-web", Status: "Running"}}})
	m = next.(model)
	if m.members[1].state != connConnected {
		t.Fatalf("a successful list should reconnect the member, got %v", m.members[1].state)
	}
	if got := m.members[1].refreshDelay(); got != refreshInterval {
		t.Fatalf("a successful list should reset the backoff to the normal cadence, got %v", got)
	}
	if got := boardNames(m); len(got) != 1 || got[0] != "remote-web" {
		t.Fatalf("the reconnected member's tile should appear automatically, got %v", got)
	}

	// The HEALTHY local member was never touched by the remote's backoff.
	if got := m.members[0].refreshDelay(); got != refreshInterval {
		t.Fatalf("a healthy member must keep the normal cadence regardless of another's backoff, got %v", got)
	}
}

// blockingRunner is a lima.Runner whose `list` BLOCKS until released — a stand-in
// for a remote profile whose SSH handshake hangs. Anything else no-ops.
type blockingProvider struct {
	providerfake.Provider
	release chan struct{}
}

func (b *blockingProvider) List() ([]vm.VM, error) {
	<-b.release // block until the test lets go
	return nil, nil
}

func (b *blockingProvider) HostFiles() lima.HostFiles { return lima.LocalFiles() }

// TestFleetBoardStaysInteractiveWhileAMemberBlocks drives the REAL Bubble Tea
// runtime: one member's List blocks indefinitely, and the board still launches,
// renders the other member's tile, and accepts keys — the async fleet's headline
// guarantee, proven end to end rather than at the Update level.
func TestFleetBoardStaysInteractiveWhileAMemberBlocks(t *testing.T) {
	isolateHostState(t)
	// NB: no pinHostCapacity here. A hung List goroutine, once released at
	// cleanup, samples the host-capacity package vars in refreshCmd — racing the
	// pin's own cleanup restore. This test asserts on rendered text, not a golden,
	// so it has no need of the pin and leaves those globals alone.
	//
	// The remote member has a managed VM to render; the LOCAL member blocks forever.
	seedManagedScoped(t, remoteScope, "remote-web")

	release := make(chan struct{})
	t.Cleanup(func() { close(release) }) // unblock the hung List goroutines on exit

	blocked := &blockingProvider{release: release}
	remote := &providerfake.Provider{
		ListFunc: func() ([]vm.VM, error) {
			return []vm.VM{{Name: "remote-web", Status: "Running"}}, nil
		},
	}
	// Local member (blocked) is the active one; the remote returns immediately.
	fleet := provider.Fleet{
		{Profile: profiles.Profile{ID: profiles.LocalProfileID, Type: profiles.TypeLocal, Enabled: true}, Prov: blocked, Scope: registry.LocalScope},
		{Profile: profiles.Profile{ID: "remote", Type: profiles.TypeRemoteSSH, Enabled: true}, Prov: remote, Scope: remoteScope},
	}
	tm := teatest.NewTestModel(t, New(fleet), teatest.WithInitialTermSize(100, 30))

	// The board renders the connected member's tile even though the local member's
	// List is hung — startup did not block on it.
	waitForText(t, tm, "remote-web")

	// And the board is interactive: 'n' opens the create form.
	tm.Send(runeKey('n'))
	waitForText(t, tm, "New VM")

	tm.Send(tea.KeyPressMsg{Code: tea.KeyEsc})
	tm.Quit()
	tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second))
}

// TestGhostSurvivesPersistentListErrorsAfterFirstSuccess pins finding 10 from
// the plan-16 code review: boardReady's doc says "has any list ever
// succeeded", but it used to check members' CURRENT state (connConnected)
// only — so a sole member that connects with zero VMs (the create-ghost
// shows, boardReady flips true) and LATER starts failing persistently would
// flip right back to connErrored, and boardReady would un-ring that bell:
// the empty board fell back to the "connecting to 1 profile…" hint and Enter
// stopped creating a VM, even though the fleet had already proven — once —
// that it has no VMs.
func TestGhostSurvivesPersistentListErrorsAfterFirstSuccess(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)

	// First list succeeds with zero VMs: the ghost shows.
	loaded, _ := m.Update(vmsLoadedMsg{vms: nil})
	m = loaded.(model)
	if !m.showsGhost() {
		t.Fatal("precondition: a successful empty list should show the create-ghost")
	}
	if !m.focusIsGhost() {
		t.Fatal("precondition: the ring should be on the ghost")
	}

	// The SAME member now starts failing persistently.
	failed, _ := m.Update(vmsLoadedMsg{err: errAnsibleBoom})
	m = failed.(model)
	if m.members[0].state != connErrored {
		t.Fatalf("precondition: the member should be errored now, got %v", m.members[0].state)
	}

	// THE FIX: the ghost (and Enter-to-create) must remain — the member
	// proved, once, that it has no VMs, and a later error must not un-prove
	// that.
	if !m.showsGhost() {
		t.Fatal("the create-ghost must survive a later persistent list error, once the fleet has proven empty")
	}
	entered, _ := press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if entered.view != viewForm {
		t.Fatalf("enter on the ghost must still create a VM after a later list error, got view %v", entered.view)
	}
}
