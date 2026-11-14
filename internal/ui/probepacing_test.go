package ui

// probepacing_test.go pins the three independent bounds on how much guest work
// the board's probe streams can cause (guestloop.go's probeRequests and the
// driven guest loop). They were added together after a laptop woke from sleep
// and a feedback loop drove each guest at over a hundred heartbeat passes a
// second, with thousands more queued behind them; each bound alone would have
// stopped it, and each is tested on its own here.

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/lullabot/sandbar/internal/checkouts"
)

// rawTime mirrors time.Time's layout (wall uint64, ext int64, loc pointer). It
// exists only so suspendedTime can build the one value the public API cannot: a
// time whose wall clock and monotonic clock disagree, as they do on a value
// taken before the workstation slept.
type rawTime struct {
	wall uint64
	ext  int64
	loc  unsafe.Pointer
}

// suspendedTime returns a time whose wall clock is wallAgo in the past but whose
// monotonic reading is only monoAgo in the past: what time.Now() taken before a
// (wallAgo - monoAgo) sleep looks like afterwards. It skips the test if the
// runtime's representation has changed and the result is not actually skewed,
// so a Go upgrade can make this test inconclusive but never falsely green.
func suspendedTime(t *testing.T, wallAgo, monoAgo time.Duration) time.Time {
	t.Helper()
	at := time.Now().Add(-wallAgo)
	if unsafe.Sizeof(at) != unsafe.Sizeof(rawTime{}) {
		t.Skip("time.Time layout changed; cannot simulate a suspended clock")
	}
	(*rawTime)(unsafe.Pointer(&at)).ext += int64(wallAgo - monoAgo)
	wallAge := time.Since(time.Unix(0, at.UnixNano()))
	monoAge := time.Since(at)
	if wallAge < wallAgo-time.Second || monoAge > monoAgo+time.Second {
		t.Skipf("could not simulate a suspended clock (wall age %s, monotonic age %s)", wallAge, monoAge)
	}
	return at
}

func drainWake(p *probeRequests) bool {
	select {
	case <-p.wake:
		return true
	default:
		return false
	}
}

// After a sleep, Update calls activity with the SAME lastInput after every
// message. That must never count as the user returning: it used to, because one
// age was judged on the wall clock and the other on the monotonic clock, and
// every wake's answer produced the next Update.
func TestProbeActivityAfterSleepDoesNotWakeOnEveryUpdate(t *testing.T) {
	p := newProbeRequests(t.Context(), heartbeatInterval, idleProbeInterval)
	// The last keypress, taken before a laptop slept for half an hour.
	last := suspendedTime(t, heartbeatIdleAfter+30*time.Minute, time.Minute)
	p.activity(last)
	drainWake(p)

	for i := 0; i < 100; i++ {
		p.activity(last)
		if drainWake(p) {
			t.Fatalf("Update %d woke the stream with an unchanged input time", i)
		}
	}
}

// The model must store lastInput without a monotonic reading, so the model's
// own idle checks (refresh.go) and the streams' agree after a sleep.
func TestLastInputIsWallClock(t *testing.T) {
	m := model{lastInput: time.Now().Round(0)}
	if s := m.lastInput.String(); strings.Contains(s, "m=") {
		t.Fatalf("lastInput carries a monotonic reading: %s", s)
	}
}

// A genuinely new input after real idleness still wakes exactly once.
func TestProbeActivityWakesOnceOnReturnFromIdle(t *testing.T) {
	p := newProbeRequests(t.Context(), heartbeatInterval, idleProbeInterval)
	p.activity(time.Now().Add(-2 * heartbeatIdleAfter).Round(0))
	drainWake(p)

	back := time.Now().Round(0)
	p.activity(back)
	if !drainWake(p) {
		t.Fatal("returning from idle must wake the stream")
	}
	p.activity(back)
	p.activity(time.Now().Round(0))
	if drainWake(p) {
		t.Fatal("only the idle-to-active transition may wake")
	}
}

// readOne runs one Read and reports whether it returned within d.
func readOne(p *probeRequests, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		_, _ = p.Read(buf)
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// At most one request may be outstanding. However short the interval and
// however many wakes arrive, the next request waits for the previous pass's
// answer: requests issued faster than a guest answers them queue up and are
// replayed later, which is what kept guests busy for minutes.
func TestProbeRequestsKeepOneRequestInFlight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := newProbeRequests(ctx, time.Millisecond, time.Millisecond)

	if !readOne(p, time.Second) {
		t.Fatal("the first request must go out at once")
	}
	for i := 0; i < 5; i++ {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		_, _ = p.Read(buf)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("a second request went out before the first was answered")
	case <-time.After(100 * time.Millisecond):
	}

	p.passDone()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("an answered pass must release the next request")
	}

	// And cancellation still releases a Read that is waiting for an answer.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if !readOne(p, time.Second) {
		t.Fatal("a cancelled stream must release a Read waiting for an answer")
	}
}

// A pass that completes no sample (a delimiter with no readings, as on a guest
// without /proc) still answered its request; if it did not count, that stream
// would never be sent another one.
func TestSampleWriterReportsEveryPassEvenWithoutASample(t *testing.T) {
	calls := 0
	w := &sampleWriter{ctx: t.Context(), out: make(chan guestSample, 1), passDone: func() { calls++ }}
	if _, err := w.Write([]byte(heartbeatDelim + "\n")); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("an empty pass must still be reported answered, got %d calls", calls)
	}
	if _, err := w.Write([]byte("cpu  1 2 3 4 5 6 7 8\n")); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("a partial pass is not an answer, got %d calls", calls)
	}
}

func TestSweepWriterReportsEachPass(t *testing.T) {
	calls := 0
	w := &sweepWriter{ctx: t.Context(), out: make(chan checkouts.VMCheckouts, 1), passDone: func() { calls++ }}
	if _, err := w.Write([]byte(sweepEndMarker + "\n")); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("want one answered pass, got %d", calls)
	}
}

// The guest answers a backlog with one pass, not one per queued request, so even
// a host that floods requests cannot buy a pass per newline.
func TestDrivenGuestLoopCoalescesQueuedRequests(t *testing.T) {
	script := guestLoop{marker: "sand-test-coalesce", pidFile: "coalesce.pid", body: "echo pass\n", driven: true}.script()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+t.TempDir())
	cmd.Stdin = strings.NewReader(strings.Repeat("\n", 200))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("guest loop: %v", err)
	}
	if passes := strings.Count(string(out), "pass\n"); passes != 1 {
		t.Fatalf("200 queued requests must be answered by one pass, got %d", passes)
	}
}

// Both probes leave a floor between passes in the guest itself.
func TestDrivenGuestLoopsHaveAFloor(t *testing.T) {
	if s := guestScriptMode(heartbeatInterval, true); !strings.Contains(s, "sleep 1\ndone") {
		t.Fatalf("the heartbeat loop must sleep heartbeatFloor between passes, got:\n%s", s)
	}
	if s := guestSweepScriptMode(sweepInterval, true); !strings.Contains(s, "sleep 10\ndone") {
		t.Fatalf("the sweep loop must sleep sweepFloor between passes, got:\n%s", s)
	}
}

// The sweep slows right down when nobody is using the board; the heartbeat keeps
// its own idle rate.
func TestSweepIdlesLongerThanTheHeartbeat(t *testing.T) {
	if sweepIdleInterval <= idleProbeInterval || sweepIdleInterval <= sweepInterval {
		t.Fatalf("sweepIdleInterval %s must exceed both the heartbeat's idle interval %s and the sweep's active interval %s",
			sweepIdleInterval, idleProbeInterval, sweepInterval)
	}
}
