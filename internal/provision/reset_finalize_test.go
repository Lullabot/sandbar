package provision

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
)

// A resume carries no preserve options — the backup's plan says what it holds
// — so finalize must learn from the plan that a whole home came back.
// Otherwise the home-file policy treats a restored home as a fresh one.
func TestReset_RestoreFromTellsFinalizeAHomeCameBack(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	cfg := testConfig()
	m := NewResetManifest(cfg, PreservePlan{WholeHome: true}, ResetOptions{})
	dir := writeBackup(t, &m, homeArchive)

	if err := p.Reset(context.Background(), cfg, ResetOptions{RestoreFrom: dir}, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if fin := finalizeStream(t, f.streams); !strings.Contains(fin, "sand_preserved_home: true") {
		t.Errorf("finalize was not told the home was restored:\n%s", fin)
	}
}
