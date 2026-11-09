package lima

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Lima v2.3.0 event-log race (lima-vm/lima#5742,
// https://github.com/lima-vm/lima/pull/5742). This whole file's recovery is a
// workaround: remove it once a Lima release that includes that fix is the
// minimum sandbar supports.
//
// `limactl start` follows the host agent's progress by tailing ha.stdout.log
// (pkg/hostagent/events/watcher.go). The tail is not line-complete, so at end of
// file it can hand over HALF a JSON event, and the watcher treats that decode
// error as fatal: limactl exits 1 with
//
//	level=fatal msg="failed to unmarshal `{…` as events.Event: unexpected end of JSON input"
//
// The reader is the same in v2.2.x, but v2.3.0 (lima-vm/lima#4902) writes a
// progress event at the start and end of every boot requirement, so the race
// went from theoretical to routine. Two facts about v2.3.0 decide the recovery:
//
//   - pkg/instance/start.go returns the watch error but deliberately leaves the
//     host agent RUNNING. The VM keeps booting; only limactl's view of it died.
//     Treating the exit as a failed boot made sandbar delete a VM that was coming
//     up fine — on a reset, after the old VM was already gone.
//   - Re-running `limactl start NAME` is NOT a readiness wait: on an instance
//     that is already running it logs "already running" and returns at once. So
//     the recovery waits on the same thing Lima's own last boot requirement waits
//     on, the guest's /run/lima-boot-done (/var/run/lima-boot-done on older
//     guests, pkg/hostagent/requirements.go).
//
// The detection is DELIBERATELY narrow: the start must have failed AND its output
// must carry both halves of that one fatal message. Any other start failure is a
// real boot failure and is returned untouched — retrying it, or waiting on it,
// would hide the actual error behind a ten-minute wait. When
// https://github.com/lima-vm/lima/pull/5742 ships in a release, the message
// stops appearing and this stops firing on its own; it can then be removed.

// startEventRaceFailed reports whether a failed `limactl start`'s output is the
// v2.3.0 event-log race signature (see above). Both substrings are required.
func startEventRaceFailed(output string) bool {
	return strings.Contains(output, "as events.Event") &&
		strings.Contains(output, "unexpected end of JSON input")
}

// bootDoneProbe succeeds once the guest has finished Lima's boot sequence. It is
// the marker Lima's own final boot requirement polls for.
const bootDoneProbe = "test -e /run/lima-boot-done || test -e /var/run/lima-boot-done"

// Recovery timing. limactl's own `start --timeout` defaults to 10 minutes, so the
// recovery allows the same again: a first boot that runs cloud-init and grows a
// disk can legitimately need most of it. These are vars, not consts, so tests
// can drive the wait with a fake clock instead of real time; the suite is serial
// (AGENTS.md), which is what makes swapping a package var safe.
var (
	startRecoveryTimeout = 10 * time.Minute
	// startRecoveryPoll is the gap between checks. A boot takes tens of seconds
	// to minutes, and each check is a limactl process (an ssh round trip for
	// remote Lima), so a few seconds keeps the wait responsive without hammering
	// the host.
	startRecoveryPoll = 3 * time.Second
	// startRecoveryProbeTimeout bounds ONE guest probe. Before the guest's sshd
	// is up a `limactl shell` can sit in ssh's connect; without a bound a single
	// probe could eat the whole budget.
	startRecoveryProbeTimeout = 30 * time.Second

	startRecoveryNow   = time.Now
	startRecoverySleep = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
)

// startRaceNote is the annotation every unrecovered race error carries, so the
// user can tell this from a genuine boot failure and find the upstream fix.
const startRaceNote = "hit the Lima 2.3.0 event-log race (https://github.com/lima-vm/lima/pull/5742)"

// runStart runs one `limactl start` (args) for instance name. stream selects the
// Stream path (combined output to out, live) over the buffered Output path
// (stderr folded into the error). Either way, a failure that is the event-log
// race is handed to recoverStart; every other result is returned exactly as
// run/runStream would return it.
func (c *Client) runStart(ctx context.Context, out io.Writer, stream bool, name string, args ...string) error {
	var err error
	var output string
	if stream {
		// The streamed output goes to the user, not into the error, so keep a
		// bounded copy of its tail to look for the signature in. The fatal line
		// is limactl's last word, so the tail is where it is.
		tw := &tailWriter{w: out, max: 16 << 10}
		err = c.r.Stream(ctx, nil, tw, args...)
		output = tw.String()
	} else {
		_, err = c.r.Output(ctx, args...)
	}
	if err == nil {
		return nil
	}
	wrapped := fmt.Errorf("limactl %s: %w", strings.Join(args, " "), err)
	// A cancelled start is the caller's decision, never something to wait out.
	if ctx.Err() != nil || !startEventRaceFailed(output+"\n"+err.Error()) {
		return wrapped
	}
	// On the streamed path limactl's fatal line went to out, not into err; carry
	// it into the error so a timed-out recovery still names the original failure.
	if line := raceLine(output); line != "" && !strings.Contains(err.Error(), line) {
		wrapped = fmt.Errorf("%w: %s", wrapped, line)
	}
	return c.recoverStart(ctx, name, out, wrapped)
}

// raceLine returns the last line of output carrying the race signature.
func raceLine(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); startEventRaceFailed(l) {
			return l
		}
	}
	return ""
}

// recoverStart waits for an instance whose `limactl start` died of the event-log
// race to finish booting anyway: first until Lima lists it Running, then until
// the guest reports Lima's boot-done marker. It returns nil once the VM is up, and
// otherwise startErr annotated with startRaceNote.
//
// A Stopped or Broken status ends the wait at once: the host agent is gone, so
// the boot really did fail and there is nothing left to wait for.
func (c *Client) recoverStart(ctx context.Context, name string, out io.Writer, startErr error) error {
	if out != nil {
		// Short on purpose: a build tile may show only ~36 columns of the latest
		// "==>" line. The detail rides on the next line.
		fmt.Fprintf(out, "\n==> Lima 2.3.0 bug: waiting for boot\n"+
			"limactl start lost track of the boot (https://github.com/lima-vm/lima/pull/5742); the VM is still starting.\n")
	}
	deadline := startRecoveryNow().Add(startRecoveryTimeout)
	running := false
	var last error
	for {
		if !running {
			st, err := c.statusCtx(ctx, name)
			switch {
			case err != nil:
				last = err
			case st == "Running":
				running = true
			case st == "Stopped" || st == "Broken":
				return fmt.Errorf("%w; %s, and the instance is now %s", startErr, startRaceNote, st)
			default:
				last = fmt.Errorf("instance status %q", st)
			}
		}
		if running {
			pctx, cancel := context.WithTimeout(ctx, startRecoveryProbeTimeout)
			_, err := c.r.Output(pctx, "shell", name, "sh", "-c", bootDoneProbe)
			cancel()
			if err == nil {
				if out != nil {
					fmt.Fprintf(out, "\n==> %s booted\n", name)
				}
				return nil
			}
			last = fmt.Errorf("boot not finished: %w", err)
		}
		if ctx.Err() != nil {
			return fmt.Errorf("%w; %s: %w", startErr, startRaceNote, ctx.Err())
		}
		if !startRecoveryNow().Before(deadline) {
			return fmt.Errorf("%w; %s, and the VM did not finish booting within %s (last check: %v)",
				startErr, startRaceNote, startRecoveryTimeout, last)
		}
		if err := startRecoverySleep(ctx, startRecoveryPoll); err != nil {
			return fmt.Errorf("%w; %s: %w", startErr, startRaceNote, err)
		}
	}
}

// statusCtx is Status honouring ctx, for the recovery wait. It uses the scoped
// `limactl list NAME`, which a sibling instance mid-clone cannot fail
// (lima-vm/lima#5236, see Get).
func (c *Client) statusCtx(ctx context.Context, name string) (string, error) {
	out, err := c.r.Output(ctx, "list", name, "--format", "{{.Status}}")
	if err != nil {
		return "", fmt.Errorf("limactl status %s: %w", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// tailWriter forwards every write to w (when set) and keeps the last max bytes
// for inspection afterwards. exec serialises writes when Stdout and Stderr are
// the same comparable writer, but a mutex keeps it safe for any Runner.
type tailWriter struct {
	w   io.Writer
	max int

	mu  sync.Mutex
	buf []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	t.mu.Unlock()
	if t.w == nil {
		return len(p), nil
	}
	return t.w.Write(p)
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// --- Known-affected Lima versions -------------------------------------------------

// startRaceVersions are the Lima releases known to hit the event-log race often
// enough to warn about (https://github.com/lima-vm/lima/pull/5742). The recovery
// above does not consult this list — it keys on the failure itself, whatever the
// version — so the list only decides who gets told. Add a release here if it
// ships without the fix; delete the list (and Client.Advisories' use of it) once
// a Lima release includes that fix and is the norm.
var startRaceVersions = map[string]bool{
	"2.3.0": true,
}

// parseLimaVersion extracts the version from `limactl --version` output
// ("limactl version 2.3.0"), without any leading "v". It returns "" when the
// output does not have that shape.
func parseLimaVersion(out string) string {
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) < 3 || f[0] != "limactl" || f[1] != "version" {
		return ""
	}
	return strings.TrimPrefix(f[2], "v")
}

// Version returns the limactl version the last successful Preflight observed, or
// "" if Preflight has not run (or could not parse it). It performs no I/O.
func (c *Client) Version() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

// Advisories returns non-fatal warnings about the Lima in use, derived from what
// the last successful Preflight observed. It performs no I/O, so it is safe to
// call anywhere — including on a path that must not block.
func (c *Client) Advisories() []string {
	v := c.Version()
	if !startRaceVersions[v] {
		return nil
	}
	return []string{fmt.Sprintf(
		"Lima %s: `limactl start` may fail intermittently (https://github.com/lima-vm/lima/pull/5742); sandbar recovers automatically", v)}
}
