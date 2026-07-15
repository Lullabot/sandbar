package provision

// baselock.go serializes preparation of THE BASE IMAGE, which every VM is cloned
// from and which therefore every create and reset has to go through.
//
// ensureBaseStopped reads the base's status and then acts on it — build it if it is
// missing, stop it if it is running, delete and rebuild it if it is stale — with
// nothing between the read and the act. That was safe exactly as long as only one
// provision could exist at a time, which is what the old full-screen progress view
// enforced by freezing the keyboard for the duration of a build. The board removed
// that freeze on purpose: concurrent builds are the headline feature. So two creates
// now race, and they race over one shared, expensive, minutes-long resource:
//
//   - Both see the base missing and BOTH BUILD IT, under the same instance name.
//   - Worse, a base that is mid-build is `Running` to Lima (a booted guest with
//     Ansible inside it), so the second create takes the "not Stopped" branch and
//     STOPS THE BASE OUT FROM UNDER the first one's build, killing it.
//   - And the stale-base path can DELETE the base while another create is cloning
//     from it.
//
// That last one is why every destructive step on the base — the from-scratch build,
// the in-place re-apply (which RUNS the base while a clone would be reading its
// disk), and `sand create --rebuild`'s destroy — lives inside ensureBaseStopped,
// which prepareBaseAndClone calls with this lock held, and which holds it through
// the clone as well. --rebuild used to delete the base up in the CLI (cmd/sand/
// create.go), before the provisioner and therefore before this lock existed for
// that run: the doc above claimed a guarantee the code did not give. Nothing may
// destroy the base outside this lock. There is no exception, and a caller that
// thinks it has one is the race.
//
// The lock is a FILE lock, not a mutex, because the second racer is not always in
// this process: `sand create` (headless, cmd/sand/create.go) builds its own
// Provisioner and can run in another terminal while the TUI is building. A mutex
// would serialize the TUI against itself and leave the interesting case open. flock
// also conflicts between two file descriptors in the SAME process, so one mechanism
// covers both.
//
// There is no non-unix build of this. sand drives Lima, Lima runs on Linux and macOS
// (.goreleaser.yaml ships exactly those two), and syscall.Flock exists on both. A
// `//go:build unix` tag with a no-op fallback beside it would be machinery kept alive
// for a platform that cannot run sand at all.
//
// It is deliberately a lock and not a queue. A queue would need to model priority,
// cancellation and fairness for something whose entire contract is "there is one
// base image, and only one of you may be preparing it". The waiter blocks, says so,
// and proceeds the moment the base is ready — which is the same thing a queue of
// depth one would do, with none of the machinery.

import (
	"context"
	"io"
	"path/filepath"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
)

// baseLockPoll is how often a waiter re-tries the lock. The thing it is waiting for
// takes minutes; a quarter-second poll is free and keeps cancellation responsive.
const baseLockPoll = 250 * time.Millisecond

// lockBase takes the exclusive base-image lock, blocking until it is free, and
// returns the function that releases it.
//
// It honours ctx: a user who hits ctrl+c on a build that is queued behind someone
// else's base build gets out immediately, rather than being stuck waiting on a lock
// for a job they have already cancelled.
//
// A failure to LOCK is not a failure to BUILD. If the lock file cannot be created at
// all (a read-only home, a permissions problem), this reports the reason and lets the
// caller proceed unserialized — the same posture the registry and the secrets store
// take. Refusing to create a VM because a lock file could not be written would turn a
// concurrency guard into an outage.
func lockBase(ctx context.Context, hf lima.HostFiles, baseName string, out io.Writer) (release func(), err error) {
	// The open+flock go through the host-access handle (hf) so a remote-Lima
	// provider serializes on the host that owns the base image, not on the laptop.
	// The two failure notes stay distinct — directory vs file — because the tests
	// (and a user diagnosing a wedged create) need to know which step gave way.
	path := baseLockPath(hf, baseName)
	if err := hf.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		step(out, "Note: could not create the base-image lock directory (%v); continuing without it.", err)
		return func() {}, nil
	}
	lf, err := hf.OpenLock(path, 0o600)
	if err != nil {
		step(out, "Note: could not open the base-image lock (%v); continuing without it.", err)
		return func() {}, nil
	}

	waited := false
	for {
		acquired, err := lf.TryLock()
		if err != nil {
			_ = lf.Close()
			step(out, "Note: could not lock the base image (%v); continuing without it.", err)
			return func() {}, nil
		}
		if acquired {
			return func() {
				_ = lf.Unlock()
				_ = lf.Close()
			}, nil
		}

		// Someone else is preparing the base. Say so ONCE — this is a wait of minutes,
		// and a silent one looks like a hang.
		if !waited {
			waited = true
			step(out, "Waiting for base image %q — another VM is building it. This VM will clone it as soon as it is ready.", baseName)
		}

		select {
		case <-ctx.Done():
			_ = lf.Close()
			return nil, ctx.Err()
		case <-time.After(baseLockPoll):
		}
	}
}

// baseLockPath is the lock file, beside the base's playbook-version stamp: both are
// sand's own state about that base image, and both live under the Lima home so they
// travel with it (see baseVersionPath).
func baseLockPath(hf lima.HostFiles, baseName string) string {
	return baseVersionPath(hf, baseName) + ".lock"
}
