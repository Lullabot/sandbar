package provision

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
)

// tickWriter is the progress stream a metered transfer writes to, with a signal
// on every write.
//
// The signal is what makes these tests deterministic instead of timed. A test
// that slept and then asserted "a line should have appeared by now" is a test
// that passes on a fast machine and flakes on a loaded one — and, worse, one
// that keeps passing after the ticker stops firing at all, because a sleep long
// enough to be safe is also long enough to hide the bug. Here the FAKE GUEST
// blocks until the meter has actually spoken, so the assertion is about
// causality rather than about elapsed time.
//
// It is mutex-guarded because the meter narrates from its own goroutine while
// the test's guest goroutine reads what has been said so far.
type tickWriter struct {
	mu   sync.Mutex
	sb   strings.Builder
	tick chan struct{}
}

func newTickWriter() *tickWriter {
	return &tickWriter{tick: make(chan struct{}, 64)}
}

func (w *tickWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.sb.Write(p)
	w.mu.Unlock()
	select {
	case w.tick <- struct{}{}:
	default: // a full buffer just means the test is not waiting; never block the meter
	}
	return len(p), nil
}

func (w *tickWriter) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sb.String()
}

// waitForLine blocks until the stream contains substr, failing the test rather
// than hanging forever if the meter never says it.
func (w *tickWriter) waitForLine(t *testing.T, substr string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		if strings.Contains(w.text(), substr) {
			return
		}
		select {
		case <-w.tick:
		case <-deadline:
			t.Fatalf("progress stream never contained %q; got:\n%s", substr, w.text())
		}
	}
}

// meterFakeRunner is a guest whose transfers move a controlled number of bytes
// and — crucially — do not finish until the meter has reported on them, so a
// test can assert on an in-flight reading and not just on the summary.
type meterFakeRunner struct {
	calls [][]string

	// payload is written to out (stage-out's archive) in two halves, with the
	// transfer pausing between them until waitFor appears on the progress stream.
	payload []byte
	// drain, when set, reads stdin to completion (stage-in's archive) with the
	// same pause partway through.
	drain bool

	stream  *tickWriter
	waitFor string
	t       *testing.T
}

func (f *meterFakeRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	return nil, nil // every probe answers "yes": zstd is present, paths exist
}

func (f *meterFakeRunner) Stream(_ context.Context, stdin io.Reader, out io.Writer, args ...string) error {
	f.calls = append(f.calls, args)
	if len(f.payload) > 0 && out != nil {
		f.writeHalves(out)
	}
	if f.drain && stdin != nil {
		f.readWithPause(stdin)
	}
	return nil
}

func (f *meterFakeRunner) StreamOut(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error {
	return f.Stream(ctx, stdin, out, args...)
}

func (f *meterFakeRunner) writeHalves(out io.Writer) {
	half := len(f.payload) / 2
	if _, err := out.Write(f.payload[:half]); err != nil {
		f.t.Errorf("fake guest write: %v", err)
	}
	f.stream.waitForLine(f.t, f.waitFor)
	if _, err := out.Write(f.payload[half:]); err != nil {
		f.t.Errorf("fake guest write: %v", err)
	}
}

func (f *meterFakeRunner) readWithPause(stdin io.Reader) {
	buf := make([]byte, 4096)
	paused := false
	for {
		n, err := stdin.Read(buf)
		if n > 0 && !paused {
			paused = true
			f.stream.waitForLine(f.t, f.waitFor)
		}
		if err != nil {
			return
		}
	}
}

// fastStageProgress makes the meter report on a timescale a test can wait for.
func fastStageProgress(t *testing.T) {
	t.Helper()
	prev := stageProgressInterval
	stageProgressInterval = time.Millisecond
	t.Cleanup(func() { stageProgressInterval = prev })
}

// TestStageOutNarratesProgress drives a real StageOut and asserts the three
// things the progress stream owes a user watching a multi-minute copy: that it
// started, that it is still moving (with a byte count and a rate), and what it
// ended up moving. The assertion is on the STREAM the TUI parses, not on the
// meter's own formatting, because a meter that formats perfectly into a writer
// nobody wired up is exactly the silence this feature exists to end.
func TestStageOutNarratesProgress(t *testing.T) {
	fastStageProgress(t)

	stream := newTickWriter()
	f := &meterFakeRunner{
		payload: make([]byte, 128<<10),
		stream:  stream,
		waitFor: "Backing up home:",
		t:       t,
	}
	archive := filepath.Join(t.TempDir(), "home.tar")

	if err := StageOut(context.Background(), lima.New(f), "web", "/home/andrew", []string{"."}, archive, "home", stream); err != nil {
		t.Fatalf("StageOut: %v", err)
	}

	got := stream.text()
	for _, want := range []string{
		"==> Backing up home…",  // announced before any byte moved
		"==> Backing up home: ", // an in-flight reading, mid-transfer
		"/s",                    // …carrying a rate
		"==> Backed up home: ",  // the closing totals
		"131 kB",                // the whole payload, counted at the host end
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress stream missing %q; got:\n%s", want, got)
		}
	}
}

// TestStageInNarratesPercentage pins the half of the story a restore can tell
// that a backup cannot: the archive's size is known up front, so the reading is
// a real fraction of a real total rather than a bare byte count.
func TestStageInNarratesPercentage(t *testing.T) {
	fastStageProgress(t)

	stream := newTickWriter()
	f := &meterFakeRunner{drain: true, stream: stream, waitFor: "Restoring home:", t: t}
	archive := filepath.Join(t.TempDir(), "home.tar")
	if err := os.WriteFile(archive, make([]byte, 256<<10), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	if err := StageIn(context.Background(), lima.New(f), "web", "/home/andrew", "andrew", []string{"."}, archive, "home", stream); err != nil {
		t.Fatalf("StageIn: %v", err)
	}

	got := stream.text()
	for _, want := range []string{
		"==> Restoring home…",
		"% of 262 kB",         // a percentage OF the archive's known size
		"==> Restored home: ", // the closing totals
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress stream missing %q; got:\n%s", want, got)
		}
	}
}

// TestStageMeterSaysNothingAboutAQuickTransfer guards the other half of the
// cadence rule. A reset stages several archives, most of them tiny
// (~/.claude.json is kilobytes), and narrating each of those with a start line,
// a reading and a summary would bury the one transfer that is actually slow
// under a screenful of noise about the ones that were not.
func TestStageMeterSaysNothingAboutAQuickTransfer(t *testing.T) {
	stream := newTickWriter()
	m := newStageMeter(stream, "Backing up", "Backed up", "Claude data", 0)
	m.add(4096)
	m.stop()

	if got := stream.text(); strings.Contains(got, "Backed up") {
		t.Fatalf("a sub-interval transfer printed a summary:\n%s", got)
	}
}

// TestStageMeterNilOutIsSilentAndSafe: both backends' resets are legitimately
// called with a nil progress writer, and a transfer must not have to know which
// kind of caller it is serving.
func TestStageMeterNilOutIsSilentAndSafe(t *testing.T) {
	var m *stageMeter = newStageMeter(nil, "Backing up", "Backed up", "home", 0)
	if m != nil {
		t.Fatalf("newStageMeter(nil) = %v, want nil", m)
	}
	m.add(1024) // must not panic
	m.stop()
}

// TestStageMeterLines pins the wording and the arithmetic of both readings
// against a frozen clock, which is the part that would otherwise only ever be
// eyeballed in a screenshot.
func TestStageMeterLines(t *testing.T) {
	start := time.Now()
	clock := start
	m := &stageMeter{
		verbing: "Restoring", verbed: "Restored", subject: "home",
		total: 4_000_000_000,
		start: start,
		now:   func() time.Time { return clock },
	}
	m.n.Store(1_000_000_000)
	clock = start.Add(20 * time.Second)

	if got, want := m.progressLine(), "Restoring home: 25% of 4.0 GB, 50 MB/s"; got != want {
		t.Fatalf("progressLine = %q, want %q", got, want)
	}

	m.n.Store(4_000_000_000)
	clock = start.Add(80 * time.Second)
	if got, want := m.summaryLine(), "Restored home: 4.0 GB in 1m20s, 50 MB/s"; got != want {
		t.Fatalf("summaryLine = %q, want %q", got, want)
	}

	// With no total — every stage-out — the reading is bytes and rate, and never
	// an invented percentage.
	out := &stageMeter{verbing: "Backing up", subject: "home", start: start, now: func() time.Time { return clock }}
	out.n.Store(1_500_000_000)
	if got, want := out.progressLine(), "Backing up home: 1.5 GB, 19 MB/s"; got != want {
		t.Fatalf("progressLine (no total) = %q, want %q", got, want)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1000, "1.0 kB"},
		{9999, "10.0 kB"},
		{412_000_000, "412 MB"},
		{1_400_000_000, "1.4 GB"},
	}
	for _, tc := range cases {
		if got := humanBytes(tc.n); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// TestPercentClamps: the archive is stat'd once and read afterwards, so a count
// that overruns the total is possible in principle — and a bar reporting 103%
// undermines every honest number beside it.
func TestPercentClamps(t *testing.T) {
	if got := percent(150, 100); got != 100 {
		t.Fatalf("percent(150, 100) = %d, want 100", got)
	}
	if got := percent(1, 0); got != 0 {
		t.Fatalf("percent(1, 0) = %d, want 0", got)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{900 * time.Millisecond, "1s"},
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m30s"},
		{3661 * time.Second, "61m01s"},
	}
	for _, tc := range cases {
		if got := humanDuration(tc.d); got != tc.want {
			t.Errorf("humanDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
