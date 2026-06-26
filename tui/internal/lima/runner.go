package lima

import (
	"context"
	"io"
	"os/exec"
)

// Runner executes limactl. It is abstracted behind an interface so that tests
// never spawn a real binary: production code uses execRunner, tests use a fake.
type Runner interface {
	// Output runs `limactl args...` and returns its combined stdout+stderr.
	Output(ctx context.Context, args ...string) ([]byte, error)
	// Stream runs `limactl args...`, piping stdin in and combined output to out.
	// Used for long-running, interactive commands like `shell` and `start`.
	Stream(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error
}

// execRunner is the real Runner: it shells out to the limactl binary.
type execRunner struct{ bin string }

// NewExecRunner returns a Runner backed by the real limactl binary on $PATH.
func NewExecRunner() Runner { return &execRunner{bin: "limactl"} }

func (r *execRunner) Output(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.bin, args...)
	return cmd.CombinedOutput()
}

func (r *execRunner) Stream(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Stdin = stdin
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}
