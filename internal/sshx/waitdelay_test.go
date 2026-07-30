package sshx

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

// TestSuccessDespiteHeldPipes pins the one mapping that keeps a successful
// command from being reported as a failure, and — just as importantly — proves it
// cannot swallow a real one.
//
// The regression is not hypothetical: a Proxmox base build's ten-minute
// dependency install ran over the first ssh to the fresh guest, which therefore
// became the ControlPersist master. On an OpenSSH build that does not detach the
// master's stderr, that master held the pipe past WaitDelay, and the successful
// build was reported "Failed: … exec: WaitDelay expired before I/O complete" —
// which purged the base it had just provisioned.
func TestSuccessDespiteHeldPipes(t *testing.T) {
	if got := SuccessDespiteHeldPipes(nil); got != nil {
		t.Errorf("nil error = %v, want nil", got)
	}
	if got := SuccessDespiteHeldPipes(exec.ErrWaitDelay); got != nil {
		t.Errorf("ErrWaitDelay = %v, want nil (the child exited reporting success)", got)
	}
	// Wrapped is the shape os/exec actually returns it in.
	if got := SuccessDespiteHeldPipes(fmt.Errorf("run: %w", exec.ErrWaitDelay)); got != nil {
		t.Errorf("wrapped ErrWaitDelay = %v, want nil", got)
	}

	// A genuine failure must survive untouched. Go returns ErrWaitDelay ONLY in
	// place of a nil error, so an ExitError always takes precedence — but a
	// mapping that matched too broadly here would silently pass every failed
	// provisioning run as a success.
	real := errors.New("exit status 255")
	if got := SuccessDespiteHeldPipes(real); !errors.Is(got, real) {
		t.Errorf("a real failure = %v, want it returned unchanged", got)
	}
}
