package provision

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLockBase_CtxCancelWhileWaitingReturnsPromptly drives the ctx.Done() arm
// (baselock.go ~109): a waiter queued behind a lock someone else holds must
// return ctx.Err() as soon as its context is cancelled, rather than sitting
// out the full baseLockPoll loop or — worse — waiting for the holder to
// release. This is what makes ctrl+c on a queued build responsive (see
// lockBase's doc comment).
//
// This takes the REAL flock, exactly as TestRebuildDeletesTheBaseOnlyWhileHoldingTheBaseLock
// does elsewhere in this package: the first lockBase call genuinely holds the
// file lock, so the second call genuinely blocks on it, and cancellation is
// the only thing that gets the second call out.
func TestLockBase_CtxCancelWhileWaitingReturnsPromptly(t *testing.T) {
	t.Setenv("LIMA_HOME", t.TempDir())

	holderRelease, err := lockBase(context.Background(), "claude-base", &bytes.Buffer{})
	if err != nil {
		t.Fatalf("lockBase (holder): %v", err)
	}
	defer holderRelease()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	release, err := lockBase(ctx, "claude-base", &bytes.Buffer{})
	elapsed := time.Since(start)

	if err != context.Canceled {
		t.Fatalf("lockBase (waiter) err = %v, want context.Canceled", err)
	}
	if release != nil {
		t.Errorf("lockBase (waiter) returned a non-nil release alongside ctx.Err(); want nil (nothing to release)")
	}
	if elapsed >= time.Second {
		t.Errorf("lockBase (waiter) took %v to return after ctx cancellation, want well under 1s", elapsed)
	}
}

// TestLockBase_MkdirAllFailure_DegradesToNoOpRelease drives the MkdirAll
// failure arm (baselock.go ~66). The lock directory's path is blocked by a
// REGULAR FILE occupying the "_sand" path component MkdirAll needs to create
// as a directory: os.MkdirAll's internal Stat sees a non-directory there and
// fails with ENOTDIR. This is filesystem state, not a permission check, so it
// fails identically for root and non-root — no os.Geteuid() guard needed.
//
// A failure to lock must not be a failure to build: the caller gets back a
// harmless no-op release and a nil error, so create proceeds unserialized
// (see lockBase's doc comment on this posture).
func TestLockBase_MkdirAllFailure_DegradesToNoOpRelease(t *testing.T) {
	limaHomeDir := t.TempDir()
	t.Setenv("LIMA_HOME", limaHomeDir)

	// baseLockPath's directory (limaHome/_sand) is exactly what lockBase's
	// MkdirAll needs to create; occupying that path with a file blocks it.
	blocker := filepath.Join(limaHomeDir, "_sand")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}

	var out bytes.Buffer
	release, err := lockBase(context.Background(), "claude-base", &out)
	if err != nil {
		t.Fatalf("lockBase err = %v, want nil (a lock failure degrades, it does not fail the caller)", err)
	}
	if release == nil {
		t.Fatal("lockBase returned a nil release alongside a nil error; want a usable no-op release")
	}
	release() // must be safe to call and must not panic

	if !strings.Contains(out.String(), "could not create the base-image lock directory") {
		t.Errorf("output = %q, want a note about failing to create the lock directory", out.String())
	}
}

// TestLockBase_OpenFileFailure_DegradesToNoOpRelease drives the OpenFile
// failure arm (baselock.go ~70). The lock FILE's own path is occupied by a
// DIRECTORY: MkdirAll(_sand) succeeds trivially (it already exists), but
// os.OpenFile(path, O_CREATE|O_RDWR, ...) on a path that is a directory fails
// with EISDIR. Again filesystem state, not permissions — no euid guard
// needed.
func TestLockBase_OpenFileFailure_DegradesToNoOpRelease(t *testing.T) {
	limaHomeDir := t.TempDir()
	t.Setenv("LIMA_HOME", limaHomeDir)

	// Pre-create the lock file's own path AS A DIRECTORY (this also creates
	// its parent "_sand" directory, so the MkdirAll arm above is not the one
	// that fires here).
	lockPath := baseLockPath("claude-base")
	if err := os.MkdirAll(lockPath, 0o755); err != nil {
		t.Fatalf("mkdir lock path as a directory: %v", err)
	}

	var out bytes.Buffer
	release, err := lockBase(context.Background(), "claude-base", &out)
	if err != nil {
		t.Fatalf("lockBase err = %v, want nil (a lock failure degrades, it does not fail the caller)", err)
	}
	if release == nil {
		t.Fatal("lockBase returned a nil release alongside a nil error; want a usable no-op release")
	}
	release() // must be safe to call and must not panic

	if !strings.Contains(out.String(), "could not open the base-image lock") {
		t.Errorf("output = %q, want a note about failing to open the lock file", out.String())
	}
}

// The third degrade arm baselock.go documents — a non-EWOULDBLOCK error from
// syscall.Flock itself (baselock.go ~85, after a successful open) — is not
// exercised here. On Linux, flock(2) against any local filesystem that does
// not define its own f_op->flock (every filesystem reachable in a sandboxed
// test: tmpfs, ext4, procfs, devtmpfs, FIFOs, sockets, char/block devices)
// falls through to the kernel's generic in-memory implementation, which can
// only ever return success or EWOULDBLOCK for a LOCK_NB request — verified
// empirically here (regular files, directories, FIFOs, /dev/null, /dev/urandom,
// /dev/ptmx, procfs files, and device nodes all either fail at open() or
// succeed at flock(), never fail at flock() with anything but EWOULDBLOCK).
// Only filesystems with a custom ->flock implementation (NFS, CIFS) can
// return a different error, and standing up a real network filesystem server
// is out of scope for a portable, deterministic unit test.
