package sshx

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

// TestSuccessDespiteHeldPipes pins the one mapping the helper makes: a held
// pipe after a successful exit is success, and nothing else is rewritten.
func TestSuccessDespiteHeldPipes(t *testing.T) {
	if err := SuccessDespiteHeldPipes(exec.ErrWaitDelay); err != nil {
		t.Errorf("ErrWaitDelay = %v, want nil", err)
	}
	if err := SuccessDespiteHeldPipes(fmt.Errorf("ssh: %w", exec.ErrWaitDelay)); err != nil {
		t.Errorf("wrapped ErrWaitDelay = %v, want nil", err)
	}
	if err := SuccessDespiteHeldPipes(nil); err != nil {
		t.Errorf("nil = %v, want nil", err)
	}
	real := errors.New("exit status 255")
	if err := SuccessDespiteHeldPipes(real); err != real {
		t.Errorf("a real failure = %v, want it returned unchanged", err)
	}
}

// TestMuxSuppressed covers the opt-out's three states, including the nil
// context a caller with no context of its own may pass.
func TestMuxSuppressed(t *testing.T) {
	//lint:ignore SA1012 a nil context is a supported input.
	if MuxSuppressed(nil) { //nolint:staticcheck
		t.Error("nil context reported as opted out")
	}
	if MuxSuppressed(context.Background()) {
		t.Error("plain context reported as opted out")
	}
	ctx, cancel := context.WithCancel(WithoutMux(context.Background()))
	defer cancel()
	if !MuxSuppressed(ctx) {
		t.Error("WithoutMux's opt-out was lost through a derived context")
	}
}
