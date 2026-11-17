package registry

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"
)

// TestMutateRefusesFileUpgradedUnderIt is a long-running TUI from an older
// sandbar whose index is rewritten by a newer one mid-session. Its next write
// must refuse rather than downgrade the file to its own stale copy.
func TestMutateRefusesFileUpgradedUnderIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")
	r, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Add(vm.CreateConfig{Name: "old", BaseName: "sandbar-base"}); err != nil {
		t.Fatal(err)
	}
	newer := []byte(`{"version":99,"vms":[{"name":"new","provider":"lima"}],"future":true}`)
	if err := os.WriteFile(path, newer, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(vm.CreateConfig{Name: "another", BaseName: "sandbar-base"}); err == nil {
		t.Fatal("Add over a newer-schema file: want an error, got nil")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newer) {
		t.Fatalf("newer-schema index was rewritten:\n got %s\nwant %s", got, newer)
	}
}
