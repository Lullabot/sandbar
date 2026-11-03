package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"
)

func TestRevisionCaptureRecordsAndForwards(t *testing.T) {
	var forwarded []string
	onSource := func(v string) { forwarded = append(forwarded, "source="+v) }
	var onSetup func(string) // a caller that supplied no setup callback
	var rev revisionCapture
	rev.wrap(&onSource, &onSetup)
	onSource("base-1")
	onSetup("setup-1")
	if rev.source != "base-1" || rev.setup != "setup-1" {
		t.Fatalf("captured %+v, want base-1/setup-1", rev)
	}
	if len(forwarded) != 1 || forwarded[0] != "source=base-1" {
		t.Fatalf("caller's callback saw %v, want only its own source revision", forwarded)
	}
}

func TestLimaMarkCompletedWritesReadyRevisions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIMA_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	p := &limaProvider{hostFiles: lima.LocalFiles()}
	cfg := vm.CreateConfig{Name: "web", BaseName: "base"}
	if err := p.MarkManaged(context.Background(), cfg.Name, NewProvenance(cfg, true)); err != nil {
		t.Fatal(err)
	}
	p.markCompleted(context.Background(), cfg, "base-1", "setup-1")
	got, ok, err := p.ProvenanceOf(context.Background(), cfg.Name)
	if err != nil || !ok {
		t.Fatalf("marker after completion: ok=%v err=%v", ok, err)
	}
	if got.Provisioning || got.BaseRevision != "base-1" || got.SetupRevision != "setup-1" {
		t.Fatalf("completed marker = %+v, want ready with base-1/setup-1", got)
	}
}
