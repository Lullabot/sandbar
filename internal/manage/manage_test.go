package manage

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// fakeProvenancer is an in-memory provider.Provenancer test double — no real
// limactl/SSH host, just a map keyed by name — so RecreateBase/RecordSuccess's
// new provenance-first resolution can be exercised without a real Lima
// instance dir (AGENTS.md's hard rule against real limactl/SSH in tests).
type fakeProvenancer struct {
	markers   map[string]provider.Provenance
	markErr   error // returned by MarkManaged, if set
	readErr   error // returned by ProvenanceOf for ANY name, if set
	markCalls []struct {
		name string
		p    provider.Provenance
	}
}

func newFakeProvenancer() *fakeProvenancer {
	return &fakeProvenancer{markers: map[string]provider.Provenance{}}
}

func (f *fakeProvenancer) Provenance(context.Context) (map[string]provider.Provenance, error) {
	return f.markers, nil
}

func (f *fakeProvenancer) ProvenanceOf(_ context.Context, name string) (provider.Provenance, bool, error) {
	if f.readErr != nil {
		return provider.Provenance{}, false, f.readErr
	}
	p, ok := f.markers[name]
	return p, ok, nil
}

func (f *fakeProvenancer) MarkManaged(_ context.Context, name string, p provider.Provenance) error {
	if f.markErr != nil {
		return f.markErr
	}
	f.markers[name] = p
	f.markCalls = append(f.markCalls, struct {
		name string
		p    provider.Provenance
	}{name, p})
	return nil
}

func (f *fakeProvenancer) Unmark(_ context.Context, name string) error {
	delete(f.markers, name)
	return nil
}

var _ provider.Provenancer = (*fakeProvenancer)(nil)

// TestReconcile verifies the drift-guard drops managed entries whose VM is no
// longer present in the live `limactl list` result, and reports the names it
// dropped — the mechanism that stops a VM deleted outside sand from staying
// flagged managed (and recreate-able) forever.
func TestReconcile(t *testing.T) {
	reg := registry.NewEmpty()
	for _, name := range []string{"a", "b", "c"} {
		cfg := vm.CreateConfig{Name: name, BaseName: "sandbar-base"}
		if err := reg.Add(cfg); err != nil {
			t.Fatalf("seed registry with %q: %v", name, err)
		}
	}

	live := []vm.VM{{Name: "a"}, {Name: "c"}}

	dropped, err := Reconcile(reg, live, registry.LocalScope)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	sort.Strings(dropped)
	if !reflect.DeepEqual(dropped, []string{"b"}) {
		t.Fatalf("dropped = %v, want [b]", dropped)
	}

	if reg.IsManaged("b") {
		t.Fatal("Reconcile left \"b\" managed after it was absent from live")
	}
	if !reg.IsManaged("a") || !reg.IsManaged("c") {
		t.Fatalf("Reconcile dropped a live VM: a managed=%v c managed=%v", reg.IsManaged("a"), reg.IsManaged("c"))
	}
}

// TestReconcile_NoneDropped confirms a live list matching the registry
// exactly leaves it untouched and reports no drops.
func TestReconcile_NoneDropped(t *testing.T) {
	reg := registry.NewEmpty()
	cfg := vm.CreateConfig{Name: "claude", BaseName: "sandbar-base"}
	if err := reg.Add(cfg); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	dropped, err := Reconcile(reg, []vm.VM{{Name: "claude"}}, registry.LocalScope)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if dropped != nil {
		t.Fatalf("dropped = %v, want nil", dropped)
	}
	if !reg.IsManaged("claude") {
		t.Fatal("Reconcile dropped a VM still present in live")
	}
}

// TestReconcile_DoesNotCrossProviders is the provider-scoping regression this
// task adds: reconciling a LOCAL provider's live list must never prune (or
// even consider) an entry a REMOTE provider owns, even though that entry's
// name is absent from the local live list — a local `List` has no way to know
// whether a remote instance is still there, and must not treat "absent from
// MY list" as "gone" for a VM it doesn't own. Only Reconcile confining itself
// to registry.LocalScope (via registry.ReconcileScoped) is what makes that
// safe.
func TestReconcile_DoesNotCrossProviders(t *testing.T) {
	reg := registry.NewEmpty()
	remoteScope := registry.Scope{Provider: "lima-remote", RemoteTarget: "dev@example.com:22"}

	if err := RecordSuccess(reg, vm.CreateConfig{Name: "claude", BaseName: "claude-base"}, registry.LocalScope); err != nil {
		t.Fatalf("seed local entry: %v", err)
	}
	if err := RecordSuccess(reg, vm.CreateConfig{Name: "web", BaseName: "claude-base"}, remoteScope); err != nil {
		t.Fatalf("seed remote entry: %v", err)
	}

	// A local `limactl list` reports NOTHING at all (as if the local "claude"
	// had been deleted outside sand, and as it always will for a remote-owned
	// VM the local backend has never heard of). Reconciling it against
	// registry.LocalScope must drop only the LOCAL entry.
	dropped, err := Reconcile(reg, nil, registry.LocalScope)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !reflect.DeepEqual(dropped, []string{"claude"}) {
		t.Fatalf("dropped = %v, want [claude] (the local entry only)", dropped)
	}

	// The remote-owned "web" must have survived untouched, even though it too
	// was absent from the (local) live list handed to Reconcile.
	base, ok := reg.BaseInScope("web", remoteScope)
	if !ok || base != "claude-base" {
		t.Fatalf("remote entry was pruned by a local-scoped reconcile: ok=%v base=%q", ok, base)
	}
	if reg.IsManaged("claude") {
		t.Fatal("local entry should have been dropped")
	}
}

// TestRecordSuccess verifies a successful create/recreate is recorded as
// managed with its CreateConfig retrievable from the registry — the
// bookkeeping shared between the TUI and the headless `sand create` path.
func TestRecordSuccess(t *testing.T) {
	reg := registry.NewEmpty()
	cfg := vm.CreateConfig{
		Name:     "claude",
		BaseName: "sandbar-base",
		GitName:  "Ada Lovelace",
		GitEmail: "ada@example.com",
		CPUs:     4,
		Memory:   "8GiB",
		Disk:     "100GiB",
	}

	if err := RecordSuccess(reg, cfg, registry.LocalScope); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}

	if !reg.IsManaged(cfg.Name) {
		t.Fatalf("RecordSuccess did not mark %q managed", cfg.Name)
	}
	got, ok := reg.Config(cfg.Name)
	if !ok {
		t.Fatalf("registry has no config recorded for %q", cfg.Name)
	}
	if got != cfg {
		t.Fatalf("recorded config = %+v, want %+v", got, cfg)
	}
}

// TestRecreateBase covers the three-way gate that decides which VMs may be
// recreated: refused outright for a VM sand did not create, the recorded
// base for one it did, and the default base name when a managed entry
// predates recording one (e.g. an older index format).
func TestRecreateBase(t *testing.T) {
	t.Run("unmanaged VM is refused", func(t *testing.T) {
		reg := registry.NewEmpty()

		base, ok := RecreateBase(reg, "not-managed", registry.LocalScope)
		if ok {
			t.Fatalf("RecreateBase(unmanaged) ok = true, want false (base=%q)", base)
		}
		if base != "" {
			t.Fatalf("RecreateBase(unmanaged) base = %q, want empty", base)
		}
	})

	t.Run("managed VM returns its recorded base", func(t *testing.T) {
		reg := registry.NewEmpty()
		cfg := vm.CreateConfig{Name: "claude", BaseName: "custom-base"}
		if err := reg.Add(cfg); err != nil {
			t.Fatalf("seed registry: %v", err)
		}

		base, ok := RecreateBase(reg, "claude", registry.LocalScope)
		if !ok {
			t.Fatal("RecreateBase(managed) ok = false, want true")
		}
		if base != "custom-base" {
			t.Fatalf("RecreateBase(managed) base = %q, want %q", base, "custom-base")
		}
	})

	t.Run("managed VM with no recorded base falls back to default", func(t *testing.T) {
		reg := registry.NewEmpty()
		cfg := vm.CreateConfig{Name: "claude", BaseName: ""}
		if err := reg.Add(cfg); err != nil {
			t.Fatalf("seed registry: %v", err)
		}

		base, ok := RecreateBase(reg, "claude", registry.LocalScope)
		if !ok {
			t.Fatal("RecreateBase(managed, no base) ok = false, want true")
		}
		want := vm.DefaultCreateConfig().BaseName
		if base != want {
			t.Fatalf("RecreateBase(managed, no base) base = %q, want default %q", base, want)
		}
	})
}

// TestRecordSuccess_WritesProvenanceMarker verifies the provenance rewire's
// central write path: when a Provenancer is supplied, RecordSuccess writes
// the authoritative marker (carrying the base name and CreateConfig) via
// MarkManaged, in ADDITION to the existing registry cache write — neither
// write is skipped in favor of the other.
func TestRecordSuccess_WritesProvenanceMarker(t *testing.T) {
	reg := registry.NewEmpty()
	prov := newFakeProvenancer()
	cfg := vm.CreateConfig{
		Name:       "claude",
		BaseName:   "custom-base",
		GitName:    "Ada Lovelace",
		GitEmail:   "ada@example.com",
		CPUs:       4,
		Memory:     "8GiB",
		Disk:       "100GiB",
		CloneToken: "ghp_secret", // must never reach the marker
	}

	if err := RecordSuccess(reg, cfg, registry.LocalScope, prov); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}

	// The registry cache write still happens.
	if !reg.IsManaged(cfg.Name) {
		t.Fatal("RecordSuccess did not update the registry cache")
	}

	got, ok, err := prov.ProvenanceOf(context.Background(), cfg.Name)
	if err != nil {
		t.Fatalf("ProvenanceOf: %v", err)
	}
	if !ok {
		t.Fatal("RecordSuccess did not write a provenance marker")
	}
	if got.Base != cfg.BaseName {
		t.Fatalf("marker Base = %q, want %q", got.Base, cfg.BaseName)
	}
	if got.Config.CloneToken != "" {
		t.Fatalf("marker Config retained the clone token: %q", got.Config.CloneToken)
	}
	wantConfig := cfg
	wantConfig.CloneToken = ""
	if got.Config != wantConfig {
		t.Fatalf("marker Config = %+v, want %+v", got.Config, wantConfig)
	}
}

// TestRecordSuccess_NoProvenancerIsRegistryOnly verifies the backward-compat
// path every existing (pre-provenance) caller relies on: omitting the
// trailing Provenancer argument entirely skips MarkManaged and behaves
// exactly like the old registry-only RecordSuccess — this is what keeps
// internal/ui's call sites compiling and working unchanged until they are
// rewired.
func TestRecordSuccess_NoProvenancerIsRegistryOnly(t *testing.T) {
	reg := registry.NewEmpty()
	cfg := vm.CreateConfig{Name: "claude", BaseName: "sandbar-base"}

	if err := RecordSuccess(reg, cfg, registry.LocalScope); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
	if !reg.IsManaged(cfg.Name) {
		t.Fatal("RecordSuccess did not update the registry cache")
	}
}

// TestRecordSuccess_MarkManagedFailureIsSurfaced verifies a MarkManaged
// failure is returned, not silently swallowed — a VM with no marker is
// invisible to any other controller that later looks at it, so this must
// never be a silent no-op even though the registry write already succeeded.
func TestRecordSuccess_MarkManagedFailureIsSurfaced(t *testing.T) {
	reg := registry.NewEmpty()
	prov := newFakeProvenancer()
	prov.markErr = errors.New("disk full")
	cfg := vm.CreateConfig{Name: "claude", BaseName: "sandbar-base"}

	err := RecordSuccess(reg, cfg, registry.LocalScope, prov)
	if err == nil {
		t.Fatal("RecordSuccess: want an error when MarkManaged fails, got nil")
	}
	if !errors.Is(err, prov.markErr) {
		t.Fatalf("RecordSuccess error = %v, want it to wrap %v", err, prov.markErr)
	}
	// The registry cache write is best-effort infrastructure independent of
	// the marker; it should still have gone through.
	if !reg.IsManaged(cfg.Name) {
		t.Fatal("RecordSuccess did not update the registry cache despite the marker failure")
	}
}

// TestRecreateBase_ManagedViaMarker is the central provenance-first
// resolution test: a VM with NO registry entry at all, but a provenance
// marker recording its base, must still be recreate-able, cloning from the
// MARKER's base — proving provenance alone (no registry fallback needed) now
// drives the recreate gate.
func TestRecreateBase_ManagedViaMarker(t *testing.T) {
	reg := registry.NewEmpty() // deliberately empty: no registry entry for "claude"
	prov := newFakeProvenancer()
	prov.markers["claude"] = provider.Provenance{Base: "marker-base"}

	base, ok := RecreateBase(reg, "claude", registry.LocalScope, prov)
	if !ok {
		t.Fatal("RecreateBase(managed via marker) ok = false, want true")
	}
	if base != "marker-base" {
		t.Fatalf("RecreateBase(managed via marker) base = %q, want %q", base, "marker-base")
	}
}

// TestRecreateBase_MarkerWithNoBaseFallsBackToDefault mirrors the existing
// registry-only "no recorded base" case, but sourced from a marker: an empty
// Base on an otherwise-present marker still resolves to the default base
// image name rather than an empty clone source.
func TestRecreateBase_MarkerWithNoBaseFallsBackToDefault(t *testing.T) {
	reg := registry.NewEmpty()
	prov := newFakeProvenancer()
	prov.markers["claude"] = provider.Provenance{Base: ""}

	base, ok := RecreateBase(reg, "claude", registry.LocalScope, prov)
	if !ok {
		t.Fatal("RecreateBase(marker, no base) ok = false, want true")
	}
	want := vm.DefaultCreateConfig().BaseName
	if base != want {
		t.Fatalf("RecreateBase(marker, no base) base = %q, want default %q", base, want)
	}
}

// TestRecreateBase_RefusedWhenNeitherMarkerNorRegistry is the refusal-gate
// regression this task's rewire must preserve: a name with NO provenance
// marker AND no registry entry is refused outright, even though a
// Provenancer was supplied — an unmanaged/unknown VM must never become
// recreate-able just because provenance was consulted.
func TestRecreateBase_RefusedWhenNeitherMarkerNorRegistry(t *testing.T) {
	reg := registry.NewEmpty()
	prov := newFakeProvenancer() // no markers seeded

	base, ok := RecreateBase(reg, "ghost", registry.LocalScope, prov)
	if ok {
		t.Fatalf("RecreateBase(no marker, no registry) ok = true, want false (base=%q)", base)
	}
	if base != "" {
		t.Fatalf("RecreateBase(no marker, no registry) base = %q, want empty", base)
	}
}

// TestRecreateBase_FallsBackToRegistryWhenProvenanceUnreadable verifies the
// LEGACY fallback window: when the provenance read itself fails (e.g. a
// transient host I/O error, distinct from "no marker"), RecreateBase falls
// back to the registry rather than refusing outright — the fallback exists
// precisely so a real read failure degrades to yesterday's behavior instead
// of bricking recreate.
func TestRecreateBase_FallsBackToRegistryWhenProvenanceUnreadable(t *testing.T) {
	reg := registry.NewEmpty()
	if err := reg.Add(vm.CreateConfig{Name: "claude", BaseName: "registry-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	prov := newFakeProvenancer()
	prov.readErr = errors.New("host unreachable")

	base, ok := RecreateBase(reg, "claude", registry.LocalScope, prov)
	if !ok {
		t.Fatal("RecreateBase(provenance read error, registry managed) ok = false, want true")
	}
	if base != "registry-base" {
		t.Fatalf("RecreateBase(provenance read error) base = %q, want registry fallback %q", base, "registry-base")
	}
}
