package lima

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Runner executes limactl. It is abstracted behind an interface so that tests
// never spawn a real binary: production code uses execRunner, tests use a fake.
type Runner interface {
	// Output runs `limactl args...` and returns its stdout only. limactl logs
	// (logrus "time=… level=… msg=…" lines) go to stderr; mixing them into stdout
	// would corrupt parsed output such as `list --format json`, so stderr is kept
	// separate and folded into the returned error instead.
	Output(ctx context.Context, args ...string) ([]byte, error)
	// Stream runs `limactl args...`, piping stdin in and combined output to out.
	// Used for long-running, interactive commands like `shell` and `start`, where
	// the caller wants to see everything (stdout and stderr) live.
	Stream(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error
}

// execRunner is the real Runner: it shells out to the limactl binary.
type execRunner struct{ bin string }

// NewExecRunner returns a Runner backed by the real limactl binary on $PATH.
func NewExecRunner() Runner { return &execRunner{bin: "limactl"} }

func (r *execRunner) Output(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.bin, args...)
	// Capture stdout and stderr separately. limactl writes its JSON/template
	// output to stdout and its logs to stderr; CombinedOutput would interleave a
	// logrus line into the JSON and break parsing, so keep them apart and surface
	// stderr only as diagnostics on failure.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
	}
	return stdout.Bytes(), err
}

func (r *execRunner) Stream(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Stdin = stdin
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}
