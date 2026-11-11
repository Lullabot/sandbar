package manage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/sshx"
	"github.com/lullabot/sandbar/internal/vm"
)

// ResetBackend is the part of provider.Provider a reset and its recovery
// drive. provider.Provider satisfies it; the narrower surface is what lets the
// CLI's and this package's tests fake a reset without a whole provider.
type ResetBackend interface {
	Reset(ctx context.Context, cfg vm.CreateConfig, opts provision.ResetOptions, out io.Writer) error
	Get(name string) (vm.VM, error)
	Delete(name string, force bool) error
	ShellOut(ctx context.Context, name string, argv ...string) ([]byte, error)
	Preflight() error
}

// The recovery policy, as package variables so tests can shrink it.
var (
	// resetRetryBudget is how long a reset that lost its connection waits for it
	// to come back. Long, on purpose: the case it exists for is a workstation
	// left locked over lunch or a meeting, whose SSH agent (1Password and similar
	// lock with the screen) refuses to authenticate until someone unlocks it. A
	// short budget would give up just before they got back.
	resetRetryBudget = 2 * time.Hour
	// resetPollInterval is how often the connection is probed while waiting. The
	// probe is one short command, so this costs nothing worth saving.
	resetPollInterval = 30 * time.Second
	// resetMaxAttempts caps the retries a connection that keeps dropping can
	// cause, so a probe that succeeds where the reset itself keeps failing
	// cannot loop until the budget runs out.
	resetMaxAttempts = 5
	// sleepCtx waits d or until ctx ends.
	sleepCtx = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
	now = time.Now
)

// Reset runs b.Reset and, when it fails only because the SSH transport
// dropped, waits for the connection to come back and finishes the job instead
// of handing the user a recovery command.
//
// A reset is the operation that most needs this. It deletes the VM part-way
// through, and from then on the only copy of what the user asked to keep is the
// staged backup on this host; the rest of the run is minutes of rebuilding over
// SSH. If the workstation locks and its SSH agent locks with it, every later
// SSH session fails to authenticate, and the reset used to fail outright,
// leaving a half-built VM and a `--from-backup` command to run by hand.
//
// What the retry does depends on where the failure struck:
//
//   - after the VM was deleted with a backup staged (a provision.StagedResetError),
//     it resumes from that backup, exactly as `sandbar reset --from-backup`
//     would, first removing the half-built replacement if — and only if — that
//     VM carries the backup's own RebuildID (OwnRebuild);
//   - otherwise it runs the same reset again. With nothing staged there is no
//     user data at risk on either side of the delete: the VM is either intact
//     or already gone, and a reset handles both.
//
// Only an ssh exit status of 255 (sshx.IsTransportFailure) is retried. A guest
// failure — a playbook task, a full disk — would fail the same way again, and
// a cancelled context (Ctrl-C, or the TUI cancelling the job) means stop.
func Reset(ctx context.Context, b ResetBackend, cfg vm.CreateConfig, opts provision.ResetOptions, out io.Writer, prov provider.Provenancer) error {
	err := b.Reset(ctx, cfg, opts, out)
	deadline := now().Add(resetRetryBudget)
	for attempt := 1; err != nil && attempt <= resetMaxAttempts; attempt++ {
		if ctx.Err() != nil || !sshx.IsTransportFailure(err) {
			return err
		}
		var staged *provision.StagedResetError
		resume := errors.As(err, &staged)
		where := "Nothing has been lost"
		if resume {
			where = "Your backup is safe in " + staged.Dir
		}
		fmt.Fprintf(out, "\n==> Lost the SSH connection while resetting %q (ssh exit status %d). Waiting up to %s for it to come back — for example, for a locked SSH agent to be unlocked. %s; press Ctrl-C to stop waiting.\n",
			cfg.Name, sshx.ExitTransport, resetRetryBudget, where)
		if !waitForTransport(ctx, b, cfg.Name, deadline) {
			return err
		}

		next := opts
		if resume {
			if rerr := replaceOwnRebuild(ctx, b, prov, staged.Dir, cfg.Name, out); rerr != nil {
				fmt.Fprintf(out, "\n==> Cannot resume automatically: %v\n", rerr)
				return err
			}
			next = resumeOptions(opts, staged.Dir)
			fmt.Fprintf(out, "\n==> The connection is back; resuming the reset of %q from %s (attempt %d of %d).\n", cfg.Name, staged.Dir, attempt+1, resetMaxAttempts+1)
		} else {
			fmt.Fprintf(out, "\n==> The connection is back; running the reset of %q again (attempt %d of %d).\n", cfg.Name, attempt+1, resetMaxAttempts+1)
		}
		err = b.Reset(ctx, cfg, next, out)
	}
	return err
}

// resumeOptions turns the options of a reset that staged into dir into those
// of a resume from dir. The preserve options go: the backup's own manifest
// says what it holds, exactly as for `sandbar reset --from-backup`. Everything
// else — the template source, the profile, the provider's hooks — carries
// over.
func resumeOptions(opts provision.ResetOptions, dir string) provision.ResetOptions {
	opts.RestoreFrom = dir
	opts.PreserveAgents, opts.PreserveProject, opts.PreserveHome = false, false, false
	opts.PreservePaths = nil
	return opts
}

// waitForTransport polls until the connection a reset needs works again, the
// deadline passes, or ctx ends, reporting whether it came back.
//
// The probe exercises what failed. While the half-built replacement is still
// running, that is a command inside it — the same SSH path (and, for a remote
// Lima host, the same hop to the host) the reset uses, so it fails for as
// long as the agent stays locked. With no running replacement to talk to, the
// backend's own Preflight is the nearest check: for a remote Lima host it is
// an SSH round trip; for Proxmox it only proves the API answers, and the
// resume itself is the test of the guest connection.
func waitForTransport(ctx context.Context, b ResetBackend, name string, deadline time.Time) bool {
	for {
		if probeTransport(ctx, b, name) == nil {
			return true
		}
		if !now().Before(deadline) {
			return false
		}
		if sleepCtx(ctx, resetPollInterval) != nil {
			return false
		}
	}
}

func probeTransport(ctx context.Context, b ResetBackend, name string) error {
	v, err := b.Get(name)
	switch {
	case err == nil && v.Status == "Running":
		_, err = b.ShellOut(ctx, name, "true")
		return err
	case err != nil && !errors.Is(err, vm.ErrNotFound):
		return err
	}
	return b.Preflight()
}

// replaceOwnRebuild clears the way for a resume from backupDir: nothing to do
// when no instance is called name, a delete when it is this backup's own
// half-built replacement, and an error otherwise.
func replaceOwnRebuild(ctx context.Context, b ResetBackend, prov provider.Provenancer, backupDir, name string, out io.Writer) error {
	if _, err := b.Get(name); errors.Is(err, vm.ErrNotFound) {
		return nil
	} else if err != nil {
		return fmt.Errorf("check whether %q exists: %w", name, err)
	}
	own, err := OwnRebuild(ctx, prov, backupDir, name)
	if err != nil {
		return err
	}
	if !own {
		return fmt.Errorf("%q exists, and nothing shows it is the half-built VM this reset left behind; delete it yourself, then run: sandbar reset %s --from-backup %s", name, name, backupDir)
	}
	return RemoveOwnRebuild(b, name, out)
}
