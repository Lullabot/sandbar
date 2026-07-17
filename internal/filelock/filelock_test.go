package filelock

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSequentialAcquireRelease exercises the happy path: two full
// acquire/release cycles against the same lock path must both succeed, and
// the second Acquire must not block forever behind the first (which was
// already released).
func TestSequentialAcquireRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.lock")

	release1, err := Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	release1()

	release2, err := Acquire(path)
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	release2()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file should exist after Acquire: %v", err)
	}
}

// TestAcquireDegradesOnUnwritableParent verifies the best-effort posture: if
// the lock file cannot be created (here, because its parent directory is not
// writable), Acquire must return a usable no-op release alongside a non-nil
// error rather than panicking or returning a nil release.
func TestAcquireDegradesOnUnwritableParent(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	// Best-effort restore so t.TempDir()'s own cleanup can remove it.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	path := filepath.Join(dir, "state.lock")

	release, err := Acquire(path)
	if err == nil {
		t.Fatal("expected an error acquiring a lock under an unwritable directory")
	}
	if release == nil {
		t.Fatal("release must never be nil, even on the degraded path")
	}

	// The no-op release must be safe to call, including more than once.
	release()
	release()
}
