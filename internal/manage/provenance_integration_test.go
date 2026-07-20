package manage

// provenance_integration_test.go is the integration-level counterpart to
// manage_test.go's fakeProvenancer-based RecreateBase/RecordSuccess tests and
// registry/adopt_test.go's fake-AdoptProvenancer-based Adopt tests: it wires
// AdoptOnce, RecordSuccess and RecreateBase to a REAL provider.Provenancer —
// the local Lima provider's marker read/write over the real filesystem
// (LIMA_HOME pointed at a fresh temp dir per test), never an in-memory
// double — so adoption and recreate correctness are proven against the actual
// on-disk marker format, not just an algorithm exercised against a fake.
//
// No limactl subprocess and no SSH are invoked anywhere in this file:
// MarkManaged/ProvenanceOf/Unmark are pure file I/O through lima.HostFiles
// (see internal/provider/limaprovenance.go), so this runs in the default
// `go test ./...` — no build tag, no skip.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// realLocalProvenancer builds a real provider.Provenancer backed by the
// local filesystem under a fresh LIMA_HOME (t.TempDir(), via t.Setenv) — no
// fake, no limactl subprocess. The local Lima provider's Provenancer methods
// touch only its hostFiles handle (lima.LocalFiles()), never the lima core
// or provisioner, so passing nil for both is safe here — exactly the same
// construction internal/provider/provenance_test.go's asProvenancer helper
// uses.
func realLocalProvenancer(t *testing.T) provider.Provenancer {
	t.Helper()
	prov, _ := realLocalProvenancerIn(t)
	return prov
}

// realLocalProvenancerIn is realLocalProvenancer plus the LIMA_HOME it was
// pointed at, for a test that must seed instance directories in it.
func realLocalProvenancerIn(t *testing.T) (provider.Provenancer, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LIMA_HOME", home)
	p, ok := provider.NewLocalLima(nil, nil).(provider.Provenancer)
	if !ok {
		t.Fatal("local Lima provider does not satisfy provider.Provenancer")
	}
	return p, home
}

// seedInstance creates instance name's directory under home, standing in for
// what `limactl clone` does. MarkManaged refuses to mark an instance that does
// not exist — a marker write that created its own parent would leave a
// lima.yaml-less directory that makes every later `limactl list` fatal — so a
// test marking a VM must first have one.
func seedInstance(t *testing.T, home, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, name), 0o700); err != nil {
		t.Fatalf("seed instance dir %s: %v", name, err)
	}
}

// adoptIntegrationScope is a scope unique to this test file, so its
// AdoptOnce calls can never collide with adoptedTargets entries any other
// test in this package uses — AdoptOnce's "at most once per process per
// scope" gate is keyed by registry.Scope in a package-level map (adopt.go).
var adoptIntegrationScope = registry.Scope{Provider: "lima-remote", RemoteTarget: "adopt-integration-test@example.invalid:22"}

// TestAdoptOnceIntegration seeds a "legacy" registry entry (managed, but
// with no provenance marker — exactly what a pre-provenance sand recorded)
// and a live VM list naming it, then runs AdoptOnce against a REAL local
// Provenancer. It must write a marker sourced from the registry entry's own
// base/config, and a second call for the SAME scope must not touch the
// marker again — even though the registry now claims a DIFFERENT base —
// proving the second call is a true no-op (AdoptOnce's per-process gate,
// backstopped by Adopt's own "already marked" skip), not merely
// idempotent-by-accident.
func TestAdoptOnceIntegration(t *testing.T) {
	prov, home := realLocalProvenancerIn(t)
	seedInstance(t, home, "legacy-web")
	reg := registry.NewEmpty()
	cfg := vm.CreateConfig{Name: "legacy-web", BaseName: "sandbar-base", CPUs: 4}
	if err := reg.AddScoped(cfg, adoptIntegrationScope); err != nil {
		t.Fatalf("seed legacy registry entry: %v", err)
	}

	live := []vm.VM{{Name: "legacy-web", Status: "Running"}}
	ctx := context.Background()

	AdoptOnce(ctx, reg.ManagedInScope(adoptIntegrationScope), live, adoptIntegrationScope, prov)

	got, ok, err := prov.ProvenanceOf(ctx, "legacy-web")
	if err != nil {
		t.Fatalf("ProvenanceOf after AdoptOnce: %v", err)
	}
	if !ok {
		t.Fatal("AdoptOnce did not write a real marker for the legacy-managed VM")
	}
	if got.Base != "sandbar-base" {
		t.Fatalf("adopted marker Base = %q, want %q", got.Base, "sandbar-base")
	}

	// Mutate the registry's recorded base and call AdoptOnce again for the
	// SAME scope: this must be a total no-op, so the marker keeps its
	// ORIGINAL base rather than being re-derived from the updated entry.
	cfg2 := cfg
	cfg2.BaseName = "different-base"
	if err := reg.AddScoped(cfg2, adoptIntegrationScope); err != nil {
		t.Fatalf("update registry entry: %v", err)
	}
	AdoptOnce(ctx, reg.ManagedInScope(adoptIntegrationScope), live, adoptIntegrationScope, prov)

	still, ok, err := prov.ProvenanceOf(ctx, "legacy-web")
	if err != nil {
		t.Fatalf("ProvenanceOf after second AdoptOnce: %v", err)
	}
	if !ok {
		t.Fatal("marker vanished after the second AdoptOnce call")
	}
	if still.Base != "sandbar-base" {
		t.Fatalf("second AdoptOnce re-wrote the marker (Base = %q), want it untouched at %q (idempotent)", still.Base, "sandbar-base")
	}
}

// TestRecordSuccessWritesRealMarkerRecreateBaseReadsIt exercises the
// create -> recreate cycle exactly as cmd/sand/create.go and the TUI drive
// it, but against a REAL local Provenancer instead of manage_test.go's
// fakeProvenancer: RecordSuccess writes BOTH the registry entry and the
// marker (its documented "warm legacy cache + authoritative marker"
// contract — see manage.go), and RecreateBase resolves the clone source
// from the REAL marker it wrote.
func TestRecordSuccessWritesRealMarkerRecreateBaseReadsIt(t *testing.T) {
	prov, home := realLocalProvenancerIn(t)
	seedInstance(t, home, "claude")
	reg := registry.NewEmpty()
	cfg := vm.CreateConfig{Name: "claude", BaseName: "custom-base", CPUs: 4, Memory: "8GiB"}

	if err := RecordSuccess(reg, cfg, registry.LocalScope, prov); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}

	base, ok := RecreateBase(reg, cfg.Name, registry.LocalScope, prov)
	if !ok {
		t.Fatal("RecreateBase = not ok after RecordSuccess wrote a real marker, want ok")
	}
	if base != cfg.BaseName {
		t.Fatalf("RecreateBase base = %q, want the recorded base %q", base, cfg.BaseName)
	}
}

// TestRecreateBaseWithRealProvenancer_MarkerAloneDrivesItAndRefusalOnUnmark
// proves, against a REAL local Provenancer (no fake), the two ends of the
// marker-only resolution path RecreateBase's doc comment describes: with NO
// registry entry at all, a real marker (written directly via MarkManaged,
// mirroring a marker-based controller that never touched this machine's
// registry) is enough for RecreateBase to resolve the recorded base — and
// once that marker is removed (mirroring what `limactl delete`/Unmark
// does), with STILL no registry entry to fall back to, recreate is refused.
// manage_test.go's TestRecreateBase_ManagedViaMarker/_RefusedWhenNeither...
// prove the same shape against a fake; this is the real-filesystem
// round-trip counterpart.
func TestRecreateBaseWithRealProvenancer_MarkerAloneDrivesItAndRefusalOnUnmark(t *testing.T) {
	prov, home := realLocalProvenancerIn(t)
	reg := registry.NewEmpty() // deliberately empty throughout: provenance alone must drive this
	ctx := context.Background()
	const name = "claude"
	seedInstance(t, home, name)

	pv := provider.Provenance{SchemaVersion: 1, Base: "marker-base", Config: vm.CreateConfig{Name: name, BaseName: "marker-base"}}
	if err := prov.MarkManaged(ctx, name, pv); err != nil {
		t.Fatalf("MarkManaged: %v", err)
	}

	base, ok := RecreateBase(reg, name, registry.LocalScope, prov)
	if !ok {
		t.Fatal("RecreateBase = not ok with a real marker present (and no registry entry), want ok")
	}
	if base != "marker-base" {
		t.Fatalf("RecreateBase base = %q, want the marker's base %q", base, "marker-base")
	}

	if err := prov.Unmark(ctx, name); err != nil {
		t.Fatalf("Unmark: %v", err)
	}
	if base, ok := RecreateBase(reg, name, registry.LocalScope, prov); ok {
		t.Fatalf("RecreateBase after Unmark (and no registry entry) = ok (base=%q), want refused", base)
	}
}

// TestAdoptStampsTheCurrentMarkerSchema is the guard the two hand-maintained
// schema constants need. registry.adoptSchemaVersion is a package-local mirror
// of provider.MarkerSchemaVersion — duplicated rather than imported, to avoid an
// import cycle — and a mirror kept in step by a comment is a mirror that drifts
// the first time someone bumps one constant and not the other. A drifted
// adoption would stamp markers claiming a schema version that no longer
// describes their shape.
//
// This package imports both, so it is the natural place to compare them, and it
// compares them THROUGH a real adoption rather than by reading the constants:
// what matters is the version that actually lands in the marker.
func TestAdoptStampsTheCurrentMarkerSchema(t *testing.T) {
	prov, home := realLocalProvenancerIn(t)
	seedInstance(t, home, "schema-check")
	reg := registry.NewEmpty()
	scope := registry.Scope{Provider: "lima-remote", RemoteTarget: "adopt-schema-test@example.invalid:22"}
	if err := reg.AddScoped(vm.CreateConfig{Name: "schema-check", BaseName: "sandbar-base"}, scope); err != nil {
		t.Fatalf("seed registry entry: %v", err)
	}
	ctx := context.Background()

	AdoptOnce(ctx, reg.ManagedInScope(scope), []vm.VM{{Name: "schema-check", Status: "Running"}}, scope, prov)

	got, ok, err := prov.ProvenanceOf(ctx, "schema-check")
	if err != nil || !ok {
		t.Fatalf("ProvenanceOf after AdoptOnce = (ok=%v, err=%v), want a marker", ok, err)
	}
	if got.SchemaVersion != provider.MarkerSchemaVersion {
		t.Fatalf("adoption stamped schema %d, but this build writes %d — registry.adoptSchemaVersion has drifted from provider.MarkerSchemaVersion",
			got.SchemaVersion, provider.MarkerSchemaVersion)
	}
	// Adoption is always a READY marker: the VM it adopts finished building long
	// before this controller ever heard of it.
	if got.Provisioning {
		t.Error("adoption wrote an in-flight marker; an adopted VM is already built")
	}
	if got.Progress != (provider.BuildProgress{}) {
		t.Errorf("adoption wrote build progress %+v; an adopted VM has no build in flight", got.Progress)
	}
}
