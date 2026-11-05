package provision

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"
)

// TestResetManifestDDEVPortSlot: `sandbar reset --from-backup` takes the VM's
// whole configuration from reset.json, so that file is how a resumed reset
// keeps the VM's ddev ports. A manifest written before slots existed must
// still resume, with no slot and nothing else changed; a recorded slot 0 must
// come back as slot 0, not as "no slot".
func TestResetManifestDDEVPortSlot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, homeArchive), []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := `{"schema":1,"config":{"Name":"web","BaseName":"sandbar-base","CPUs":4,"WithDDEV":true},"plan":{"WholeHome":true}}`
	if err := os.WriteFile(filepath.Join(dir, resetManifestName), []byte(prev), 0o600); err != nil {
		t.Fatal(err)
	}
	_, m, legacy, err := ResumeStage(dir)
	if err != nil || legacy {
		t.Fatalf("ResumeStage(previous-release manifest) = legacy %v, err %v", legacy, err)
	}
	want := vm.CreateConfig{Name: "web", BaseName: "sandbar-base", CPUs: 4, WithDDEV: true}
	if !reflect.DeepEqual(m.Config, want) {
		t.Fatalf("previous-release manifest config = %+v, want %+v with no slot", m.Config, want)
	}

	zero := 0
	cfg := want
	cfg.DDEVPortSlot = &zero
	g := &StageGuard{dir: dir}
	if err := g.WriteManifest(NewResetManifest(cfg, PreservePlan{WholeHome: true}, ResetOptions{})); err != nil {
		t.Fatal(err)
	}
	_, m, _, err = ResumeStage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s := m.Config.DDEVPortSlot; s == nil || *s != 0 {
		t.Fatalf("recorded slot 0 resumed as %v, want 0", s)
	}
}
