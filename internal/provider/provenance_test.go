package provider_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
