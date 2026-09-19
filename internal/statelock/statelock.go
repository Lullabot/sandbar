// Package statelock serializes writes to sand's host-side JSON state files —
// the managed-VM index (internal/registry) and the secrets store
// (internal/secrets) — across concurrent sand processes.
//
// Both files are rewritten WHOLE from an in-memory map. That is safe for one
// writer and quietly lossy for two: a long-running TUI holds the state it read
// at startup, a `sand create` in another terminal adds an entry, and the TUI's
// next save — a delete, a reconcile, a secret edit — writes its own older map
// over the top and the new VM is simply gone from the index (still a real VM,
// no longer sand-managed, no longer resettable). The atomic temp-file+rename
// each store already does makes every write land intact; it does nothing about
// which writer wins.
//
// The lock is a FILE lock, not a mutex, because the second writer is usually
// another PROCESS — which is also why it is per state file rather than global:
// the registry and the secrets store are written independently and have no
// reason to wait on each other. flock also conflicts between two descriptors in
// the same process, so one mechanism covers both.
//
// A failure to LOCK is not a failure to SAVE. If the lock file cannot be
// created (a read-only home, a filesystem with no flock), or another holder
// keeps it past the wait budget, Acquire returns a no-op release and the caller
// proceeds unserialized — exactly the posture internal/provision's base lock
// takes. Refusing to record a VM because a lock file could not be written would
// turn a concurrency guard into an outage.
//
// There is no non-unix build. sand drives Lima, which runs on Linux and macOS
// (the two .goreleaser.yaml ships), and syscall.Flock exists on both — the same
// reasoning internal/provision/baselock.go gives for not carrying a fallback.
package statelock

import (
	"os"
	"syscall"
	"time"
)

// pollInterval / waitBudget bound how long Acquire waits for another holder.
// The critical section is a small read-modify-write measured in milliseconds,
// so a holder still there after the budget is a stuck or crashed-but-somehow-
// still-open one, and blocking a user's create on it indefinitely would be
// worse than the lost update this exists to prevent.
//
// Vars, not consts, so the give-up path can be tested without a two-second
// test. Nothing in production writes them.
var (
	pollInterval = 5 * time.Millisecond
	waitBudget   = 2 * time.Second
)

// noop is the release returned whenever the caller proceeds unserialized.
func noop() {}

// Acquire takes the exclusive advisory lock for the state file at path (the
// lock lives beside it, at "<path>.lock") and returns the function that
// releases it. It never fails: an unlockable path yields a no-op release and
// the caller writes anyway. Call the returned function — via defer — exactly
// once.
func Acquire(path string) (release func()) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return noop
	}
	deadline := time.Now().Add(waitBudget)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}
		}
		if err != syscall.EWOULDBLOCK || time.Now().After(deadline) {
			_ = f.Close()
			return noop
		}
		time.Sleep(pollInterval)
	}
}
