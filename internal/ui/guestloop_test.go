package ui

// guestloop_test.go pins the two properties that stop a long-lived guest probe
// from piling up inside a VM (guestloop.go): the loop identifies and kills the
// abandoned copy of itself before starting, and it does not run forever. Both
// are host-side backstops for the sshd reaping roles/base installs — see
// guestloop.go's header for the failure they were written against.

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The janitor must be self-identifying. Killing whatever pid a file names is not
// acceptable: pids are recycled, so a stale file could name an unrelated process
// — the developer's own build, a database — and this loop would kill it. The
// /proc/<pid>/cmdline check against the probe's own marker is what makes the kill
// safe, so it must be present and must be checked BEFORE the kill.
func TestGuestLoopJanitorChecksTheProcessBeforeKillingIt(t *testing.T) {
	script := guestLoop{
		marker:  heartbeatDelim,
		pidFile: "sand-heartbeat.pid",
		body:    "echo hi\n",
		every:   2 * time.Second,
	}.script()

	cmdline := strings.Index(script, "/proc/$old/cmdline")
	kill := strings.Index(script, "kill ")
	switch {
	case cmdline < 0:
		t.Fatalf("the janitor must verify the recorded pid via /proc, got:\n%s", script)
	case kill < 0:
		t.Fatalf("the janitor must kill the previous loop, got:\n%s", script)
	case cmdline > kill:
		t.Fatalf("the /proc check must come BEFORE the kill, got:\n%s", script)
	}
	if !strings.Contains(script, "grep -qF -e '"+heartbeatDelim+"'") {
		t.Fatalf("the pid must be matched against this probe's own marker, got:\n%s", script)
	}
	// A non-numeric pid file must never reach kill.
	if !strings.Contains(script, `case "$old" in ''|*[!0-9]*) old= ;; esac`) {
		t.Fatalf("a non-numeric pid file must be discarded, got:\n%s", script)
	}
	// And the loop must record itself for its successor to find.
	if !strings.Contains(script, `echo $$ > "$p"`) {
		t.Fatalf("the loop must record its own pid, got:\n%s", script)
	}
}

// The loop must be bounded, not `while true`: an unreaped copy has to age out on
// its own, because the pid-file janitor only runs when a NEW loop starts and
// sshd's reaping only helps on a base image new enough to have it.
func TestGuestLoopIsBoundedNotInfinite(t *testing.T) {
	script := guestLoop{marker: "---m---", pidFile: "p.pid", body: "echo hi\n", every: 2 * time.Second}.script()

	if strings.Contains(script, "while true") {
		t.Fatalf("the loop must be bounded, got:\n%s", script)
	}
	// 30 minutes of 2s passes.
	if !strings.Contains(script, `while [ "$n" -lt 900 ]`) {
		t.Fatalf("want 900 passes for a 2s probe over guestLoopTTL, got:\n%s", script)
	}
	if !strings.Contains(script, "sleep 2") {
		t.Fatalf("want the interval's sleep, got:\n%s", script)
	}
}

// A sub-second interval must never render `sleep 0` — that is a hot loop pinning
// a guest CPU, not a probe. Reachable from any test or caller that passes a
// millisecond interval, which the lifecycle tests do.
func TestGuestLoopNeverSleepsZero(t *testing.T) {
	script := guestLoop{marker: "---m---", pidFile: "p.pid", body: "echo hi\n", every: 10 * time.Millisecond}.script()
	if strings.Contains(script, "sleep 0") {
		t.Fatalf("a sub-second interval must clamp to 1s, got:\n%s", script)
	}
	if !strings.Contains(script, "sleep 1") {
		t.Fatalf("want the clamped 1s sleep, got:\n%s", script)
	}
}

// Only the sweep yields priority: its pass is a recursive find over $HOME, while
// the heartbeat's is two /proc reads that must not be delayed behind other work.
func TestGuestLoopLowPriorityIsOptIn(t *testing.T) {
	sweep := guestSweepScript(sweepInterval)
	if !strings.Contains(sweep, "renice") || !strings.Contains(sweep, "ionice") {
		t.Fatalf("the sweep must run at low priority, got:\n%s", sweep)
	}
	beat := guestScript(heartbeatInterval)
	if strings.Contains(beat, "renice") || strings.Contains(beat, "ionice") {
		t.Fatalf("the heartbeat must NOT be deprioritized, got:\n%s", beat)
	}
	// The two probes must not fight over one pid file.
	if strings.Contains(sweep, "sand-heartbeat.pid") {
		t.Fatal("the sweep must not use the heartbeat's pid file")
	}
}

// backoff doubles per consecutive failure and stops at the ceiling, and a single
// failure is still retried at the base delay — the ordinary `limactl stop` must
// not be punished for the pathological case this exists for.
func TestBackoffDoublesToACeiling(t *testing.T) {
	base, max := 5*time.Second, 30*time.Second
	for _, tc := range []struct {
		n    int
		want time.Duration
	}{
		{0, 5 * time.Second}, // never happened; treated as the first
		{1, 5 * time.Second},
		{2, 10 * time.Second},
		{3, 20 * time.Second},
		{4, 30 * time.Second}, // clamped
		{9, 30 * time.Second},
	} {
		if got := backoff(base, tc.n, max); got != tc.want {
			t.Errorf("backoff(5s, %d, 30s) = %s, want %s", tc.n, got, tc.want)
		}
	}
	if got := backoff(0, 3, max); got != 0 {
		t.Errorf("backoff with no base = %s, want 0 (a registry with no retry delay)", got)
	}
}

// Exercise the actual shell/stdin boundary: inactivity suppresses work, waking
// resumes the same process, and cancellation releases the stdin copier.
func TestDrivenProbeIdlesAndWakesWithoutReplacingProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := newProbeRequests(ctx, time.Hour)
	requests.activity(time.Now().Add(-2 * heartbeatIdleAfter))
	script := guestLoop{marker: "sand-test-driven", pidFile: "driven.pid", body: "echo $$\n", driven: true}.script()
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+t.TempDir())
	cmd.Stdin = requests
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 2)
	go func() {
		scanner := bufio.NewScanner(out)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	read := func() string {
		t.Helper()
		select {
		case line := <-lines:
			return line
		case <-time.After(3 * time.Second):
			t.Fatal("probe did not respond")
			return ""
		}
	}
	pid := read()
	select {
	case line := <-lines:
		t.Fatalf("idle probe did unsolicited work: %q", line)
	case <-time.After(30 * time.Millisecond):
	}
	requests.activity(time.Now())
	if next := read(); next != pid || pid == "" {
		t.Fatalf("process changed: %q -> %q", pid, next)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled probe retained stdin copier")
	}
}

func TestDrivenGuestLoopReapsAnAbandonedInputStream(t *testing.T) {
	script := guestLoop{marker: "sand-test-abandoned", pidFile: "abandoned.pid", body: "echo hi\n", driven: true}.script()
	script = strings.ReplaceAll(script, "-t 120", "-t 0.05")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+t.TempDir())
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	cmd.Stdin = reader // an open stream that never supplies a request
	if output, err := cmd.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("abandoned probe: %v, %q", err, output)
	}
	if ctx.Err() != nil {
		t.Fatal("guest failed to reap an abandoned stream")
	}
}
