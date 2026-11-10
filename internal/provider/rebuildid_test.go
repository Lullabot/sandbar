package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"
)

// A reset's replacement carries its RebuildID through the TUI's progress
// republishes, which rewrite the whole marker. Losing it there would make the
// replacement unrecognisable to a resume of the very reset that built it.
func TestLimaProgressKeepsTheRebuildID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIMA_HOME", home)
	p := &limaProvider{hostFiles: lima.LocalFiles()}
	ctx := context.Background()
	cfg := vm.CreateConfig{Name: "web", BaseName: "base"}
	if err := os.MkdirAll(filepath.Join(home, cfg.Name), 0o700); err != nil {
		t.Fatal(err)
	}

	rev := revisionCapture{source: "base-v1"}
	if err := resetInFlightMarker(p, cfg, &rev)(ctx, "abc123"); err != nil {
		t.Fatal(err)
	}
	progress := NewProvenance(cfg, true)
	progress.Progress = BuildProgress{Role: "project", Index: 3, Total: 10}
	if err := p.MarkProgress(ctx, cfg.Name, progress); err != nil {
		t.Fatal(err)
	}

	got, ok, err := p.ProvenanceOf(ctx, cfg.Name)
	if err != nil || !ok {
		t.Fatalf("marker: ok=%v err=%v", ok, err)
	}
	if got.RebuildID != "abc123" || !got.Provisioning || got.BaseRevision != "base-v1" {
		t.Errorf("after a progress update the marker is %+v; want it in flight with RebuildID abc123 and base-v1", got)
	}
	if got.SchemaVersion != MarkerSchemaVersion {
		t.Errorf("schema = %d, want %d", got.SchemaVersion, MarkerSchemaVersion)
	}
}
