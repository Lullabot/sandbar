package lima

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/sshx"
)

// StreamOut must write only the command's stdout to out and keep its stderr
// out of that stream (folding it into the error). This is the exact property
// that keeps a `tar -czf -` archive from being corrupted by `limactl shell`'s
// `cd` warning: a regression here silently corrupts every reset's staged data.
// The test drives `sh` rather than limactl so it needs no VM or binary beyond
// a POSIX shell.
func TestExecRunnerStreamOutKeepsStderrSeparate(t *testing.T) {
	r := &execRunner{bin: "sh"}
	var out bytes.Buffer
	// Emit a byte to stdout and a noisy warning to stderr, then fail — mirroring
	// a guest command whose stdout is a payload and whose stderr is a warning.
	err := r.StreamOut(context.Background(), nil, &out,
		"-c", "printf payload; printf 'cd: no such file\\n' >&2; exit 2")
	if err == nil {
		t.Fatal("StreamOut should surface the command's non-zero exit as an error")
	}
	if got := out.String(); got != "payload" {
		t.Fatalf("stdout = %q, want %q — stderr must never leak into the payload", got, "payload")
	}
	if !strings.Contains(err.Error(), "cd: no such file") {
		t.Fatalf("error %q should carry the captured stderr for diagnostics", err.Error())
	}
}

// Stream, by contrast, deliberately merges stderr into out for live display —
// documenting the distinction so no one "simplifies" the two into one method.
func TestExecRunnerStreamMergesStderr(t *testing.T) {
	r := &execRunner{bin: "sh"}
	var out bytes.Buffer
	if err := r.Stream(context.Background(), nil, &out,
		"-c", "printf out; printf err >&2"); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "out") || !strings.Contains(got, "err") {
		t.Fatalf("Stream out = %q, want both stdout and stderr merged", got)
	}
}

// CANCELLING A STREAM MUST ACTUALLY REAP IT. This is not a hypothetical: it was
// observed against a real Lima VM. `limactl shell` FORKS an ssh client, and that
// child inherits the stdout/stderr pipes exec created for us. exec.CommandContext
// kills only its DIRECT child (limactl), so the ssh grandchild is orphaned, keeps
// running — it kept streaming guest output for 20+ seconds after cancel — and
// holds the pipes open. cmd.Wait() waits on the goroutines copying those pipes, so
// it never returns: the caller's goroutine leaks and the SSH connection to the
// guest stays open, which is precisely what the heartbeat's idle-gating exists to
// prevent.
//
// cmd.WaitDelay is Go's remedy, and it is what these two runners set: once the
// context is done, it bounds the wait and CLOSES the pipes, which both frees our
// goroutine and gives the orphan a SIGPIPE on its next write.
//
// The test needs no VM: `sh` standing in for limactl, backgrounding a sleeper that
// outlives it, reproduces the exact shape (a killed parent, an orphaned grandchild
// holding the inherited pipe).
func TestStreamReapsAnOrphanedGrandchildHoldingThePipe(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(r *execRunner, ctx context.Context, out io.Writer) error
	}{
		{"Stream", func(r *execRunner, ctx context.Context, out io.Writer) error {
			return r.Stream(ctx, nil, out, "-c", orphanScript)
		}},
		{"StreamOut", func(r *execRunner, ctx context.Context, out io.Writer) error {
			return r.StreamOut(ctx, nil, out, "-c", orphanScript)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &execRunner{bin: "sh"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			done := make(chan error, 1)
			go func() { done <- tc.run(r, ctx, io.Discard) }()

			// Let the grandchild come up holding the pipe, then kill the parent.
			time.Sleep(300 * time.Millisecond)
			cancel()

			// The grandchild sleeps far longer than WaitDelay. Without it the
			// wait lasts as long as the ORPHAN does; with it, it is bounded.
			select {
			case <-done:
			case <-time.After(sshx.WaitDelay + 5*time.Second):
				t.Fatalf("%s did not return within %v of cancel: its goroutine is blocked on a pipe the orphaned grandchild still holds — that is the leak", tc.name, sshx.WaitDelay)
			}
		})
	}
}

// orphanScript backgrounds a child that outlives the shell and keeps the shell's
// inherited stdout/stderr open — the `limactl shell` -> ssh relationship, in one
// line of POSIX sh.
const orphanScript = `sleep 60 & sleep 60`

// A held-pipe SUCCESS must be a success. The FIRST mux ssh to a target forks
// the ControlPersist master, and on some OpenSSH builds that master inherits
// (and keeps) the stderr pipe for its whole persist window — so the child
// exits 0, its output is fully drained, and Wait still comes back with
// exec.ErrWaitDelay, which Go documents as replacing only a NIL error.
// Observed in the field as a Proxmox base build whose ten-minute dependency
// install succeeded and was then reported "Failed: … exec: WaitDelay expired
// before I/O complete" — and purged. The holderScript below is that
// relationship in one line of sh, exactly as orphanScript is for the cancel
// case.
func TestAHeldPipeAfterSuccessIsNotAFailure(t *testing.T) {
	r := &execRunner{bin: "sh"}

	var out bytes.Buffer
	start := time.Now()
	err := r.Stream(context.Background(), nil, &out, "-c", holderScript+"; echo done")
	if err != nil {
		t.Fatalf("Stream reported %v for a command that exited 0; a lingering pipe-holder is not the command's failure", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("done")) {
		t.Fatalf("Stream output %q lost the child's own output; want it drained in full before the pipes were given up on", out.String())
	}
	// The wait really was bounded by WaitDelay — proving the holder held the
	// pipes and the suppression (not a fast clean exit) is what was exercised.
	if elapsed := time.Since(start); elapsed < sshx.WaitDelay {
		t.Fatalf("Stream returned in %s, before WaitDelay (%s) — the grandchild did not hold the pipes and this test proved nothing", elapsed, sshx.WaitDelay)
	}
}

// The suppression maps ONLY the held-pipe success: a child that FAILS while a
// grandchild holds its pipes must still report its own failure.
func TestAHeldPipeDoesNotMaskARealFailure(t *testing.T) {
	r := &execRunner{bin: "sh"}

	var out bytes.Buffer
	err := r.Stream(context.Background(), nil, &out, "-c", holderScript+"; exit 7")
	if err == nil {
		t.Fatal("Stream reported success for a child that exited 7")
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 {
		t.Fatalf("Stream error = %v; want the child's own exit status 7", err)
	}
}

// holderScript backgrounds a child that inherits and holds the shell's
// stdout/stderr past WaitDelay while the shell itself exits promptly — the
// ssh -> ControlPersist-master relationship, in one line of POSIX sh.
const holderScript = `(sleep 5 &)`
