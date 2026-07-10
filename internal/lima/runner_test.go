package lima

import (
	"bytes"
	"context"
	"strings"
	"testing"
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
