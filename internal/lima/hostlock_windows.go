//go:build windows

package lima

import (
	"errors"

	"golang.org/x/sys/windows"
)

// Windows has no flock(2). LockFileEx is the equivalent: it locks a byte RANGE
// rather than the whole file, so by convention we lock a single byte at offset
// 0 -- the range never has to correspond to real content, and every sand
// process agrees on it. LOCKFILE_FAIL_IMMEDIATELY is the LOCK_NB analogue, and
// the lock is released when the handle closes, matching flock's behaviour and
// localLock.Close's contract.
//
// Contention reports ERROR_LOCK_VIOLATION here where unix reports EWOULDBLOCK;
// both mean "held by someone else", which TryLock must render as (false, nil)
// rather than an error so callers wait and retry instead of aborting.

// lockOneByte is the single-byte range every sand process locks. Low and high
// halves of the length are passed separately to LockFileEx.
const lockOneByte = 1

func (l *localLock) TryLock() (bool, error) {
	var ol windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(l.f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, lockOneByte, 0, &ol,
	)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil // held by someone else — wait and retry
	}
	return false, err
}

func (l *localLock) Unlock() error {
	var ol windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, lockOneByte, 0, &ol)
}
