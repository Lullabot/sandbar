package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lullabot/sandbar/internal/baseimage"
	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/vm"
)

func writeRevisionFixture(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLimaDesiredRevisionsFollowPublishedBaseAndSetupFiles(t *testing.T) {
	dir := t.TempDir()
	writeRevisionFixture(t, dir, "site.yml", "site")
	writeRevisionFixture(t, dir, "roles/project/tasks/main.yml", "project-v1")
	p := &limaProvider{prov: &provision.Provisioner{UsePublishedBase: true, PlaybookDir: dir}}
	cfg := vm.CreateConfig{Name: "web", BaseName: "base"}
	ctx := context.Background()
	marker := NewProvenance(cfg, false)
	marker.BaseRevision = baseimage.PinnedManifest.Version
	marker.SetupRevision = p.DesiredSetupRevision(ctx)
	if marker.SetupRevision == "" {
		t.Fatal("setup revision was not computed from configured playbook")
	}
	if got := CheckUpdates(ctx, p, marker); got.Base != UpdateCurrent || got.Setup != UpdateCurrent {
		t.Fatalf("newly built Lima VM = %+v, want both current", got)
	}
	writeRevisionFixture(t, dir, "roles/project/tasks/main.yml", "project-v2")
	if got := CheckUpdates(ctx, p, marker); got.Base != UpdateCurrent || got.Setup != UpdateAvailable {
		t.Fatalf("finalize-only edit = %+v, want setup update only", got)
	}
	marker.BaseRevision = "older-published-base"
	if got := CheckUpdates(ctx, p, marker); got.Base != UpdateAvailable || got.Setup != UpdateAvailable {
		t.Fatalf("old source and setup = %+v, want both updates", got)
	}
	remote := &remoteLimaProvider{limaProvider: p}
	if got := CheckUpdates(ctx, remote, marker); got.Base != UpdateAvailable || got.Setup != UpdateAvailable {
		t.Fatalf("remote Lima inherited different revision semantics: %+v", got)
	}
}

func TestLimaLegacyBaseRevisionAndUnreadableConfiguredFiles(t *testing.T) {
	dir := t.TempDir()
	writeRevisionFixture(t, dir, "site.yml", "legacy-base")
	p := &limaProvider{prov: &provision.Provisioner{PlaybookDir: dir}}
	cfg := vm.CreateConfig{Name: "web", BaseName: "base", WithGo: true}
	ctx := context.Background()
	base := p.DesiredBaseRevision(ctx, cfg)
	want, err := provision.PlaybookVersion(os.DirFS(dir), cfg.ToolsetKey())
	if err != nil {
		t.Fatal(err)
	}
	if base != want || base == baseimage.PinnedManifest.Version {
		t.Fatalf("legacy base revision = %q, want actual playbook/toolset %q", base, want)
	}
	changed := cfg
	changed.WithGo = false
	if p.DesiredBaseRevision(ctx, changed) == base {
		t.Fatal("tool selection did not change legacy base revision")
	}
	p.prov.PlaybookDir = filepath.Join(dir, "missing")
	marker := NewProvenance(cfg, false)
	marker.BaseRevision, marker.SetupRevision = base, "old-setup"
	if got := CheckUpdates(ctx, p, marker); got.Base != UpdateUnknown || got.Setup != UpdateUnknown {
		t.Fatalf("unreadable configured files claimed freshness: %+v", got)
	}
}

func TestProxmoxDesiredRevisionsUseImageIdentityAndEmbeddedSetup(t *testing.T) {
	image, err := baseimage.PinnedManifest.ForArch("amd64")
	if err != nil {
		t.Fatal(err)
	}
	p := &proxmoxProvider{imageURL: image.URL}
	ctx := context.Background()
	cfg := vm.CreateConfig{Name: "web", BaseName: "base"}
	marker := NewProvenance(cfg, false)
	marker.BaseRevision = p.DesiredBaseRevision(ctx, cfg)
	marker.SetupRevision = p.DesiredSetupRevision(ctx)
	if marker.BaseRevision == "" || marker.SetupRevision == "" {
		t.Fatalf("PVE desired revisions missing: base=%q setup=%q", marker.BaseRevision, marker.SetupRevision)
	}
	if got := CheckUpdates(ctx, p, marker); got.Base != UpdateCurrent || got.Setup != UpdateCurrent {
		t.Fatalf("new PVE VM = %+v, want both current", got)
	}
	p.imageURL = "https://example.invalid/new-image.qcow2"
	if got := CheckUpdates(ctx, p, marker); got.Base != UpdateAvailable || got.Setup != UpdateCurrent {
		t.Fatalf("changed PVE image = %+v, want base update only", got)
	}
	marker.BaseRevision = ""
	if got := CheckUpdates(ctx, p, marker); got.Base != UpdateUnknown || got.Setup != UpdateCurrent {
		t.Fatalf("legacy PVE VM with unknown source = %+v", got)
	}
}

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
