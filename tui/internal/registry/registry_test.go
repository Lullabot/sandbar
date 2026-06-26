package registry

import (
	"path/filepath"
	"testing"
)

// TestRoundTrip exercises the custom persistence: add -> reload -> remove must
// survive across separate LoadFrom calls (i.e. it is actually written to disk).
func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")

	r, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load (missing file): %v", err)
	}
	if r.IsManaged("claude") {
		t.Fatal("empty registry should not report claude as managed")
	}

	if err := r.Add("claude", "claude-base"); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Reload from disk: the entry and its base must persist.
	r2, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !r2.IsManaged("claude") {
		t.Fatal("claude should be managed after reload")
	}
	if got := r2.Base("claude"); got != "claude-base" {
		t.Fatalf("base = %q, want claude-base", got)
	}

	if err := r2.Remove("claude"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	r3, _ := LoadFrom(path)
	if r3.IsManaged("claude") {
		t.Fatal("claude should be gone after remove")
	}
}

// TestMissingFileIsEmptyNotError: a first run with no index file is normal.
func TestMissingFileIsEmptyNotError(t *testing.T) {
	r, err := LoadFrom(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if r.IsManaged("anything") {
		t.Fatal("expected empty registry")
	}
}

// TestEmptyIsInMemory: NewEmpty never persists, so Add/Remove are safe no-ops
// against disk.
func TestEmptyIsInMemory(t *testing.T) {
	r := NewEmpty()
	if err := r.Add("x", "base"); err != nil {
		t.Fatalf("in-memory add should not error: %v", err)
	}
	if !r.IsManaged("x") {
		t.Fatal("in-memory add should be visible")
	}
}
