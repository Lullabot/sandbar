package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
)

// reclaimProvider adds only the optional memory-reclaim capability to the
// ordinary provider fake. Embedding provider.Provider is deliberate: capability
// detection must depend on the dynamic provider type, not on a flag in the UI.
type reclaimProvider struct {
	provider.Provider
	reclaim func(context.Context, string) error
}

func (p *reclaimProvider) ReclaimMemory(ctx context.Context, name string) error {
	return p.reclaim(ctx, name)
}

type reclaimMetricProvider struct {
	*reclaimProvider
	hostMemory func(context.Context, string) (int64, error)
}

func (p *reclaimMetricProvider) VMHostMemory(ctx context.Context, name string) (int64, error) {
	return p.hostMemory(ctx, name)
}

func newReclaimModel(t *testing.T, p provider.Provider) model {
	t.Helper()
	isolateHostState(t)
	m, ok := New(singleFleet(p, registry.LocalScope)).(model)
	if !ok {
		t.Fatal("New did not return a model")
	}
	return m
}

func TestReclaimMemoryCapabilityGatesAndRunsAction(t *testing.T) {
	const name = "web"

	unsupported := newTestModel(t)
	unsupported = focusTile(t, unsupported, vm.VM{Name: name, Status: limaRunning})
	if got := boardVerbs(unsupported); strings.Contains(got, "m reclaim memory") {
		t.Fatalf("unsupported provider offered reclaim memory:\n%s", got)
	}

	var reclaimed string
	capable := &reclaimProvider{
		Provider: &providerfake.Provider{},
		reclaim: func(_ context.Context, got string) error {
			reclaimed = got
			return nil
		},
	}
	m := newReclaimModel(t, capable)
	m = focusTile(t, m, vm.VM{Name: name, Status: limaRunning})
	if got := boardVerbs(m); !strings.Contains(got, "m reclaim memory") {
		t.Fatalf("capable running VM did not offer reclaim memory:\n%s", got)
	}

	after, cmd := pressDispatch(t, m, runeKey('m'))
	if cmd == nil || !after.acting {
		t.Fatal("reclaim memory should start a quick action with progress feedback")
	}
	done := actionDone(t, cmd)
	if done.err != nil {
		t.Fatalf("reclaim memory command failed: %v", done.err)
	}
	if !done.refreshMetrics {
		t.Fatal("reclaim completion did not request fresh guest and host readings")
	}
	if reclaimed != name {
		t.Fatalf("ReclaimMemory called for %q, want %q", reclaimed, name)
	}

	stopped := newReclaimModel(t, capable)
	stopped = focusTile(t, stopped, vm.VM{Name: name, Status: "Stopped"})
	if got := boardVerbs(stopped); strings.Contains(got, "m reclaim memory") {
		t.Fatalf("stopped VM offered reclaim memory:\n%s", got)
	}
}

func TestReclaimMemoryFailureIsReported(t *testing.T) {
	want := errors.New("sudo denied")
	p := &reclaimProvider{
		Provider: &providerfake.Provider{},
		reclaim:  func(context.Context, string) error { return want },
	}
	m := newReclaimModel(t, p)
	m = focusTile(t, m, vm.VM{Name: "web", Status: limaRunning})

	_, cmd := pressDispatch(t, m, runeKey('m'))
	done := actionDone(t, cmd)
	next, _ := m.dispatch(done)
	got := next.(model).lastMessage()
	if got != "reclaim memory web failed: sudo denied" {
		t.Fatalf("failure message = %q", got)
	}
}

// TestReclaimMemoryCompletionRefreshesGuestAndHost crosses the two boundaries
// the user cares about after the action: a new guest stream is opened, and the
// provider's host-memory method is called. Merely checking that Update returned
// a command would allow either measurement to stay stale.
func TestReclaimMemoryCompletionRefreshesGuestAndHost(t *testing.T) {
	const (
		name       = "web"
		hostMemory = int64(30 << 30)
		guestCache = uint64(11 << 20)
	)
	var hostReads atomic.Int32

	fake := &providerfake.Provider{
		ShellStreamOutFunc: func(ctx context.Context, _ string, _ io.Reader, out io.Writer, _ ...string) error {
			_, _ = io.WriteString(out, strings.Join([]string{
				"MemTotal: 32768 kB",
				"MemAvailable: 16384 kB",
				"Buffers: 1024 kB",
				"Cached: 8192 kB",
				"SReclaimable: 2048 kB",
				"Shmem: 0 kB",
				heartbeatDelim,
				"",
			}, "\n"))
			<-ctx.Done()
			return ctx.Err()
		},
	}
	p := &reclaimMetricProvider{
		reclaimProvider: &reclaimProvider{
			Provider: fake,
			reclaim:  func(context.Context, string) error { return nil },
		},
		hostMemory: func(context.Context, string) (int64, error) {
			hostReads.Add(1)
			return hostMemory, nil
		},
	}
	m := newReclaimModel(t, p)
	m = focusTile(t, m, vm.VM{Name: name, Status: limaRunning, Memory: "32GiB"})
	t.Cleanup(m.heartbeats.stopAll)

	// focusTile's real vmsLoadedMsg path establishes the pre-reclaim stream. Its
	// commands were deliberately not run: this test must prove completion
	// schedules new measurements independently of periodic samples already pending.
	key := vmHandle{Scope: registry.LocalScope, Name: name}
	old, exists := m.heartbeats.beats[key]
	if !exists {
		t.Fatal("precondition: running VM did not start a heartbeat")
	}
	oldEpoch := old.epoch

	_, action := pressDispatch(t, m, runeKey('m'))
	done := actionDone(t, action)
	next, _ := m.dispatch(done)
	m = next.(model)
	if _, exists := m.heartbeats.beats[key]; exists {
		t.Fatal("reclaim completion kept the pre-reclaim heartbeat")
	}

	refresh := m.syncHeartbeats()
	if refresh == nil {
		t.Fatal("reclaim completion did not start fresh metric commands")
	}
	if got := m.heartbeats.beats[key].epoch; got <= oldEpoch {
		t.Fatalf("heartbeat epoch after reclaim = %d, want newer than %d", got, oldEpoch)
	}

	refreshMsg := refresh()
	batch, ok := refreshMsg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("metric refresh returned %T, want tea.BatchMsg", refreshMsg)
	}
	var guestSeen, hostSeen bool
	for _, cmd := range batch {
		msg := cmd()
		switch msg.(type) {
		case heartbeatSampleMsg:
			guestSeen = true
		case heartbeatHostMemoryMsg:
			hostSeen = true
		}
		next, _ = m.dispatch(msg)
		m = next.(model)
	}
	if !guestSeen || !hostSeen {
		t.Fatalf("post-reclaim refresh results: guest=%v host=%v", guestSeen, hostSeen)
	}
	if got := hostReads.Load(); got != 1 {
		t.Fatalf("post-reclaim host-memory reads = %d, want 1", got)
	}
	sample, ok := m.sampleOf(registry.LocalScope, name)
	if !ok || !sample.HasCache || sample.Cache != guestCache {
		t.Fatalf("fresh guest cache = (%d, has=%v, sample=%v), want %d", sample.Cache, sample.HasCache, ok, guestCache)
	}
	if !sample.HasHostMem || sample.HostMemUsed != uint64(hostMemory) {
		t.Fatalf("fresh host memory = (%d, has=%v), want %d", sample.HostMemUsed, sample.HasHostMem, hostMemory)
	}
}
