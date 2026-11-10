package manage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/provider"
)

// markers is a Provenancer holding one marker per name.
type markers map[string]provider.Provenance

func (m markers) Provenance(context.Context) (map[string]provider.Provenance, error) { return m, nil }
func (m markers) ProvenanceOf(_ context.Context, name string) (provider.Provenance, bool, error) {
	p, ok := m[name]
	return p, ok, nil
}
func (m markers) MarkManaged(_ context.Context, name string, p provider.Provenance) error {
	m[name] = p
	return nil
}
func (m markers) Unmark(_ context.Context, name string) error {
	delete(m, name)
	return nil
}

// brokenMarkers cannot read a marker at all.
type brokenMarkers struct{ markers }

func (brokenMarkers) ProvenanceOf(context.Context, string) (provider.Provenance, bool, error) {
	return provider.Provenance{}, false, errors.New("read sandbar.json: i/o error")
}

// backupWithRebuild is a staging directory whose reset got as far as cloning
// a replacement carrying id.
func backupWithRebuild(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	if id != "" {
		rec := `{"rebuild_id":"` + id + `","name":"web"}`
		if err := os.WriteFile(filepath.Join(dir, "rebuild.json"), []byte(rec), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestOwnRebuild(t *testing.T) {
	ctx := context.Background()
	withID := backupWithRebuild(t, "abc123")
	cases := []struct {
		name string
		dir  string
		prov provider.Provenancer
		want bool
	}{
		{"matching marker", withID, markers{"web": {RebuildID: "abc123"}}, true},
		{"healed to ready keeps the id", withID, markers{"web": provider.Provenance{RebuildID: "abc123", Provisioning: true}.Ready()}, true},
		{"different id", withID, markers{"web": {RebuildID: "zzz"}}, false},
		{"no marker", withID, markers{}, false},
		{"no rebuild record", backupWithRebuild(t, ""), markers{"web": {}}, false},
		{"no provenancer", withID, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := OwnRebuild(ctx, tc.prov, tc.dir, "web")
			if err != nil || got != tc.want {
				t.Errorf("OwnRebuild = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

// Failing to establish ownership is an error and never a claim: a record or a
// marker that cannot be read must not let a VM be treated as disposable.
func TestOwnRebuildUnreadableEvidence(t *testing.T) {
	ctx := context.Background()
	if own, err := OwnRebuild(ctx, brokenMarkers{}, backupWithRebuild(t, "abc123"), "web"); own || err == nil {
		t.Errorf("unreadable marker: own=%v err=%v, want an error and no claim", own, err)
	}
	corrupt := t.TempDir()
	if err := os.WriteFile(filepath.Join(corrupt, "rebuild.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if own, err := OwnRebuild(ctx, markers{}, corrupt, "web"); own || err == nil {
		t.Errorf("corrupt record: own=%v err=%v, want an error and no claim", own, err)
	}
}

type failingDelete struct{}

func (failingDelete) Delete(string, bool) error { return errors.New("permission denied") }

func TestRemoveOwnRebuildReportsAFailedDelete(t *testing.T) {
	if err := RemoveOwnRebuild(failingDelete{}, "web", io.Discard); err == nil || !strings.Contains(err.Error(), "half-built") {
		t.Errorf("err = %v, want it to name the half-built VM", err)
	}
}
