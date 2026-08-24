package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// markerProv is a providerfake.Provider that ALSO satisfies Provenancer,
// recording every marker written through it. The heal path reaches its backend
// by type-asserting the member's provider to provider.Provenancer, so a double
// that stops at provider.Provider would make every test here vacuously pass by
// taking the "no marker facility" early return.
type markerProv struct {
	providerfake.Provider
	mu      sync.Mutex
	written map[string]provider.Provenance
	writes  int
	err     error
}

func (p *markerProv) MarkManaged(_ context.Context, name string, pv provider.Provenance) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes++
	if p.err != nil {
		return p.err
	}
	if p.written == nil {
		p.written = map[string]provider.Provenance{}
	}
	p.written[name] = pv
	return nil
}

func (p *markerProv) Provenance(context.Context) (map[string]provider.Provenance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.written, nil
}

func (p *markerProv) ProvenanceOf(_ context.Context, name string) (provider.Provenance, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pv, ok := p.written[name]
	return pv, ok, nil
}

func (p *markerProv) Unmark(_ context.Context, name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.written, name)
	return nil
}

func (p *markerProv) snapshot() (map[string]provider.Provenance, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]provider.Provenance{}
	for k, v := range p.written {
		out[k] = v
	}
	return out, p.writes
}

// healModel builds a one-member model whose backend records marker writes, with
// `name` carrying an in-flight marker stamped `age` ago.
func healModel(t *testing.T, name string, age time.Duration) (model, *markerProv) {
	t.Helper()
	m := newTestModel(t)
	m = resized(m, 120, 40)
	fake := &markerProv{}
	m.members[0].prov = fake
	m.members[0].state = connConnected
	m.members[0].vms = []vm.VM{{Name: name, Status: "Running"}}
	m.members[0].provenance = map[string]provider.Provenance{
		name: {
			SchemaVersion:  provider.MarkerSchemaVersion,
			Base:           "sandbar-base",
			Config:         vm.CreateConfig{Name: name, BaseName: "sandbar-base", CPUs: 8},
			SandbarVersion: "318afb4",
			CreatedAt:      time.Now().Add(-age).UTC().Format(time.RFC3339),
			Provisioning:   true,
			Progress:       provider.BuildProgress{Role: "project", Index: 204},
		},
	}
	return m, fake
}

// runCmds executes every returned command synchronously and feeds each resulting
// message back through Update, which is what the Bubble Tea runtime does and
// what the in-flight guard and the map patch both depend on.
func runCmds(t *testing.T, m model, cmds []tea.Cmd) model {
	t.Helper()
	for _, c := range cmds {
		if c == nil {
			continue
		}
		msg := c()
		if msg == nil {
			continue
		}
		next, _ := m.Update(msg)
		got, ok := next.(model)
		if !ok {
			t.Fatalf("Update did not return a model")
		}
		m = got
	}
	return m
}

// TestAbandonedMarkerIsHealed is the core proof: a marker left in-flight by a
// build that stopped reporting long ago is rewritten as READY, automatically, on
// an ordinary refresh — no user action, and no verb to discover.
//
// It also pins WHAT is written. The repair must not rebuild the marker from the
// healing controller's own state: Base, Config and SandbarVersion describe the
// build that actually produced this VM, recreate-gating reads them, and this
// controller may not be the one that ran it.
func TestAbandonedMarkerIsHealed(t *testing.T) {
	m, fake := healModel(t, "lullabot-proposals", 10*24*time.Hour)

	// Precondition: this is exactly the wedged tile the feature exists to fix.
	v := vm.VM{Name: "lullabot-proposals", Status: "Running"}
	if got := m.statusOf(registry.LocalScope, v); got != statusBuilding {
		t.Fatalf("precondition: a stale in-flight marker must read Building, got %v", got)
	}

	cmds := m.healAbandonedMarkers(&m.members[0], time.Now())
	if len(cmds) != 1 {
		t.Fatalf("want exactly one repair dispatched, got %d", len(cmds))
	}
	m = runCmds(t, m, cmds)

	written, _ := fake.snapshot()
	got, ok := written["lullabot-proposals"]
	if !ok {
		t.Fatal("the repair wrote no marker at all")
	}
	if got.Provisioning {
		t.Error("the repaired marker is still flagged Provisioning")
	}
	if got.Progress != (provider.BuildProgress{}) {
		t.Errorf("the repaired marker still carries build progress: %+v", got.Progress)
	}
	if got.Base != "sandbar-base" || got.Config.CPUs != 8 || got.SandbarVersion != "318afb4" {
		t.Errorf("the repair must preserve the original build's record, got %+v", got)
	}
}

// TestHealedMarkerFlipsTheTileImmediately pins the half of the repair the user
// actually sees. Writing the marker fixes the HOST; the board reads its own
// cached provenance map and would go on painting Building until the next refresh
// re-read it. Patching the map on success is what makes the tile change in the
// frame the repair lands.
func TestHealedMarkerFlipsTheTileImmediately(t *testing.T) {
	m, _ := healModel(t, "web", 10*24*time.Hour)
	m = runCmds(t, m, m.healAbandonedMarkers(&m.members[0], time.Now()))

	v := vm.VM{Name: "web", Status: "Running"}
	if got := m.statusOf(registry.LocalScope, v); got != statusRunning {
		t.Fatalf("statusOf = %v, want statusRunning once the marker is repaired", got)
	}
	if m.members[0].provenance["web"].Provisioning {
		t.Error("the member's cached marker is still in-flight after a successful repair")
	}
}

// TestOwnBuildIsNeverHealed is the veto that makes the whole feature safe to run
// on a clock. BuildAbandoned can only see the marker; the ONE controller whose
// judgement must beat the cutoff is the one actually running the build, whose
// own role could in principle outlast it. Healing there would have this
// controller declare its OWN in-progress build finished.
func TestOwnBuildIsNeverHealed(t *testing.T) {
	m, fake := healModel(t, "web", 10*24*time.Hour)
	seedJob(t, &m, "web", vm.CreateConfig{Name: "web", BaseName: "sandbar-base"})

	if cmds := m.healAbandonedMarkers(&m.members[0], time.Now()); len(cmds) != 0 {
		t.Fatalf("a VM this controller is building must never be healed, got %d repairs", len(cmds))
	}
	if _, writes := fake.snapshot(); writes != 0 {
		t.Errorf("no marker write should have been attempted, got %d", writes)
	}
}

// TestLiveBuildElsewhereIsNotHealed covers the other side of the same rule: a
// marker republished RECENTLY is a build reporting in normally, and the cutoff
// must leave it alone. An in-flight marker's CreatedAt is restamped at every
// Ansible role boundary, so "recent" genuinely means "still building".
func TestLiveBuildElsewhereIsNotHealed(t *testing.T) {
	m, fake := healModel(t, "web", 30*time.Second)

	if cmds := m.healAbandonedMarkers(&m.members[0], time.Now()); len(cmds) != 0 {
		t.Fatalf("a freshly republished marker must be left alone, got %d repairs", len(cmds))
	}
	if _, writes := fake.snapshot(); writes != 0 {
		t.Errorf("no marker write should have been attempted, got %d", writes)
	}
	// And the tile still reads Building, which is the correct answer here.
	if got := m.statusOf(registry.LocalScope, vm.VM{Name: "web", Status: "Running"}); got != statusBuilding {
		t.Errorf("statusOf = %v, want statusBuilding for a live remote build", got)
	}
}

// TestHealIsNotRedispatchedWhileInFlight guards the cost of the repair. This
// runs on every refresh — a few seconds apart — while the write is a host round
// trip, so without the in-flight guard one stuck marker would queue a fresh
// write on every tick until the first one landed.
func TestHealIsNotRedispatchedWhileInFlight(t *testing.T) {
	m, _ := healModel(t, "web", 10*24*time.Hour)

	first := m.healAbandonedMarkers(&m.members[0], time.Now())
	if len(first) != 1 {
		t.Fatalf("want one repair on the first pass, got %d", len(first))
	}
	// A second refresh arrives before the first write has reported back.
	if second := m.healAbandonedMarkers(&m.members[0], time.Now()); len(second) != 0 {
		t.Fatalf("a repair already in flight must not be dispatched again, got %d", len(second))
	}
	// Once it reports, the guard is released — the marker is repaired by then, so
	// there is nothing left to dispatch, but a FAILED one must be retryable.
	m = runCmds(t, m, first)
	if m.members[0].healing["web"] {
		t.Error("the in-flight guard was not released when the repair reported back")
	}
}

// TestHealFailureIsWarnedOnceAndRetried pins the failure contract. A marker that
// cannot be written leaves the tile reading Building — the exact symptom — so it
// must be said. But the repair reruns every refresh, so saying it every time
// would bury the Messages log; and latching it off entirely would make a
// transient transport failure permanent.
func TestHealFailureIsWarnedOnceAndRetried(t *testing.T) {
	m, fake := healModel(t, "web", 10*24*time.Hour)
	fake.err = errors.New("connection reset")

	for i := range 3 {
		cmds := m.healAbandonedMarkers(&m.members[0], time.Now())
		if len(cmds) != 1 {
			t.Fatalf("pass %d: a failed repair must be retried on the next refresh, got %d", i, len(cmds))
		}
		m = runCmds(t, m, cmds)
	}

	if _, writes := fake.snapshot(); writes != 3 {
		t.Errorf("want 3 write attempts across 3 refreshes, got %d", writes)
	}
	var warns int
	for _, line := range m.messages {
		if strings.Contains(line.text, "could not clear the stale building marker") {
			warns++
		}
	}
	if warns != 1 {
		t.Errorf("want the failure stated exactly once, got %d times in %v", warns, m.messages)
	}
	// The ANNOUNCEMENT is latched for the same reason the warning is. The repair
	// is retried every 5s refresh; narrating each attempt would fill the 50-entry
	// Messages ring with this one sentence in about four minutes and evict the
	// warning above — the only line that says why the repair is not sticking.
	var said int
	for _, line := range m.messages {
		if strings.Contains(line.text, "interrupted run") {
			said++
		}
	}
	if said != 1 {
		t.Errorf("want the repair announced exactly once across the retries, got %d times in %v", said, m.messages)
	}
}

// TestHealIsAnnouncedAgainForALaterStuckMarker is the other half of the latch:
// it must not be permanent. A repair that lands clears the latch, so a build
// interrupted LATER in the same session — same VM, same member — is announced
// again rather than repaired in a silence the user cannot account for.
func TestHealIsAnnouncedAgainForALaterStuckMarker(t *testing.T) {
	m, _ := healModel(t, "web", 10*24*time.Hour)
	stale := m.members[0].provenance["web"]

	m = runCmds(t, m, m.healAbandonedMarkers(&m.members[0], time.Now()))
	// A refresh over the repaired marker clears the latch.
	m.healAbandonedMarkers(&m.members[0], time.Now())
	// A second interrupted build leaves the same VM stuck again.
	m.members[0].provenance["web"] = stale
	m = runCmds(t, m, m.healAbandonedMarkers(&m.members[0], time.Now()))

	var said int
	for _, line := range m.messages {
		if strings.Contains(line.text, "interrupted run") {
			said++
		}
	}
	if said != 2 {
		t.Errorf("a later interrupted build must be announced too, got %d announcements in %v", said, m.messages)
	}
}

// TestHealSaysWhatItDid keeps the repair from being invisible magic. The user
// watched this tile claim to be building; a line in the Messages log is what
// connects the tile they remember to the tile they now see.
func TestHealSaysWhatItDid(t *testing.T) {
	m, _ := healModel(t, "lullabot-proposals", 10*24*time.Hour)
	m = runCmds(t, m, m.healAbandonedMarkers(&m.members[0], time.Now()))

	var found bool
	for _, line := range m.messages {
		if strings.Contains(line.text, "lullabot-proposals") && strings.Contains(line.text, "interrupted run") {
			found = true
		}
	}
	if !found {
		t.Errorf("the repair should be narrated in the Messages log, got %v", m.messages)
	}
}

// TestRefreshHealsAnAbandonedMarkerEndToEnd drives the REAL refresh path — a
// vmsLoadedMsg through Update, exactly as the fleet's poll delivers it — rather
// than calling healAbandonedMarkers directly. The unit tests above prove the
// rule; this proves it is actually wired to something that runs, which is the
// half that silently rots when a handler is refactored.
//
// It is also the closest thing to the reported symptom: a `sand` that has been
// restarted since the build, so there is no local job anywhere, meeting a marker
// left in-flight days ago.
func TestRefreshHealsAnAbandonedMarkerEndToEnd(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)
	fake := &markerProv{}
	m.members[0].prov = fake

	stale := provider.Provenance{
		SchemaVersion:  provider.MarkerSchemaVersion,
		Base:           "sandbar-base",
		Config:         vm.CreateConfig{Name: "lullabot-proposals", BaseName: "sandbar-base"},
		SandbarVersion: "318afb4",
		CreatedAt:      time.Now().Add(-10 * 24 * time.Hour).UTC().Format(time.RFC3339),
		Provisioning:   true,
		Progress:       provider.BuildProgress{Role: "project", Index: 204},
	}

	next, cmd := m.Update(vmsLoadedMsg{
		scope:      registry.LocalScope,
		vms:        []vm.VM{{Name: "lullabot-proposals", Status: "Stopped"}},
		provenance: map[string]provider.Provenance{"lullabot-proposals": stale},
	})
	m = next.(model)

	// A refresh returns a BATCH (the repair alongside the adoption migration), so
	// unwrap one level before running the leaves.
	m = runCmds(t, m, flatten(cmd))

	written, _ := fake.snapshot()
	got, ok := written["lullabot-proposals"]
	if !ok {
		t.Fatal("an ordinary refresh did not repair the abandoned marker")
	}
	if got.Provisioning || got.Progress != (provider.BuildProgress{}) {
		t.Errorf("the marker written by the refresh is still in-flight: %+v", got)
	}
	// And the symptom is gone: a STOPPED VM reads Stopped again, which it could
	// not do before — remoteProvisioning is checked ahead of the VM's real status.
	if s := m.statusOf(registry.LocalScope, vm.VM{Name: "lullabot-proposals", Status: "Stopped"}); s != statusStopped {
		t.Errorf("statusOf = %v, want statusStopped once the marker is repaired", s)
	}
}

// flatten runs a command far enough to collect the individual leaves of a
// tea.Batch, so a test can execute them itself. A nil command yields nothing.
func flatten(cmd tea.Cmd) []tea.Cmd {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		return msg
	case nil:
		return nil
	default:
		// Not a batch: hand back a command that replays the message we just took.
		return []tea.Cmd{func() tea.Msg { return msg }}
	}
}

// TestBackendWithoutProvenanceIsNeverHealed pins the early return for a backend
// that has no marker facility at all. Provenancer is an OPTIONAL seam — the
// board type-asserts for it — so this path is reachable by any test double and
// by any future backend that cannot store a marker, and it must be a quiet
// no-op rather than a panic on a nil assertion.
func TestBackendWithoutProvenanceIsNeverHealed(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)
	m.members[0].prov = &providerfake.Provider{} // provider.Provider, NOT Provenancer
	m.members[0].provenance = map[string]provider.Provenance{
		"web": {Provisioning: true, CreatedAt: time.Now().Add(-10 * 24 * time.Hour).UTC().Format(time.RFC3339)},
	}

	if cmds := m.healAbandonedMarkers(&m.members[0], time.Now()); len(cmds) != 0 {
		t.Fatalf("a backend with no marker facility has nothing to repair, got %d", len(cmds))
	}
}

// TestHealResultForADeletedProfileIsDropped covers the window live profile
// management opens: a repair is a host round trip, and the profile it belongs to
// can be deleted or connection-edited while it is still in flight. Its result
// then names a scope no member owns. Applying it anyway would splice one
// profile's repair onto whichever member happens to sit at that index now.
func TestHealResultForADeletedProfileIsDropped(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)

	orphan := registry.Scope{Provider: "remote", RemoteTarget: "a-profile-that-is-gone"}
	m.applyMarkerHealed(markerHealedMsg{scope: orphan, vm: "web", err: errors.New("boom")})

	// Nothing recorded, and in particular no warning attributed to a member that
	// never asked for this repair.
	for _, line := range m.messages {
		if strings.Contains(line.text, "stale building marker") {
			t.Fatalf("a repair for an unknown scope must be dropped, got %q", line.text)
		}
	}
}
