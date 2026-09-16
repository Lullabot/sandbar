package lima

import (
	"path/filepath"
	"testing"
)

// TestLocalLockContention pins the three-way contract LockFile documents and
// internal/provision/baselock.go depends on: acquisition is (true, nil),
// CONTENTION IS (false, nil) -- not an error -- and a released lock can be
// taken again.
//
// The middle case is the one that earns the test. baselock.go treats a TryLock
// error as "give up on serializing, build unserialized", so an implementation
// that reported contention as an error would not fail loudly -- it would
// quietly let two concurrent `sand create` runs prepare the same base image at
// once. flock(2) reports contention as EWOULDBLOCK, and translating that to
// (false, nil) rather than letting it surface as an error is the whole job.
//
// It drives the exported seam rather than the syscall, so it pins the contract
// callers depend on rather than the implementation underneath. flock holds the
// lock per open file description, so two OpenLock calls contend within a single
// process and no subprocess is needed.
func TestLocalLockContention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "base.lock")
	hf := LocalFiles()

	first, err := hf.OpenLock(path, 0o600)
	if err != nil {
		t.Fatalf("OpenLock (first): %v", err)
	}
	defer first.Close()

	acquired, err := first.TryLock()
	if err != nil {
		t.Fatalf("first TryLock: unexpected error: %v", err)
	}
	if !acquired {
		t.Fatal("first TryLock: got acquired=false on an uncontended lock, want true")
	}

	second, err := hf.OpenLock(path, 0o600)
	if err != nil {
		t.Fatalf("OpenLock (second): %v", err)
	}
	defer second.Close()

	acquired, err = second.TryLock()
	if err != nil {
		t.Fatalf("second TryLock: contention must report (false, nil), got error: %v", err)
	}
	if acquired {
		t.Fatal("second TryLock: acquired a lock already held; base preparation would not be serialized")
	}

	// Close, not Unlock: release-on-close is the property OpenLock's callers
	// rely on, and it falls out of flock's per-descriptor semantics rather than
	// being arranged explicitly -- so it is worth pinning.
	if err := first.Close(); err != nil {
		t.Fatalf("closing the first lock: %v", err)
	}

	acquired, err = second.TryLock()
	if err != nil {
		t.Fatalf("second TryLock after release: %v", err)
	}
	if !acquired {
		t.Fatal("second TryLock after release: Close did not free the lock")
	}
	if err := second.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
}

// TestLocalLockUnlockThenReacquire proves Unlock releases without closing, so a
// LockFile can serve more than one acquire/release cycle over its lifetime.
func TestLocalLockUnlockThenReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "base.lock")
	hf := LocalFiles()

	lf, err := hf.OpenLock(path, 0o600)
	if err != nil {
		t.Fatalf("OpenLock: %v", err)
	}
	defer lf.Close()

	for i := range 2 {
		acquired, err := lf.TryLock()
		if err != nil {
			t.Fatalf("cycle %d: TryLock: %v", i, err)
		}
		if !acquired {
			t.Fatalf("cycle %d: TryLock returned false on a free lock", i)
		}
		if err := lf.Unlock(); err != nil {
			t.Fatalf("cycle %d: Unlock: %v", i, err)
		}
	}
}
