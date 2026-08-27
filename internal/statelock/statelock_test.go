package statelock

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestAcquireExcludesASecondHolder is the property the whole package exists
// for: while one holder has the lock, nobody else takes it. flock conflicts
// between two descriptors even in the SAME process, which is what lets this be
// tested without spawning a second sand.
func TestAcquireExcludesASecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-vms.json")

	release := Acquire(path)
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatalf("the lock file should sit beside the state file: %v", err)
	}

	// A second descriptor must not get the lock while the first holds it.
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != syscall.EWOULDBLOCK {
		t.Fatalf("a second holder took the lock (flock err = %v), so writes are not serialized", err)
	}

	// After the release it is free again.
	release()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// TestAcquireGivesUpRatherThanHanging: a failure to LOCK is not a failure to
// WRITE. A holder still there past the budget (a stuck or crashed-but-open
// process) must not wedge a user's create forever — the caller is told nothing
// and proceeds unserialized, which is the same posture the base-image lock
// takes.
func TestAcquireGivesUpRatherThanHanging(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")

	held := Acquire(path)
	defer held()

	// Shrink the budget so the give-up path costs milliseconds, not seconds.
	defer func(p, w time.Duration) { pollInterval, waitBudget = p, w }(pollInterval, waitBudget)
	pollInterval, waitBudget = time.Millisecond, 20*time.Millisecond

	done := make(chan func(), 1)
	go func() { done <- Acquire(path) }()

	select {
	case release := <-done:
		release() // must be safe to call: it is the no-op release
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire blocked on a held lock instead of giving up")
	}
}

// TestAcquireOnAnUnlockablePathProceeds: an unwritable location (a read-only
// home, a directory that is not there) yields a usable no-op release rather
// than a panic or a refusal — the write itself still has to be allowed to
// happen.
func TestAcquireOnAnUnlockablePathProceeds(t *testing.T) {
	release := Acquire(filepath.Join(t.TempDir(), "no-such-dir", "state.json"))
	if release == nil {
		t.Fatal("Acquire must always return a callable release")
	}
	release()
}
