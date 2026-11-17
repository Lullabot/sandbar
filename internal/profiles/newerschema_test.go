package profiles

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestNewerSchemaFileIsNeitherQuarantinedNorSavedOver covers both halves: a
// load refuses a file from a newer sandbar without moving or rewriting it, and
// a store that loaded an older file refuses to save once a newer sandbar has
// rewritten it, rather than dropping fields it does not know.
func TestNewerSchemaFileIsNeitherQuarantinedNorSavedOver(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.yaml")
	newer := []byte("version: 99\nprofiles:\n  - id: local\n    name: Local\n    type: local\n    enabled: true\n    future_field: x\n")

	s, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(Profile{Name: "r1", Type: TypeRemoteSSH, Enabled: true, Host: "h", User: "u", Port: 22}); err == nil {
		t.Fatal("Add over a newer-schema file: want an error, got nil")
	}

	if _, err := LoadFrom(path); err == nil {
		t.Fatal("LoadFrom of a newer-schema file: want an error")
	}
	if _, err := os.Stat(path + ".corrupt"); err == nil {
		t.Fatal("newer-schema file was quarantined as corrupt")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newer) {
		t.Fatalf("newer-schema file was rewritten:\n got %s\nwant %s", got, newer)
	}
}
