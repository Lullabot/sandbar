package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"
)

// TestConcurrentWritersDoNotLoseEntries is the regression test for a silent,
// long-standing data loss: two sand processes each hold their own in-memory
// copy of the index, and every save rewrote the WHOLE file from it. A TUI open
// since before a `sand create` therefore erased that create's entry the next
// time it saved anything at all — the VM stayed real, but sand no longer knew
// it was managed (no reset, and no tile on a managed-only board).
//
// Two *Registry values over ONE path are exactly those two processes: neither
// sees the other's write until it reloads, which is the whole point.
func TestConcurrentWritersDoNotLoseEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")

	tui, err := LoadFrom(path) // the long-running process, loaded while empty
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cli, err := LoadFrom(path) // a second process, e.g. `sand create`
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if err := cli.Add(vm.CreateConfig{Name: "created-elsewhere", BaseName: "sandbar-base"}); err != nil {
		t.Fatalf("add from the second process: %v", err)
	}
	// The first process now writes something of its own, from a map that has
	// never heard of the VM above.
	if err := tui.Add(vm.CreateConfig{Name: "created-here", BaseName: "sandbar-base"}); err != nil {
		t.Fatalf("add from the first process: %v", err)
	}

	back, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !back.IsManaged("created-elsewhere") {
		t.Error("the second process's VM was erased by the first process's save")
	}
	if !back.IsManaged("created-here") {
		t.Error("the first process's own VM is missing")
	}
	// The writing process is refreshed to the merged state, so it is never
	// behind the file it just wrote.
	if !tui.IsManaged("created-elsewhere") {
		t.Error("the writing process should see the merged state it just persisted")
	}
}

// TestReconcileKeepsAVMAddedByAnotherProcess pins the other half of that rule.
// Reconcile prunes entries whose VM is gone, and its evidence — a live instance
// listing — was taken before the call. A VM another process created since that
// listing is not in it, and must NOT be read as one that has disappeared:
// pruning it would delete the index entry moments after that process wrote it.
func TestReconcileKeepsAVMAddedByAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")

	tui, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := tui.Add(vm.CreateConfig{Name: "claude", BaseName: "sandbar-base"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := tui.Add(vm.CreateConfig{Name: "gone", BaseName: "sandbar-base"}); err != nil {
		t.Fatalf("add: %v", err)
	}

	cli, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := cli.Add(vm.CreateConfig{Name: "brand-new", BaseName: "sandbar-base"}); err != nil {
		t.Fatalf("add from the second process: %v", err)
	}

	// The first process reconciles against a listing taken before "brand-new"
	// existed: "gone" is genuinely deleted, "brand-new" is simply unknown to it.
	dropped, err := tui.Reconcile(map[string]bool{"claude": true})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(dropped) != 1 || dropped[0] != "gone" {
		t.Fatalf("reconcile should drop only the VM it knew had gone, got %v", dropped)
	}

	back, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !back.IsManaged("brand-new") {
		t.Error("a VM created by another process was pruned by a reconcile that had never heard of it")
	}
	if back.IsManaged("gone") {
		t.Error("the genuinely absent VM should have been pruned")
	}
}

// TestIndexLockFileLivesBesideTheIndex documents where the lock file appears —
// it is now a permanent entry in the data dir beside managed-vms.json, and both
// a reader browsing that directory and any test asserting on its contents
// should know what it is. It is also what makes the lock PER state file: the
// secrets store takes its own, so neither waits on the other.
func TestIndexLockFileLivesBesideTheIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")

	r, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := r.Add(vm.CreateConfig{Name: "claude", BaseName: "sandbar-base"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatalf("expected the advisory lock file beside the index: %v", err)
	}
}
