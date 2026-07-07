package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"
)

// TestRoundTrip exercises the custom persistence: add -> reload -> remove must
// survive across separate LoadFrom calls (i.e. it is actually written to disk),
// and the stored config (sizing/identity) must come back intact.
func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")

	r, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load (missing file): %v", err)
	}
	if r.IsManaged("claude") {
		t.Fatal("empty registry should not report claude as managed")
	}

	cfg := vm.CreateConfig{Name: "claude", BaseName: "claude-base", CPUs: 8, Memory: "32GiB", Hostname: "dev"}
	if err := r.Add(cfg); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Reload from disk: the entry, its base, and the stored config must persist.
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
	got, ok := r2.Config("claude")
	if !ok || got.CPUs != 8 || got.Memory != "32GiB" || got.Hostname != "dev" {
		t.Fatalf("stored config not restored: %+v (ok=%v)", got, ok)
	}

	if err := r2.Remove("claude"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	r3, _ := LoadFrom(path)
	if r3.IsManaged("claude") {
		t.Fatal("claude should be gone after remove")
	}
}

// TestTokenNeverPersisted: a clone token must never be written to the index.
func TestTokenNeverPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")
	r, _ := LoadFrom(path)
	if err := r.Add(vm.CreateConfig{Name: "claude", BaseName: "claude-base", CloneToken: "ghp_secret"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("index not written")
	}
	if containsStr(string(data), "ghp_secret") {
		t.Fatalf("clone token leaked into the index:\n%s", data)
	}
	cfg, _ := r.Config("claude")
	if cfg.CloneToken != "" {
		t.Fatalf("in-memory config retained the token: %q", cfg.CloneToken)
	}
}

// TestReconcilePrunesAbsent: entries for VMs no longer present must be dropped.
func TestReconcilePrunesAbsent(t *testing.T) {
	r := NewEmpty()
	_ = r.Add(vm.CreateConfig{Name: "claude", BaseName: "claude-base"})
	_ = r.Add(vm.CreateConfig{Name: "gone", BaseName: "claude-base"})

	changed, err := r.Reconcile(map[string]bool{"claude": true})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !changed {
		t.Fatal("reconcile should report a change when pruning")
	}
	if r.IsManaged("gone") {
		t.Fatal("absent VM should be pruned")
	}
	if !r.IsManaged("claude") {
		t.Fatal("present VM should be kept")
	}

	if changed, _ := r.Reconcile(map[string]bool{"claude": true}); changed {
		t.Fatal("second reconcile with no diff should report no change")
	}
}

func TestIsBase(t *testing.T) {
	r := NewEmpty()
	if r.IsBase("claude-base") {
		t.Error("empty registry: no base images recorded yet")
	}
	if r.IsBase("") {
		t.Error("empty name is never a base image")
	}

	_ = r.Add(vm.CreateConfig{Name: "claude", BaseName: "claude-base"})
	if !r.IsBase("claude-base") {
		t.Error("a recorded clone source should be a base image")
	}
	if r.IsBase("claude") {
		t.Error("the clone itself is not a base image")
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

// TestCorruptFileMovedAside: a corrupt index is reported AND preserved so a
// later save() cannot silently clobber it.
func TestCorruptFileMovedAside(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	r, err := LoadFrom(path)
	if err == nil {
		t.Fatal("corrupt index should return an error")
	}
	if r == nil || r.IsManaged("x") {
		t.Fatal("should still return a usable empty registry")
	}
	if _, statErr := os.Stat(path + ".corrupt"); statErr != nil {
		t.Fatalf("corrupt file should be preserved at %s.corrupt: %v", path, statErr)
	}
}

// TestMigrateLegacyIndex: a pre-rename claude-code-ansible index is migrated to
// the new sandbar location on Load() — the managed VM survives, and the old
// index file is removed once the copy is verified.
func TestMigrateLegacyIndex(t *testing.T) {
	base := t.TempDir()
	// Load() resolves the data dir from XDG_DATA_HOME via defaultPath().
	t.Setenv("XDG_DATA_HOME", base)

	// Seed a legacy index using the real writer so the on-disk JSON shape
	// matches production exactly.
	oldPath := filepath.Join(base, "claude-code-ansible", "managed-vms.json")
	seed, err := LoadFrom(oldPath)
	if err != nil {
		t.Fatalf("seed load: %v", err)
	}
	if err := seed.Add(vm.CreateConfig{Name: "claude", BaseName: "claude-base", CPUs: 8, Memory: "32GiB", Hostname: "dev"}); err != nil {
		t.Fatalf("seed add: %v", err)
	}

	// Load() migrates the legacy index before the first read.
	r, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !r.IsManaged("claude") {
		t.Fatal("migrated registry should report claude as managed")
	}

	// (a) the new index exists and parses with the VM present.
	newPath := filepath.Join(base, "sandbar", "managed-vms.json")
	if _, statErr := os.Stat(newPath); statErr != nil {
		t.Fatalf("new index should exist at %s: %v", newPath, statErr)
	}
	moved, err := LoadFrom(newPath)
	if err != nil {
		t.Fatalf("load migrated index: %v", err)
	}
	if !moved.IsManaged("claude") || moved.Base("claude") != "claude-base" {
		t.Fatalf("migrated index missing the VM: managed=%v base=%q", moved.IsManaged("claude"), moved.Base("claude"))
	}

	// (b) the old index file no longer exists.
	if _, statErr := os.Stat(oldPath); !os.IsNotExist(statErr) {
		t.Fatalf("old index should be removed, stat err = %v", statErr)
	}
}

func containsStr(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
