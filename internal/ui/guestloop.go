package ui

// guestloop.go is the wrapper both long-lived guest probes share: the board's
// utilization heartbeat (heartbeat.go) and the checkout sweep (sweepshell.go).
// Each opens ONE ssh session per running VM and runs a shell loop inside the
// guest for as long as the board is on screen.
//
// # What this exists to prevent
//
// A guest loop outlives its host end. When the ssh client dies abruptly — the
// process killed, a shared control master lost, a laptop suspended — the guest
// is not told: upstream sshd defaults ClientAliveInterval to 0 (it never probes
// the peer) and TCPKeepAlive rides the kernel's two-hour idle timer. So the
// session process stays, the loop inside it keeps running, and the host
// meanwhile reconnects and starts ANOTHER one. Observed in the field: 77
// heartbeat and 79 sweep sessions opened against a single VM in one session,
// against 28 control-master deaths. A guest carrying dozens of these — the
// sweep's pass is a recursive `find` over $HOME — starves itself until the OOM
// killer takes out sshd, which drops every session on the box including the
// user's own interactive ones.
//
// roles/base fixes the guest side properly (ClientAliveInterval, so sshd reaps
// an abandoned session in about a minute and the loop dies with its stdout).
// This file bounds the same leak from the host side, for the base images that
// predate that drop-in and for any path where sshd never notices:
//
//  1. A SELF-IDENTIFYING pid file. Each loop records its shell's pid, and a new
//     loop kills the recorded one first — but only after confirming, through
//     /proc/<pid>/cmdline, that the pid really is a sand probe of the same kind
//     and not whatever unrelated process has since inherited that number.
//  2. Host-driven loops exit on stdin EOF or 120 seconds without a request.
//     Healthy streams stay authenticated indefinitely. Legacy autonomous loops
//     instead count passes and exit after guestLoopTTL.
//
// Two sand processes watching the SAME guest will take turns killing each
// other's probe (each reconnect kills the incumbent), costing each side a
// reconnect delay. That is deliberate: cleaning up real strays is worth more
// than a rare double-watcher's churn, and the alternative — a per-process pid
// file — would make sand unable to clean up after its own previous run, which
// is the common case.

import (
	"context"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// guestLoopTTL bounds ONE guest loop's lifetime. It is deliberately long: the
// pid-file janitor and sshd's own reaping are what handle strays promptly, and
// this is only the last backstop, so it is set to trade a rare, brief gap in the
// gauges (one reconnect per VM per half hour) for a hard ceiling on how long an
// unreaped loop can run.
const guestLoopTTL = 30 * time.Minute

// guestLoopRotateAfter is how old a guest stream must be for its end to be
// read as the loop's own scheduled TTL exit rather than a failure. The loop
// runs a fixed pass count whose sleeps alone take the whole TTL, so a real
// loop always lives slightly OVER guestLoopTTL and never meaningfully under
// it; one minute of slack keeps the classification unambiguous. A stream this
// old that ends did what it was built to do — announcing it as "lost the
// guest connection" (and paying a retry cooldown) would turn a planned
// rotation into a twice-hourly false alarm per VM.
const guestLoopRotateAfter = guestLoopTTL - time.Minute

// guestLoop is one long-lived in-guest probe: a body run every interval, framed
// by the janitor and the pass counter above.
type guestLoop struct {
	// marker is a string that appears in the loop's OWN argv, which is what
	// makes the pid check self-identifying. Both probes already print a unique
	// delimiter (heartbeatDelim, sweepEndMarker), so the marker is free — it is
	// in the script, and the script is the argv of the `sh -c` that runs it.
	marker string

	// pidFile is the basename recorded under $XDG_RUNTIME_DIR (a per-user
	// directory), falling back to /tmp on a guest where it is unset.
	pidFile string

	// body is one pass. It must end with a newline; the loop framing is added
	// around it verbatim, so what a pass prints is entirely this string's
	// business.
	body string

	// every is the sleep between passes.
	every time.Duration

	// lowPrio renices and ionices the loop's whole process tree, for a probe
	// whose pass is heavy enough to be worth yielding to real work (the sweep's
	// `find`). The heartbeat's two /proc reads are not.
	lowPrio bool

	// driven waits for host requests instead of polling independently.
	driven bool
}

// script renders the loop as a POSIX sh program for `sh -c`.
//
// Nothing here may use `set -e`: every step of the janitor is allowed to fail on
// a guest that lacks /proc, a writable runtime dir, or `tr` — a probe that
// refuses to start because it could not clean up would be strictly worse than
// one that just starts.
func (l guestLoop) script() string {
	secs := int(l.every / time.Second)
	if secs < 1 {
		secs = 1 // never `sleep 0`: that is a hot loop, not a probe
	}
	passes := int(guestLoopTTL/time.Second) / secs
	if passes < 1 {
		passes = 1
	}

	var b strings.Builder
	b.WriteString(`p="${XDG_RUNTIME_DIR:-/tmp}/` + l.pidFile + `"` + "\n")
	b.WriteString("old=$(cat \"$p\" 2>/dev/null)\n")
	// A pid file is untrusted input: anything non-numeric is discarded rather
	// than handed to kill.
	b.WriteString("case \"$old\" in ''|*[!0-9]*) old= ;; esac\n")
	// -e is load-bearing: every marker begins with dashes, which grep would
	// otherwise read as its own options.
	b.WriteString("if [ -n \"$old\" ] && [ -r \"/proc/$old/cmdline\" ] && " +
		"tr '\\0' ' ' < \"/proc/$old/cmdline\" | grep -qF -e '" + l.marker + "'; then\n")
	b.WriteString("  kill \"$old\" 2>/dev/null\n")
	b.WriteString("fi\n")
	b.WriteString("echo $$ > \"$p\" 2>/dev/null\n")
	if l.lowPrio {
		b.WriteString("renice 10 $$ >/dev/null 2>&1\n")
		b.WriteString("ionice -c3 -p $$ >/dev/null 2>&1\n")
	}
	if l.driven {
		b.WriteString("while IFS= read -r -t 120 request; do\n")
		b.WriteString(l.body)
		b.WriteString("done")
		return b.String()
	}
	b.WriteString("n=0\n")
	b.WriteString("while [ \"$n\" -lt " + strconv.Itoa(passes) + " ]; do\n")
	b.WriteString(l.body)
	b.WriteString("n=$((n+1))\n")
	b.WriteString("sleep " + strconv.Itoa(secs) + "\n")
	b.WriteString("done")
	return b.String()
}

// backoff is the cooldown for the nth CONSECUTIVE failure of a guest connection:
// base doubled per failure, capped at max. Shared by the heartbeat and the sweep
// so the two probes cannot drift into different retry behaviour against the same
// guest.
//
// The point is not politeness, it is not making a sick guest sicker. Each retry
// costs the guest an ssh handshake and a fresh shell, and the failures this backs
// off from — a lost control master, an OOM-killed sshd, a guest thrashing on I/O
// — are exactly the ones a burst of reconnects prolongs. n <= 1 returns base
// unchanged, so a one-off death (the ordinary `limactl stop`) is still retried as
// promptly as it always was.
func backoff(base time.Duration, n int, max time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	wait := base
	for i := 1; i < n && wait < max; i++ {
		wait *= 2
	}
	if wait > max {
		wait = max
	}
	return wait
}

// probeRequests paces guest work over the existing SSH stdin. EOF or a two-minute
// gap reaps an abandoned guest loop without rotating healthy SSH connections.
// Read is called by the subprocess stdin copier; activity is updated by Update.
type probeRequests struct {
	ctx       context.Context
	lastInput atomic.Int64
	wake      chan struct{}
	every     time.Duration
	first     bool
}

const idleProbeInterval = time.Minute

func newProbeRequests(ctx context.Context, every time.Duration) *probeRequests {
	p := &probeRequests{ctx: ctx, every: every, wake: make(chan struct{}, 1), first: true}
	p.lastInput.Store(time.Now().UnixNano())
	return p
}

func (p *probeRequests) activity(at time.Time) {
	old := p.lastInput.Swap(at.UnixNano())
	if time.Since(time.Unix(0, old)) >= heartbeatIdleAfter && time.Since(at) < heartbeatIdleAfter {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}

func (p *probeRequests) Read(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	if !p.first {
		delay := p.every
		if time.Since(time.Unix(0, p.lastInput.Load())) >= heartbeatIdleAfter {
			delay = idleProbeInterval
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-p.ctx.Done():
			return 0, io.EOF
		case <-p.wake:
		case <-timer.C:
		}
	}
	p.first = false
	if p.ctx.Err() != nil {
		return 0, io.EOF
	}
	buf[0] = '\n'
	return 1, nil
}
