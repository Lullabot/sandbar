package provider_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/vm"
)

// TestMarkerDDEVPortSlotCompatibility: the marker is what every controller of
// a host reads a VM's slot from, so the two decodings that matter are pinned
// against the real file path. A marker written by an earlier release (schema
// 4, no slot key) decodes with no slot and is otherwise unchanged — no schema
// bump, since the key is an additive one inside config — and a recorded slot
// 0 survives the write and both the single and the batched read.
func TestMarkerDDEVPortSlotCompatibility(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIMA_HOME", home)
	p := asProvenancer(t, newLocal(&fakeRunner{}))
	ctx := context.Background()

	if err := os.MkdirAll(filepath.Join(home, "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	prev := `{"schema":4,"base":"sandbar-base","config":{"Name":"web","BaseName":"sandbar-base","CPUs":4,"WithDDEV":true},"sandbar_version":"0.17.0","created_at":"2026-10-01T00:00:00Z","base_revision":"b1","setup_revision":"s1"}`
	if err := os.WriteFile(filepath.Join(home, "web", lima.MarkerFilename), []byte(prev), 0o600); err != nil {
		t.Fatal(err)
	}
	want := provider.Provenance{
		SchemaVersion:  4,
		Base:           "sandbar-base",
		Config:         vm.CreateConfig{Name: "web", BaseName: "sandbar-base", CPUs: 4, WithDDEV: true},
		SandbarVersion: "0.17.0",
		CreatedAt:      "2026-10-01T00:00:00Z",
		BaseRevision:   "b1",
		SetupRevision:  "s1",
	}
	got, ok, err := p.ProvenanceOf(ctx, "web")
	if err != nil || !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("previous-release marker = %+v (ok=%v, err=%v), want %+v", got, ok, err, want)
	}

	seedInstance(t, home, "api")
	zero := 0
	if err := p.MarkManaged(ctx, "api", provider.NewProvenance(vm.CreateConfig{Name: "api", BaseName: "sandbar-base", WithDDEV: true, DDEVPortSlot: &zero}, false)); err != nil {
		t.Fatalf("MarkManaged: %v", err)
	}
	all, err := p.Provenance(ctx)
	if err != nil {
		t.Fatalf("Provenance: %v", err)
	}
	if s := all["api"].Config.DDEVPortSlot; s == nil || *s != 0 {
		t.Fatalf("recorded slot 0 read back as %v, want 0", s)
	}
	if s := all["web"].Config.DDEVPortSlot; s != nil {
		t.Fatalf("unrecorded slot read back as %d, want none", *s)
	}
}
