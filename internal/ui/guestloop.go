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
//  2. A BOUNDED lifetime. The loop counts its passes and exits after
//     guestLoopTTL, so even a loop nothing ever reaps is gone within the hour,
//     and the host's ordinary reconnect brings a fresh one back.
//
// Two sand processes watching the SAME guest will take turns killing each
// other's probe (each reconnect kills the incumbent), costing each side a
// reconnect delay. That is deliberate: cleaning up real strays is worth more
// than a rare double-watcher's churn, and the alternative — a per-process pid
// file — would make sand unable to clean up after its own previous run, which
// is the common case.

import (
	"strconv"
	"strings"
	"time"
)

// guestLoopTTL bounds ONE guest loop's lifetime. It is deliberately long: the
// pid-file janitor and sshd's own reaping are what handle strays promptly, and
// this is only the last backstop, so it is set to trade a rare, brief gap in the
// gauges (one reconnect per VM per half hour) for a hard ceiling on how long an
// unreaped loop can run.
const guestLoopTTL = 30 * time.Minute

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
