package lima

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// raceFatal is the line limactl 2.3.0 prints when its event watcher reads half a
// JSON event (lima-vm/lima#5742), as observed in sandbar's CI.
const raceFatal = "time=\"2026-11-07T10:00:00Z\" level=fatal msg=\"failed to unmarshal `{\\\"time\\\":\\\"2026-11-07T10:00:00Z\\\",\\\"status\\\":{\\\"sshLocalPort\\\":36889,` as events.Event: unexpected end of JSON input\""

// scriptRunner is a Runner whose every call is answered by fn, keyed by argv. It
// records each argv, and Stream writes fn's output to the caller's writer the way
// a real limactl would (combined output), while Output folds it into the error
// the way execRunner does with stderr.
type scriptRunner struct {
	calls [][]string
	fn    func(args []string) (string, error)
}

func (s *scriptRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	s.calls = append(s.calls, args)
	out, err := s.fn(args)
	if err != nil {
		if out != "" {
			err = fmt.Errorf("%w: %s", err, out)
		}
		return nil, err
	}
	return []byte(out), nil
}

func (s *scriptRunner) Stream(_ context.Context, _ io.Reader, w io.Writer, args ...string) error {
	s.calls = append(s.calls, args)
	out, err := s.fn(args)
	if w != nil {
		_, _ = io.WriteString(w, out)
	}
	return err
}

func (s *scriptRunner) StreamOut(ctx context.Context, r io.Reader, w io.Writer, args ...string) error {
	return s.Stream(ctx, r, w, args...)
}

func (s *scriptRunner) count(sub string) int {
	n := 0
	for _, c := range s.calls {
		if strings.Join(c, " ") == sub || (len(c) > 0 && c[0] == sub) {
			n++
		}
	}
	return n
}

// fakeRecoveryClock replaces the recovery's clock and sleep with a virtual clock
// that advances only when the loop sleeps, so a ten-minute timeout runs in
// microseconds. It returns a pointer to the total time slept.
func fakeRecoveryClock(t *testing.T) *time.Duration {
	t.Helper()
	var slept time.Duration
	base := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	oldNow, oldSleep := startRecoveryNow, startRecoverySleep
	startRecoveryNow = func() time.Time { return base.Add(slept) }
	startRecoverySleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		slept += d
		return nil
	}
	t.Cleanup(func() { startRecoveryNow, startRecoverySleep = oldNow, oldSleep })
	return &slept
}

var errExit1 = errors.New("exit status 1")

// guestScript simulates a VM whose start died of the race: status goes Running
// after statusAfter checks, and the boot-done probe passes after bootAfter
// probes. probe decides whether a given probe script succeeds once booted.
func guestScript(statusAfter, bootAfter int, probe func(script string) bool) func([]string) (string, error) {
	statusChecks, bootChecks := 0, 0
	return func(args []string) (string, error) {
		switch args[0] {
		case "start":
			return "INFO booting\n" + raceFatal + "\n", errExit1
		case "list":
			statusChecks++
			if statusChecks > statusAfter {
				return "Running\n", nil
			}
			return "Starting\n", nil
		case "shell":
			bootChecks++
			if bootChecks > bootAfter && probe(args[len(args)-1]) {
				return "", nil
			}
			return "", errExit1
		}
		return "", fmt.Errorf("unexpected limactl %v", args)
	}
}

func always(string) bool { return true }

func TestStartRaceRecoversWhenTheVMBoots(t *testing.T) {
	fakeRecoveryClock(t)
	for _, tc := range []struct {
		name  string
		start func(c *Client, out io.Writer) error
	}{
		{"StartStreaming", func(c *Client, out io.Writer) error {
			return c.StartStreaming(context.Background(), "web", out)
		}},
		{"CreateStreaming", func(c *Client, out io.Writer) error {
			return c.CreateStreaming(context.Background(), "web", "/tmp/overlay.yaml", out)
		}},
		{"Start", func(c *Client, _ io.Writer) error { return c.Start("web") }},
		{"Create", func(c *Client, _ io.Writer) error { return c.Create("web", "/tmp/overlay.yaml") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &scriptRunner{fn: guestScript(2, 3, always)}
			var out bytes.Buffer
			if err := tc.start(New(r), &out); err != nil {
				t.Fatalf("start = %v, want recovered", err)
			}
			if got := r.count("start"); got != 1 {
				t.Errorf("limactl start ran %d times, want exactly 1 (a re-run is not a readiness wait)", got)
			}
			if r.count("list") != 3 || r.count("shell") != 4 {
				t.Errorf("status checks = %d, boot probes = %d; want 3 and 4", r.count("list"), r.count("shell"))
			}
			if strings.HasSuffix(tc.name, "Streaming") {
				if !strings.Contains(out.String(), "==> Lima 2.3.0 bug: waiting for boot\n") {
					t.Errorf("progress output lacks the recovery banner:\n%s", out.String())
				}
				if !strings.Contains(out.String(), raceFatal) {
					t.Errorf("limactl's own output was not passed through:\n%s", out.String())
				}
			}
		})
	}
}

// The banner must fit a narrow build tile (about 36 columns).
func TestStartRaceBannerIsShort(t *testing.T) {
	fakeRecoveryClock(t)
	r := &scriptRunner{fn: guestScript(0, 0, always)}
	var out bytes.Buffer
	if err := New(r).StartStreaming(context.Background(), "web", &out); err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(l, "==> ") && strings.Contains(l, "Lima") && len([]rune(l)) > 36 {
			t.Errorf("banner %q is %d columns, want <= 36", l, len([]rune(l)))
		}
	}
}

func TestStartRaceTimesOutWithAnnotatedError(t *testing.T) {
	slept := fakeRecoveryClock(t)
	r := &scriptRunner{fn: guestScript(0, 1<<30, always)} // Running, never boots
	err := New(r).StartStreaming(context.Background(), "web", io.Discard)
	if err == nil {
		t.Fatal("start succeeded; want a timeout")
	}
	msg := err.Error()
	for _, want := range []string{
		"limactl start web: exit status 1", // the original failure
		"as events.Event: unexpected end of JSON input",
		"Lima 2.3.0 event-log race (https://github.com/lima-vm/lima/pull/5742)",
		"did not finish booting within 10m0s",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error lacks %q:\n%s", want, msg)
		}
	}
	if !errors.Is(err, errExit1) {
		t.Errorf("error does not wrap limactl's failure: %v", err)
	}
	if *slept < startRecoveryTimeout || *slept > startRecoveryTimeout+startRecoveryPoll {
		t.Errorf("waited %s, want about %s", *slept, startRecoveryTimeout)
	}
}

// A host agent that died is a real boot failure: stop waiting at once.
func TestStartRaceStopsWaitingWhenTheInstanceStops(t *testing.T) {
	slept := fakeRecoveryClock(t)
	r := &scriptRunner{fn: func(args []string) (string, error) {
		if args[0] == "start" {
			return raceFatal, errExit1
		}
		if args[0] == "list" {
			return "Stopped\n", nil
		}
		t.Fatalf("unexpected limactl %v", args)
		return "", nil
	}}
	err := New(r).StartStreaming(context.Background(), "web", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "instance is now Stopped") || !strings.Contains(err.Error(), "https://github.com/lima-vm/lima/pull/5742") {
		t.Fatalf("err = %v, want an annotated Stopped failure", err)
	}
	if *slept != 0 {
		t.Errorf("waited %s for a stopped instance", *slept)
	}
}

func TestOtherStartFailuresAreNotRetried(t *testing.T) {
	slept := fakeRecoveryClock(t)
	for _, output := range []string{
		"level=fatal msg=\"did not receive an event with the \\\"running\\\" status\"",
		// Only one half of the signature: a different JSON error.
		"level=fatal msg=\"failed to unmarshal `x` as events.Event: invalid character 'x'\"",
		"level=fatal msg=\"failed to load YAML: unexpected end of JSON input\"",
	} {
		r := &scriptRunner{fn: func(args []string) (string, error) {
			if args[0] != "start" {
				t.Fatalf("a non-race failure must not be waited on, but ran limactl %v", args)
			}
			return output, errExit1
		}}
		var out bytes.Buffer
		err := New(r).StartStreaming(context.Background(), "web", &out)
		if err == nil || err.Error() != "limactl start web: exit status 1" {
			t.Errorf("streamed err = %v, want it unchanged", err)
		}
		if strings.Contains(out.String(), "==>") {
			t.Errorf("printed a recovery banner for %q", output)
		}
		err = New(r).Start("web")
		if want := "limactl start web: exit status 1: " + output; err == nil || err.Error() != want {
			t.Errorf("buffered err = %v, want %q", err, want)
		}
	}
	if *slept != 0 {
		t.Errorf("slept %s", *slept)
	}
}

// A cancelled start is the caller's choice and is never waited out, even when
// its output happens to carry the signature.
func TestStartRaceNotRecoveredAfterCancel(t *testing.T) {
	fakeRecoveryClock(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &scriptRunner{fn: guestScript(0, 0, always)}
	if err := New(r).StartStreaming(ctx, "web", io.Discard); err == nil {
		t.Fatal("cancelled start reported success")
	}
	if r.count("list") != 0 {
		t.Error("a cancelled start was waited on")
	}
}

// Older guests have only /var/run/lima-boot-done; the probe must accept it.
func TestStartRaceAcceptsVarRunBootDone(t *testing.T) {
	fakeRecoveryClock(t)
	var probes []string
	r := &scriptRunner{fn: guestScript(0, 0, func(script string) bool {
		probes = append(probes, script)
		// Run the real probe against a fake root that has only /var/run.
		root := t.TempDir()
		if err := os.MkdirAll(root+"/run", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(root+"/var/run", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(root+"/var/run/lima-boot-done", nil, 0o644); err != nil {
			t.Fatal(err)
		}
		rooted := strings.ReplaceAll(script, " /", " "+root+"/")
		return exec.Command("sh", "-c", rooted).Run() == nil
	})}
	if err := New(r).StartStreaming(context.Background(), "web", io.Discard); err != nil {
		t.Fatalf("start = %v, want recovered via /var/run/lima-boot-done", err)
	}
	if len(probes) != 1 {
		t.Fatalf("probes = %v", probes)
	}
	if got := r.calls[len(r.calls)-1]; strings.Join(got[:4], " ") != "shell web sh -c" {
		t.Errorf("probe argv = %v, want a guest shell", got)
	}
}

// Remote Lima shares the Client, so the recovery runs over the hop too. This
// drives the real SSHHost Runner (fake exec seam) to prove the remote limactl's
// output reaches the detector on both the streamed and the buffered path.
func TestStartRaceRecoversOverSSH(t *testing.T) {
	fakeRecoveryClock(t)
	rec := &recordingExec{stub: func(ctx context.Context, argv []string) *exec.Cmd {
		switch {
		case hasToken(argv, "start"):
			return sh(ctx, "cat >&2 <<'EOF'\n"+raceFatal+"\nEOF\nexit 1")
		case hasToken(argv, "list"):
			return sh(ctx, "echo Running")
		case hasToken(argv, "shell"):
			return sh(ctx, "true")
		}
		return sh(ctx, "exit 3")
	}}
	c := New(hostWith(testCfg, rec))
	var out bytes.Buffer
	if err := c.StartStreaming(context.Background(), "web", &out); err != nil {
		t.Fatalf("streamed start over ssh = %v, want recovered", err)
	}
	if !strings.Contains(out.String(), "==> Lima 2.3.0 bug") {
		t.Errorf("no recovery banner over ssh:\n%s", out.String())
	}
	if err := c.Start("web"); err != nil {
		t.Fatalf("buffered start over ssh = %v, want recovered", err)
	}
}

func TestLimaVersionAdvisories(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want bool
	}{
		{"limactl version 2.3.0\n", true},
		{"limactl version v2.3.0", true},
		{"limactl version 2.2.1", false},
		{"limactl version 2.3.1", false},
		{"limactl version 2.3.0-rc.1", false},
		{"something else entirely", false},
	} {
		r := &fakeRunner{outputs: map[string][]byte{"--version": []byte(tc.out)}}
		c := New(r)
		if len(c.Advisories()) != 0 {
			t.Fatal("advisory before Preflight ran")
		}
		if err := c.Preflight(); err != nil {
			t.Fatal(err)
		}
		adv := c.Advisories()
		if got := len(adv) > 0; got != tc.want {
			t.Errorf("%q: advisory = %v, want %v", tc.out, adv, tc.want)
		}
		if tc.want && (!strings.Contains(adv[0], "https://github.com/lima-vm/lima/pull/5742") || !strings.Contains(adv[0], "recovers automatically")) {
			t.Errorf("advisory %q lacks the issue link or the reassurance", adv[0])
		}
	}
}
