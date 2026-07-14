package lima

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

// chunkRunner replays canned output chunks to the stream writer, so a test can
// choose exactly where the chunk boundaries fall.
type chunkRunner struct{ chunks []string }

func (r *chunkRunner) Output(context.Context, ...string) ([]byte, error) { return nil, nil }

func (r *chunkRunner) StreamOut(_ context.Context, _ io.Reader, out io.Writer, _ ...string) error {
	return r.Stream(context.Background(), nil, out)
}

func (r *chunkRunner) Stream(_ context.Context, _ io.Reader, out io.Writer, _ ...string) error {
	for _, c := range r.chunks {
		if _, err := io.WriteString(out, c); err != nil {
			return err
		}
	}
	return nil
}

// Copy passes -v for progress, which also switches on ssh's debug1 chatter: one
// "truncating at <size>" line per file received. Harvesting the apt cache copies
// a directory of .debs, so that noise would bury the progress -v was asked for.
func TestCopyDropsSSHDebugLinesButKeepsProgress(t *testing.T) {
	var out bytes.Buffer
	c := New(&chunkRunner{chunks: []string{
		"/usr/bin/scp: debug1: truncating at 3192\n",
		"file.deb                        100%  3192     1.2MB/s   00:00\n",
		"debug1: Exit status 0\n",
		"/usr/bin/scp: debug2: channel 0: read<=0\n",
	}})
	if err := c.Copy(context.Background(), &out, true, "base:/var/cache/apt/archives", "/host"); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "debug") {
		t.Errorf("ssh debug lines reached the stream:\n%s", got)
	}
	if !strings.Contains(got, "file.deb") {
		t.Errorf("the progress line was dropped along with the noise:\n%s", got)
	}
}

// The pipe hands over arbitrary chunks, not lines, so a debug line can be split
// across two writes. Filtering per-chunk would leak the tail of every split one.
func TestSCPDebugFilterHandlesLinesSplitAcrossChunks(t *testing.T) {
	var out bytes.Buffer
	f := &scpDebugFilter{w: &out}
	for _, c := range []string{"/usr/bin/scp: deb", "ug1: truncating at 8301", "6640\nkeep me\n"} {
		n, err := f.Write([]byte(c))
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		if n != len(c) {
			t.Fatalf("Write reported %d of %d bytes; a short write is an error to the caller", n, len(c))
		}
	}
	if err := f.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := out.String(); got != "keep me\n" {
		t.Errorf("got %q, want %q", got, "keep me\n")
	}
}

// scp ends its final progress line without a newline, so anything held back
// waiting for one has to be released when the copy finishes.
func TestSCPDebugFilterFlushesATrailingPartialLine(t *testing.T) {
	var out bytes.Buffer
	f := &scpDebugFilter{w: &out}
	if _, err := f.Write([]byte("debug1: dropped\nfile.deb 100%")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := out.String(); got != "" {
		t.Errorf("a partial line was emitted before Flush: %q", got)
	}
	if err := f.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := out.String(); got != "file.deb 100%" {
		t.Errorf("got %q, want %q", got, "file.deb 100%")
	}
}
