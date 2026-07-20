package registry

import (
	"context"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"
)

// fakeAdoptProvenancer is an in-memory AdoptProvenancer test double. It
// records every MarkManaged call (so a test can assert Adopt wrote exactly
// once) and answers ProvenanceOf from the same map, so a marker Adopt just
// wrote is visible to the very next ProvenanceOf call — exactly like a real
// marker file would be.
type fakeAdoptProvenancer struct {
	markers map[string]AdoptProvenance
	marks   []string // names MarkManaged was called with, in call order
}

func newFakeAdoptProvenancer() *fakeAdoptProvenancer {
	return &fakeAdoptProvenancer{markers: map[string]AdoptProvenance{}}
}

func (f *fakeAdoptProvenancer) ProvenanceOf(_ context.Context, name string) (AdoptProvenance, bool, error) {
	p, ok := f.markers[name]
	return p, ok, nil
}

func (f *fakeAdoptProvenancer) MarkManaged(_ context.Context, name string, p AdoptProvenance) error {
	f.markers[name] = p
	f.marks = append(f.marks, name)
	return nil
}

// TestAdoptStampsLiveUnmarkedManagedEntryIdempotently seeds a registry with a
// managed entry that has no marker yet, and a fake Provenancer whose store
// starts empty and whose "live" set includes the name — mirroring an
// already-managed VM recorded by a pre-provenance sand. Running Adopt twice
// must write the marker exactly once (the second pass is a no-op because the
// first pass's own write now makes ProvenanceOf report ok=true).
func TestAdoptStampsLiveUnmarkedManagedEntryIdempotently(t *testing.T) {
	r := NewEmpty()
	cfg := vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}
	if err := r.AddScoped(cfg, LocalScope); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	prov := newFakeAdoptProvenancer()
	live := map[string]bool{"web": true}

	adopted, err := Adopt(context.Background(), r.ManagedInScope(LocalScope), live, prov)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if len(adopted) != 1 || adopted[0] != "web" {
		t.Fatalf("adopted = %v, want [web]", adopted)
	}
	if len(prov.marks) != 1 {
		t.Fatalf("MarkManaged called %d times, want 1", len(prov.marks))
	}
	if got := prov.markers["web"]; got.Base != "sandbar-base" {
		t.Fatalf("marker base = %q, want sandbar-base", got.Base)
	}

	// Second pass: idempotency. Must not write again.
	adopted2, err := Adopt(context.Background(), r.ManagedInScope(LocalScope), live, prov)
	if err != nil {
		t.Fatalf("second Adopt: %v", err)
	}
	if len(adopted2) != 0 {
		t.Fatalf("second Adopt adopted %v, want none", adopted2)
	}
	if len(prov.marks) != 1 {
		t.Fatalf("MarkManaged called %d times after second pass, want still 1", len(prov.marks))
	}
}

// TestAdoptSkipsAlreadyMarkedEntry proves Adopt never overwrites an existing
// marker: a VM that already carries provenance (e.g. because it was created
// by a provenance-aware sand, or a previous adoption already ran) must be
// left untouched.
func TestAdoptSkipsAlreadyMarkedEntry(t *testing.T) {
	r := NewEmpty()
	cfg := vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}
	if err := r.AddScoped(cfg, LocalScope); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	prov := newFakeAdoptProvenancer()
	preexisting := AdoptProvenance{
		SchemaVersion:  1,
		Base:           "custom-base",
		SandbarVersion: "9.9.9",
		CreatedAt:      "2020-01-01T00:00:00Z",
	}
	prov.markers["web"] = preexisting

	adopted, err := Adopt(context.Background(), r.ManagedInScope(LocalScope), map[string]bool{"web": true}, prov)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if len(adopted) != 0 {
		t.Fatalf("adopted = %v, want none (already marked)", adopted)
	}
	if len(prov.marks) != 0 {
		t.Fatalf("MarkManaged was called; want it never called for an already-marked instance")
	}
	if prov.markers["web"] != preexisting {
		t.Fatalf("existing marker was overwritten: got %+v, want %+v", prov.markers["web"], preexisting)
	}
}

// TestAdoptSkipsEntryAbsentFromLive proves Adopt never resurrects a managed
// entry whose VM no longer exists: a registry entry not present in live must
// not be marked, even though the registry still claims it.
func TestAdoptSkipsEntryAbsentFromLive(t *testing.T) {
	r := NewEmpty()
	cfg := vm.CreateConfig{Name: "ghost", BaseName: "sandbar-base"}
	if err := r.AddScoped(cfg, LocalScope); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	prov := newFakeAdoptProvenancer()

	adopted, err := Adopt(context.Background(), r.ManagedInScope(LocalScope), map[string]bool{}, prov)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if len(adopted) != 0 {
		t.Fatalf("adopted = %v, want none (not in live list)", adopted)
	}
	if len(prov.marks) != 0 {
		t.Fatalf("MarkManaged was called for a VM absent from live")
	}
}

// TestAdoptIgnoresNilProvenancer proves Adopt degrades to a no-op (rather
// than panicking) when no Provenancer is available — e.g. a backend that
// does not implement it.
func TestAdoptIgnoresNilProvenancer(t *testing.T) {
	r := NewEmpty()
	cfg := vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}
	if err := r.AddScoped(cfg, LocalScope); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	adopted, err := Adopt(context.Background(), r.ManagedInScope(LocalScope), map[string]bool{"web": true}, nil)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if len(adopted) != 0 {
		t.Fatalf("adopted = %v, want none (nil Provenancer)", adopted)
	}
}
