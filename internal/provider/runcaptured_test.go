package provider_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/sshx"
	"github.com/lullabot/sandbar/internal/vm"
)

// shProvider stands `sh -c script` in for the limactl/ssh argv RunArgv
// returns, so RunCaptured's own process handling runs with no VM.
func shProvider(script string) *providerfake.Provider {
	return &providerfake.Provider{
		RunArgvFunc: func(vm.VM, string, string) []string { return []string{"sh", "-c", script} },
	}
}

// RunCaptured runs `sandbar publish`'s and the Landing pane's guest reads.
// The first mux ssh to a guest forks a ControlPersist master that can hold
// the captured pipes long after the command exits; `(sleep 5 &)` is that
// relationship in one line of sh. Without WaitDelay the read waits on the
// holder, and without SuccessDespiteHeldPipes a clean exit reads as a failure.
func TestRunCapturedHeldPipeSuccessIsSuccess(t *testing.T) {
	start := time.Now()
	out, err := provider.RunCaptured(context.Background(), shProvider("(sleep 5 &); echo done"), vm.VM{Name: "web"}, "/w", "x")
	if err != nil {
		t.Fatalf("RunCaptured = %v for a command that exited 0; a lingering pipe-holder is not the command's failure", err)
	}
	if strings.TrimSpace(string(out)) != "done" {
		t.Fatalf("RunCaptured stdout = %q, want the command's own output", out)
	}
	if elapsed := time.Since(start); elapsed < sshx.WaitDelay || elapsed > sshx.WaitDelay+2*time.Second {
		t.Fatalf("RunCaptured returned after %s; want it bounded by WaitDelay (%s), not by the holder", elapsed, sshx.WaitDelay)
	}
}

// The held-pipe mapping must not mask a real failure, and the guest's stderr
// must still reach the error.
func TestRunCapturedHeldPipeKeepsRealFailure(t *testing.T) {
	_, err := provider.RunCaptured(context.Background(), shProvider("(sleep 5 &); echo boom >&2; exit 7"), vm.VM{Name: "web"}, "/w", "x")
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 {
		t.Fatalf("RunCaptured error = %v; want the command's own exit status 7", err)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("RunCaptured error = %v; want the guest's stderr in it", err)
	}
}
