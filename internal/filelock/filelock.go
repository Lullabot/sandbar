// Package filelock provides a small, local-only, best-effort cross-process
// advisory file lock used to serialize read-modify-write updates to sand's
// host-side state stores (registry, secrets, profiles).
//
// It deliberately does not share code with internal/provision/baselock.go:
// baselock also has to work over SSH for remote Lima hosts, and it guards a
// multi-minute base-image build, so it polls TryLock(LOCK_EX|LOCK_NB) every
// 250ms while honoring a context so a cancelled build doesn't wait forever.
// This package guards a sub-millisecond local write to a small JSON/state
// file — always on the machine running the binary, never over SSH — so a
// simple bounded blocking LOCK_EX is sufficient and there is no context
// argument to plumb through the store packages that use it.
//
// Posture: best-effort. If the lock file cannot be created, opened, or
// acquired within the bounded deadline, Acquire returns a non-nil error
// together with a safe no-op release, so the caller proceeds UNSERIALIZED
// (today's behavior) rather than failing the underlying write. Acquire must
// never make the caller's operation impossible — a wedged or unwritable lock
// file is not a reason to refuse a write that would otherwise have succeeded.
//
// Non-re-entrant: a second Acquire of the same path from the same process
// opens a distinct file descriptor / open file description and will block
// (or, past the deadline, degrade to the no-op path) exactly as it would
// against a different process. Callers must acquire exactly once per
// operation and must never re-acquire the same path while already holding
// it.
package filelock

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

// acquireTimeout bounds how long Acquire will block trying to take the
// exclusive lock before degrading to the best-effort no-op path. The
// operations this package guards are sub-millisecond local writes, so a lock
// held for anywhere near this long indicates a wedged or abandoned holder,
// not ordinary contention.
const acquireTimeout = 5 * time.Second

// noop is the safe, always-non-nil release returned on every failure path.
func noop() {}

// Acquire takes an exclusive advisory lock on lockPath, creating the file
// (mode 0600) if it does not already exist, and returns a release function
// that unlocks and closes it.
//
// release is NEVER nil, on either the success or the failure path, so callers
// can `defer release()` unconditionally.
//
// On any failure — the file cannot be created/opened, or the lock cannot be
// taken within acquireTimeout — Acquire returns a non-nil error together with
// a no-op release. Callers must treat this as permission to proceed
// unserialized, not as a fatal error.
func Acquire(lockPath string) (release func(), err error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return noop, err
	}

	fd := int(f.Fd())

	done := make(chan error, 1)
	go func() {
		done <- syscall.Flock(fd, syscall.LOCK_EX)
	}()

	select {
	case lockErr := <-done:
		if lockErr != nil {
			_ = f.Close()
			return noop, lockErr
		}
		var once sync.Once
		return func() {
			once.Do(func() {
				_ = syscall.Flock(fd, syscall.LOCK_UN)
				_ = f.Close()
			})
		}, nil
	case <-time.After(acquireTimeout):
		// The goroutine above may still be blocked inside Flock; there is no
		// portable way to interrupt an in-flight flock(2) syscall, and closing fd
		// out from under it here would race the kernel's use of that descriptor
		// number for whatever it gets reallocated to next. So we hand fd off to a
		// cleanup goroutine that waits for Flock to eventually return and then
		// unlocks and closes it — the descriptor is held a little longer, not
		// leaked forever. This path is expected to be rare (a truly wedged
		// holder); the caller proceeds unserialized in the meantime.
		go func() {
			<-done
			_ = syscall.Flock(fd, syscall.LOCK_UN)
			_ = f.Close()
		}()
		return noop, fmt.Errorf("filelock: timed out acquiring lock on %s", lockPath)
	}
}
