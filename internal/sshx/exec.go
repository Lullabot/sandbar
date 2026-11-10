package sshx

import (
	"errors"
	"os/exec"
	"time"
)

// WaitDelay bounds how long a cancelled command may wait on its child's I/O
// pipes before it gives up on them, closes them, and returns. Every executor that
// runs ssh — or limactl, which forks ssh — sets it as cmd.WaitDelay.
//
// It exists because of something observed against a real Lima VM, not something
// assumed: `limactl shell` FORKS an ssh client, and that child inherits the
// stdout/stderr pipes os/exec created for us. exec.CommandContext's cancellation
// kills only its DIRECT child — limactl — so the ssh grandchild is orphaned, keeps
// running (it went on streaming guest output for 20+ seconds after the cancel), and
// holds those pipes open. cmd.Wait() waits for the goroutines copying them, so it
// NEVER RETURNS: the caller's goroutine leaks and, worse, the SSH connection into
// the guest stays open — the exact cost the guest heartbeat's idle-gating exists to
// avoid paying.
//
// WaitDelay is Go's remedy for precisely this ("a child process that exits but
// leaves its I/O pipes unclosed"). Once the context is done, it bounds the wait,
// then closes the pipes — which frees our goroutine AND hands the orphan a SIGPIPE
// on its next write, so it dies too. It has no effect on a command that is never
// cancelled and whose child closes its pipes on exit, which is every command on the
// happy path.
const WaitDelay = 2 * time.Second

// SuccessDespiteHeldPipes maps exec.ErrWaitDelay to success, and every executor
// that sets WaitDelay on captured or streamed pipes must route its Run error
// through it.
//
// Go returns ErrWaitDelay ONLY in place of a nil error ("Wait returns
// ErrWaitDelay instead of a nil error"): the child exited reporting success and
// its output was drained, but something that inherited its pipes still held
// them open when WaitDelay expired. A real failure — a non-zero remote exit, a
// transport 255, the kill after a cancel — produces an ExitError, which takes
// precedence, so this can never mask one.
//
// The inheritor is real, not hypothetical: the FIRST mux ssh to a target
// (ControlMaster=auto + ControlPersist, see muxFlags) forks a background
// master, and on OpenSSH builds that do not detach its stderr, that master holds the
// pipe for the whole ControlPersist window. Observed in the field: a Proxmox
// base build's ten-minute dependency install — the first ssh into the fresh
// guest, and therefore the connection that BECAME the master — succeeded and
// was then reported as "Failed: … exec: WaitDelay expired before I/O
// complete", which purged the successfully provisioned base. The lingering
// holder is exactly what WaitDelay exists to stop waiting on; it is not the
// command's failure.
func SuccessDespiteHeldPipes(err error) error {
	if errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	return err
}

// ExitTransport is the status ssh exits with when the CONNECTION failed rather
// than the remote command: it could not be established (authentication with a
// locked agent among the causes), it was torn down mid-session, or the remote
// command was killed by a signal (OpenSSH maps the protocol's exit-signal onto
// 255, and prints nothing about it at the default log level).
//
// It is technically ambiguous — a remote command that itself exits 255 is
// reported identically, since the protocol has one field for both. It is not
// ambiguous in practice: every remote command sandbar runs is a `set -e` bash
// script or an ansible-playbook run, whose own failure statuses are 2 (failed
// task), 4 (unreachable) and 250 (unexpected), never 255.
const ExitTransport = 255

// IsTransportFailure reports whether err is a command that exited with
// ExitTransport — the class of failure worth waiting out and retrying, because
// nothing the guest did caused it. `limactl shell` passes its ssh child's
// status through, so this holds for every backend's guest commands.
func IsTransportFailure(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == ExitTransport
}
