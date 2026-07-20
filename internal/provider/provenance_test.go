package provider_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/vm"
)

// var _ provider.Provenancer = ... is the DoD's compile-time assertion for the
// local provider, restated at the package boundary the way local_test.go
// restates var _ provider.Provider = ... — NewLocalLima's own concrete type
// (limaProvider) is what local.go's in-package assertion already pins; this
// proves the SAME value, reached only through the exported constructor,
// satisfies Provenancer too.
var _ provider.Provenancer = provider.NewLocalLima(nil, nil).(provider.Provenancer)

// asProvenancer type-asserts p to provider.Provenancer, failing the test if the
// local provider ever stops satisfying it.
func asProvenancer(t *testing.T, p provider.Provider) provider.Provenancer {
	t.Helper()
	pv, ok := p.(provider.Provenancer)
	if !ok {
		t.Fatalf("provider %T does not satisfy provider.Provenancer", p)
	}
	return pv
}

// TestLimaProviderProvenanceRoundTrip proves MarkManaged/ProvenanceOf write and
// read the SAME marker through the local provider's HostFiles seam (the real
// local filesystem, pointed at a temp dir via LIMA_HOME so the test never
// touches a real ~/.lima). This is the write->read round trip the DoD calls
// for.
func TestLimaProviderProvenanceRoundTrip(t *testing.T) {
	t.Setenv("LIMA_HOME", t.TempDir())
	p := asProvenancer(t, newLocal(&fakeRunner{}))
	ctx := context.Background()

	want := provider.Provenance{
		SchemaVersion:  1,
		Base:           "base",
		Config:         vm.CreateConfig{Name: "web", BaseName: "base", CPUs: 4},
		SandbarVersion: "0.6.0",
		CreatedAt:      "2026-07-17T00:00:00Z",
	}
	if err := p.MarkManaged(ctx, "web", want); err != nil {
		t.Fatalf("MarkManaged: %v", err)
	}

	got, ok, err := p.ProvenanceOf(ctx, "web")
	if err != nil {
		t.Fatalf("ProvenanceOf: %v", err)
	}
	if !ok {
		t.Fatal("ProvenanceOf ok = false after MarkManaged, want true")
	}
	if got != want {
		t.Fatalf("ProvenanceOf = %+v, want %+v", got, want)
	}
}

// TestLimaProviderProvenanceOfUnmanaged proves an instance that was never
// marked reads back as (zero, false, nil) — "unmanaged", not an error.
func TestLimaProviderProvenanceOfUnmanaged(t *testing.T) {
	t.Setenv("LIMA_HOME", t.TempDir())
	p := asProvenancer(t, newLocal(&fakeRunner{}))

	got, ok, err := p.ProvenanceOf(context.Background(), "never-marked")
	if err != nil {
		t.Fatalf("ProvenanceOf: %v", err)
	}
	if ok {
		t.Fatalf("ProvenanceOf ok = true for an unmarked instance, want false (got %+v)", got)
	}
	if got != (provider.Provenance{}) {
		t.Fatalf("ProvenanceOf value = %+v, want the zero value", got)
	}
}

// TestLimaProviderProvenanceUnmark proves Unmark clears a marker: after
// MarkManaged then Unmark, ProvenanceOf reports the instance unmanaged again,
// and a second Unmark (nothing left to remove) is still not an error.
func TestLimaProviderProvenanceUnmark(t *testing.T) {
	t.Setenv("LIMA_HOME", t.TempDir())
	p := asProvenancer(t, newLocal(&fakeRunner{}))
	ctx := context.Background()

	if err := p.MarkManaged(ctx, "web", provider.Provenance{SchemaVersion: 1, Base: "base"}); err != nil {
		t.Fatalf("MarkManaged: %v", err)
	}
	if err := p.Unmark(ctx, "web"); err != nil {
		t.Fatalf("Unmark: %v", err)
	}
	if _, ok, err := p.ProvenanceOf(ctx, "web"); err != nil || ok {
		t.Fatalf("ProvenanceOf after Unmark = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
	// Unmarking an already-unmanaged instance must not error (RemoveAll's
	// "missing path is not an error" contract).
	if err := p.Unmark(ctx, "web"); err != nil {
		t.Fatalf("second Unmark (nothing to remove) errored: %v", err)
	}
}

// TestLimaProviderProvenanceBatchedRead proves Provenance() returns every
// marked instance under LIMA_HOME in one call, that an instance with NO
// marker is simply absent from the map, and — the DoD's "malformed marker
// skipped" requirement — that a marker whose content is not valid JSON is
// skipped rather than surfacing an error that would hide its (valid) peers.
func TestLimaProviderProvenanceBatchedRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIMA_HOME", home)
	p := asProvenancer(t, newLocal(&fakeRunner{}))
	ctx := context.Background()

	web := provider.Provenance{SchemaVersion: 1, Base: "base", Config: vm.CreateConfig{Name: "web"}}
	api := provider.Provenance{SchemaVersion: 1, Base: "base", Config: vm.CreateConfig{Name: "api"}}
	if err := p.MarkManaged(ctx, "web", web); err != nil {
		t.Fatalf("MarkManaged(web): %v", err)
	}
	if err := p.MarkManaged(ctx, "api", api); err != nil {
		t.Fatalf("MarkManaged(api): %v", err)
	}
	// A third instance directory with a deliberately malformed marker: valid
	// file, invalid JSON. It must not abort the batch or hide web/api.
	if err := os.MkdirAll(filepath.Join(home, "broken"), 0o700); err != nil {
		t.Fatalf("mkdir broken instance dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "broken", "sandbar.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write malformed marker: %v", err)
	}
	// A fourth instance directory with NO marker at all — must simply be
	// absent from the result, not an error.
	if err := os.MkdirAll(filepath.Join(home, "unmanaged"), 0o700); err != nil {
		t.Fatalf("mkdir unmanaged instance dir: %v", err)
	}

	got, err := p.Provenance(ctx)
	if err != nil {
		t.Fatalf("Provenance: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Provenance returned %d entries, want 2 (web, api): %+v", len(got), got)
	}
	if got["web"] != web {
		t.Fatalf("Provenance[web] = %+v, want %+v", got["web"], web)
	}
	if got["api"] != api {
		t.Fatalf("Provenance[api] = %+v, want %+v", got["api"], api)
	}
	if _, present := got["broken"]; present {
		t.Fatalf("Provenance included the malformed marker's instance: %+v", got)
	}
	if _, present := got["unmanaged"]; present {
		t.Fatalf("Provenance included an instance with no marker at all: %+v", got)
	}
}

// TestNewProvenance covers both marker shapes NewProvenance can produce: a
// provisional (in-flight) marker written at clone time, and a ready marker
// written on success. Both stamp the CURRENT MarkerSchemaVersion and strip
// CloneToken — the marker must never carry the secret used to clone it.
func TestNewProvenance(t *testing.T) {
	cases := []struct {
		name         string
		provisioning bool
	}{
		{"ready", false},
		{"provisioning", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := vm.CreateConfig{Name: "web", BaseName: "sandbar-base", CPUs: 4, CloneToken: "super-secret"}
			got := provider.NewProvenance(cfg, c.provisioning)

			if got.SchemaVersion != provider.MarkerSchemaVersion {
				t.Fatalf("SchemaVersion = %d, want %d (provider.MarkerSchemaVersion)", got.SchemaVersion, provider.MarkerSchemaVersion)
			}
			if got.Base != cfg.BaseName {
				t.Fatalf("Base = %q, want %q (cfg.BaseName)", got.Base, cfg.BaseName)
			}
			if got.Provisioning != c.provisioning {
				t.Fatalf("Provisioning = %v, want %v", got.Provisioning, c.provisioning)
			}
			if got.Config.CloneToken != "" {
				t.Fatalf("Config.CloneToken = %q, want stripped (empty) — the marker must never carry the clone secret", got.Config.CloneToken)
			}
		})
	}
}

// TestLimaProviderProvenanceRoundTripProvisioningFlag proves the Provisioning
// bit survives a real write/read/batched-read round trip through the local
// provider's HostFiles seam, exactly like
// TestLimaProviderProvenanceRoundTrip — just for both marker states rather
// than the schema-1 zero value.
func TestLimaProviderProvenanceRoundTripProvisioningFlag(t *testing.T) {
	t.Setenv("LIMA_HOME", t.TempDir())
	p := asProvenancer(t, newLocal(&fakeRunner{}))
	ctx := context.Background()

	building := provider.Provenance{SchemaVersion: provider.MarkerSchemaVersion, Base: "sandbar-base", Config: vm.CreateConfig{Name: "building"}, Provisioning: true}
	ready := provider.Provenance{SchemaVersion: provider.MarkerSchemaVersion, Base: "sandbar-base", Config: vm.CreateConfig{Name: "ready"}, Provisioning: false}

	if err := p.MarkManaged(ctx, "building", building); err != nil {
		t.Fatalf("MarkManaged(building): %v", err)
	}
	if err := p.MarkManaged(ctx, "ready", ready); err != nil {
		t.Fatalf("MarkManaged(ready): %v", err)
	}

	gotBuilding, ok, err := p.ProvenanceOf(ctx, "building")
	if err != nil || !ok {
		t.Fatalf("ProvenanceOf(building) = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	if !gotBuilding.Provisioning {
		t.Fatalf("ProvenanceOf(building).Provisioning = false, want true")
	}

	gotReady, ok, err := p.ProvenanceOf(ctx, "ready")
	if err != nil || !ok {
		t.Fatalf("ProvenanceOf(ready) = (ok=%v, err=%v), want (true, nil)", ok, err)
	}
	if gotReady.Provisioning {
		t.Fatalf("ProvenanceOf(ready).Provisioning = true, want false")
	}

	// Same check through the batched read.
	all, err := p.Provenance(ctx)
	if err != nil {
		t.Fatalf("Provenance: %v", err)
	}
	if !all["building"].Provisioning {
		t.Fatalf("Provenance()[building].Provisioning = false, want true")
	}
	if all["ready"].Provisioning {
		t.Fatalf("Provenance()[ready].Provisioning = true, want false")
	}
}

// TestLimaProviderProvenanceOfDecodesV1MarkerAsReady proves backward
// compatibility: a v1 marker on disk (written before Provisioning existed, so
// it has no "provisioning" key at all) decodes with Provisioning=false — every
// pre-existing marker on a user's machine reads back as "ready", never as a
// phantom in-flight build.
func TestLimaProviderProvenanceOfDecodesV1MarkerAsReady(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIMA_HOME", home)
	p := asProvenancer(t, newLocal(&fakeRunner{}))

	markerPath := filepath.Join(home, "web", lima.MarkerFilename)
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o700); err != nil {
		t.Fatalf("mkdir instance dir: %v", err)
	}
	rawV1 := `{"schema":1,"base":"sandbar-base","config":{"Name":"web","BaseName":"sandbar-base"},"sandbar_version":"0.5.0","created_at":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(markerPath, []byte(rawV1), 0o600); err != nil {
		t.Fatalf("write raw v1 marker: %v", err)
	}

	got, ok, err := p.ProvenanceOf(context.Background(), "web")
	if err != nil {
		t.Fatalf("ProvenanceOf: %v", err)
	}
	if !ok {
		t.Fatal("ProvenanceOf ok = false for a v1 marker, want true")
	}
	if got.Provisioning {
		t.Fatalf("a v1 marker (no provisioning key) decoded with Provisioning=true, want false (ready)")
	}
	if got.SchemaVersion != 1 {
		t.Fatalf("SchemaVersion = %d, want 1 (the marker as written, not migrated)", got.SchemaVersion)
	}
}

// TestProvenanceProgressRoundTripAndV2Compat covers the v3 addition from both
// directions: a marker carrying build progress survives the write/read cycle
// intact, and a v2 marker — one written by any sand built before progress
// existed — decodes with a zero BuildProgress rather than failing. The latter is
// what keeps a mixed-version fleet working: an older controller's in-flight
// marker still says "building", it just cannot say how far.
func TestProvenanceProgressRoundTripAndV2Compat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIMA_HOME", home)
	p := asProvenancer(t, newLocal(&fakeRunner{}))
	ctx := context.Background()

	want := provider.NewProvenance(vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}, true)
	want.Progress = provider.BuildProgress{Role: "claude-code", Index: 30, Total: 120}
	if err := p.MarkManaged(ctx, "web", want); err != nil {
		t.Fatalf("MarkManaged: %v", err)
	}
	got, ok, err := p.ProvenanceOf(ctx, "web")
	if err != nil || !ok {
		t.Fatalf("ProvenanceOf = (ok=%v, err=%v), want a marker", ok, err)
	}
	if got.Progress != want.Progress {
		t.Fatalf("Progress = %+v, want %+v", got.Progress, want.Progress)
	}
	if got != want {
		t.Fatalf("marker did not round-trip:\n got %+v\nwant %+v", got, want)
	}

	// A READY marker must not carry a progress key at all — that is what
	// `omitzero` buys, and it keeps the common marker (every finished VM) free of
	// a meaningless `"progress":{}`.
	ready := provider.NewProvenance(vm.CreateConfig{Name: "done", BaseName: "sandbar-base"}, false)
	if err := p.MarkManaged(ctx, "done", ready); err != nil {
		t.Fatalf("MarkManaged(ready): %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "done", lima.MarkerFilename))
	if err != nil {
		t.Fatalf("read ready marker: %v", err)
	}
	if strings.Contains(string(raw), "progress") {
		t.Errorf("a ready marker carries a progress key:\n%s", raw)
	}

	// A v2 marker (in-flight, but from before progress existed) still decodes.
	markerPath := filepath.Join(home, "old", lima.MarkerFilename)
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o700); err != nil {
		t.Fatalf("mkdir instance dir: %v", err)
	}
	rawV2 := `{"schema":2,"base":"sandbar-base","config":{"Name":"old"},"sandbar_version":"0.6.0","created_at":"2026-01-01T00:00:00Z","provisioning":true}`
	if err := os.WriteFile(markerPath, []byte(rawV2), 0o600); err != nil {
		t.Fatalf("write raw v2 marker: %v", err)
	}
	oldGot, ok, err := p.ProvenanceOf(ctx, "old")
	if err != nil || !ok {
		t.Fatalf("ProvenanceOf(v2) = (ok=%v, err=%v), want a marker", ok, err)
	}
	if !oldGot.Provisioning {
		t.Error("a v2 in-flight marker lost its Provisioning flag")
	}
	if oldGot.Progress != (provider.BuildProgress{}) {
		t.Errorf("a v2 marker decoded with progress %+v, want the zero value", oldGot.Progress)
	}
}
