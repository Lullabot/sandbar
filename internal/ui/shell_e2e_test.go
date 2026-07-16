//go:build limae2e

// This file boots REAL Lima VMs and drives a REAL guest tmux server, so it is
// gated behind the `limae2e` build tag (and the LIMA_E2E env var) exactly like
// its siblings in this package — it never runs in the normal `go test ./...`
// (AGENTS.md's hard rule: no test may require a real limactl without the tag).
//
// Tmux-backed multi-shell access rests on three claims that cross a
// process/machine boundary and so cannot be proven by a fake lima.Runner or by
// asserting on argv alone (attach_test.go already does the latter, in-process):
//
//  1. TestE2EShellSessionPersistsAcrossDetach — the core claim. A process
//     started in the attached session survives its client detaching (closing
//     the terminal), and a fresh attach afterwards finds the session — and the
//     process — still there.
//  2. TestE2EShellGroupedSessionIndependence — a second, independent attach
//     gets a GROUPED session: exactly two guest tmux sessions sharing one
//     window set, the second client's own window switch does not move the
//     first, and neither client's display is size-clamped to the other's.
//  3. TestE2EShellDestroyUnattachedAsymmetry — the single most dangerous
//     failure mode this feature can ship, asserted explicitly rather than
//     eyeballed: when the grouped client detaches, ITS session evaporates
//     while `main` — and anything running in it — survives; and `main` keeps
//     surviving even after every client has gone, because that persistence,
//     not the windows, is the entire point of the feature.
//
// None of this can run against the ansible-free minimal overlay the other e2e
// tests in this package use (secretsE2EOverlay / e2eMinimalOverlay): tmux and
// its shipped ~/.tmux.conf come from the base-phase Ansible run
// (roles/base/defaults/main.yml, roles/user/templates/tmux.conf.j2), which
// that overlay deliberately skips to boot fast. These tests instead reuse
// ensureSharedBase's REAL, fully-provisioned shared base image (built once,
// shared with tests 4/5 in lima_e2e_test.go) and cheaply CLONE+START a fresh
// instance per test — no per-clone Ansible finalize is needed here, since a
// clone already carries the base's installed tmux and deployed config on its
// disk.
//
// Driving the actual attach needs a real controlling terminal — tmux refuses
// to run without one ("open terminal failed: not a terminal"), and a Go test
// binary has none. e2eHostTmux fabricates one exactly the way the manual
// validation harness did by hand: a PRIVATE host tmux server on its own -S
// socket path (never the default socket, never touched with kill-server, only
// ever killed by session name), used purely as a PTY source for driving
// `limactl shell` interactively and for reading back what appeared on screen.
//
// Run (needs limactl + KVM/nested virt, and a host tmux binary; downloads the
// Debian 13 image and builds the shared base once — allow real time):
//
//	LIMA_E2E=1 go test -tags limae2e -timeout 45m -run TestE2EShell ./internal/ui/
package ui

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"
)

// e2eHostTmux drives a private, throwaway host tmux server used only as a PTY
// source for these tests. It is a distinct -S socket path inside t.TempDir()
// per test — never the user's default socket, and NEVER torn down with
// kill-server (only ever by session name), so it cannot disturb any tmux
// session that exists outside this test.
type e2eHostTmux struct {
	t    *testing.T
	sock string
}

func newE2EHostTmux(t *testing.T) *e2eHostTmux {
	t.Helper()
	return &e2eHostTmux{t: t, sock: t.TempDir() + "/sand-e2e-shell.sock"}
}

// run execs `tmux -S sock <args...>` and fails the test on error.
func (h *e2eHostTmux) run(args ...string) []byte {
	h.t.Helper()
	full := append([]string{"-S", h.sock}, args...)
	out, err := exec.Command("tmux", full...).CombinedOutput()
	if err != nil {
		h.t.Fatalf("tmux %v: %v\n%s", args, err, out)
	}
	return out
}

// newSession starts a detached session named session, sized w x h, running
// argv directly — tmux execs trailing positional arguments as a real argv
// vector (verified against the installed tmux: a multi-word argument survives
// intact and a literal `;` in an argument is NOT treated as a command
// separator), so this needs no shell-quoting of argv's contents.
func (h *e2eHostTmux) newSession(session string, w, hgt int, argv ...string) {
	h.t.Helper()
	args := append([]string{"new-session", "-d", "-s", session, "-x", strconv.Itoa(w), "-y", strconv.Itoa(hgt)}, argv...)
	h.run(args...)
}

func (h *e2eHostTmux) capture(session string) string {
	h.t.Helper()
	return string(h.run("capture-pane", "-p", "-t", session))
}

func (h *e2eHostTmux) sendKeys(session string, keys ...string) {
	h.t.Helper()
	h.run(append([]string{"send-keys", "-t", session}, keys...)...)
}

func (h *e2eHostTmux) display(session, format string) string {
	h.t.Helper()
	return strings.TrimSpace(string(h.run("display-message", "-p", "-t", session, format)))
}

// kill ends session by NAME — the same effect as closing that terminal. A
// no-op (not a failure) if the session (or the whole private server) is
// already gone, which happens routinely once the last session in it exits.
func (h *e2eHostTmux) kill(session string) {
	h.t.Helper()
	_ = exec.Command("tmux", "-S", h.sock, "kill-session", "-t", session).Run()
}

// waitForAttach polls session's pane for a guest tmux STATUS BAR — the same
// signal ("[main…" / "[sand-…") the original manual PTY probe used to prove
// `limactl shell` allocates a real terminal — rather than a fixed sleep, and
// fails loudly on a timeout instead of racing ahead onto a not-yet-attached
// pane.
func (h *e2eHostTmux) waitForAttach(session string, timeout time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		pane := h.capture(session)
		if strings.Contains(pane, "[main") || strings.Contains(pane, "[sand-") {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out after %s waiting for a guest tmux status bar in session %q; last pane:\n%s", timeout, session, pane)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// e2eCloneAndStart clones name off the shared e2e base and boots it, with no
// per-clone Ansible finalize — the shared base's own build already carries
// the installed tmux binary and the deployed ~/.tmux.conf on its disk, and
// that is all these tests need. Far cheaper than a full CreateVM finalize
// pass, which these tests have no use for.
//
// Configure is called explicitly, the same way createVM sizes a real clone
// before its first start: a bare `limactl clone` does NOT inherit the shared
// base's modest footprint (ensureSharedBase's own comment explains why that
// footprint is modest — this host has 16 cores/15GiB and sand-tmux-probe, the
// long-lived probe VM, is left running at 8GiB throughout this validation) —
// left unconfigured, a clone defaults to the template's own (much larger)
// footprint, which was observed to make `limactl start` fail under real
// resource contention on this exact host.
//
// Output is captured (not discarded) so a real failure here — infra, not the
// tmux/attach code these tests actually exercise — is diagnosable rather than
// a bare "exit status 1".
func e2eCloneAndStart(t *testing.T, cli *lima.Client, name string) {
	t.Helper()
	_ = cli.Delete(name, true)
	t.Cleanup(func() { _ = cli.Delete(name, true) })

	ctx := context.Background()
	var out bytes.Buffer
	if err := cli.CloneStreaming(ctx, sharedBaseName, name, &out); err != nil {
		t.Fatalf("clone %s from %s: %v\n%s", name, sharedBaseName, err, out.String())
	}
	out.Reset()
	if err := cli.Configure(name, 2, "2GiB", vm.BaseDiskFloor); err != nil {
		t.Fatalf("configure %s: %v", name, err)
	}
	if err := cli.StartStreaming(ctx, name, &out); err != nil {
		t.Fatalf("start %s: %v\n%s", name, err, out.String())
	}
}

// e2eAttachArgv resolves name's guest home off the real Lima instance dir and
// builds the exact argv the TUI's `S` verb and `sand shell` both use —
// lima.AttachArgv, the one seam that knows guest tmux exists. Building it
// straight from lima.AttachArgv (rather than shelling out to the sand binary)
// keeps these tests focused on the attach mechanism itself.
func e2eAttachArgv(t *testing.T, cli *lima.Client, name string) []string {
	t.Helper()
	dir := e2eInstanceDir(t, cli, name)
	return lima.AttachArgv(name, lima.GuestHome(dir), os.Getenv("COLORTERM"))
}

// THE CORE CLAIM of tmux-backed shells: a process started in the attached
// session survives its client detaching — and the session (with that process
// still in it) is still there for a fresh attach afterwards. This is the entire
// reason the feature exists; if it regresses, everything else is decoration.
func TestE2EShellSessionPersistsAcrossDetach(t *testing.T) {
	if os.Getenv("LIMA_E2E") == "" {
		t.Skip("set LIMA_E2E=1 (and -tags limae2e) to run the real-Lima e2e tests")
	}
	cli, _, _ := ensureSharedBase(t)

	const name = "sand-e2e-shell-persist"
	e2eCloneAndStart(t, cli, name)
	argv := e2eAttachArgv(t, cli, name)

	h := newE2EHostTmux(t)
	h.newSession("c1", 200, 50, argv...)
	h.waitForAttach("c1", 30*time.Second)

	// A process that would NOT survive its shell dying, distinguishable in a
	// process listing.
	h.sendKeys("c1", "sleep 120 # sand-e2e-persist-marker", "Enter")
	time.Sleep(2 * time.Second) // let the marker process actually start in the guest before we detach

	if out := e2eGuestOut(t, cli, name, "pgrep", "-af", "sleep 120"); !strings.Contains(out, "sleep 120") {
		t.Fatalf("marker process is not even running before detach — test setup is broken, not the feature. pgrep: %q", out)
	}

	// THE ASSERTION: detach (close the terminal) — the session and the marker
	// process must both survive.
	h.kill("c1")
	time.Sleep(2 * time.Second)

	if out := e2eGuestOut(t, cli, name, "pgrep", "-af", "sleep 120"); !strings.Contains(out, "sleep 120") {
		t.Fatalf("PERSISTENCE BROKEN: the marker process did not survive detach. pgrep -af 'sleep 120': %q", out)
	}
	if out := e2eGuestOut(t, cli, name, "tmux", "list-sessions"); !strings.Contains(out, "main") {
		t.Fatalf("guest tmux session %q should still exist after detach, got tmux list-sessions: %q", "main", out)
	}

	// Re-attach with a FRESH client and confirm the window (and the still-
	// running marker) is still there — this is the "quit the TUI, close the
	// terminal, open a new one" scenario, exercised end to end.
	h.newSession("c2", 200, 50, argv...)
	h.waitForAttach("c2", 30*time.Second)
	t.Cleanup(func() { h.kill("c2") })

	if out := e2eGuestOut(t, cli, name, "pgrep", "-af", "sleep 120"); !strings.Contains(out, "sleep 120") {
		t.Fatalf("marker process should still be running after a fresh re-attach, pgrep: %q", out)
	}
}

// A SECOND, INDEPENDENT ATTACH GETS A GROUPED SESSION: exactly two guest tmux
// sessions sharing one window set (not a mirrored client on the same
// session), the second client's own window switch does not move the first,
// and neither client's display is clamped to the other's smaller size. A
// plain `tmux new-session -A -s main` would fail every one of these three
// checks, which is precisely why the design uses a grouped session instead.
func TestE2EShellGroupedSessionIndependence(t *testing.T) {
	if os.Getenv("LIMA_E2E") == "" {
		t.Skip("set LIMA_E2E=1 (and -tags limae2e) to run the real-Lima e2e tests")
	}
	cli, _, _ := ensureSharedBase(t)

	const name = "sand-e2e-shell-grouped"
	e2eCloneAndStart(t, cli, name)
	argv := e2eAttachArgv(t, cli, name)

	h := newE2EHostTmux(t)
	h.newSession("c1", 200, 50, argv...)
	h.waitForAttach("c1", 30*time.Second)
	t.Cleanup(func() { h.kill("c1") })

	// Deliberately a DIFFERENT size — same-session mirrored clients clamp the
	// display to the smallest attached client; a grouped session must not.
	h.newSession("c2", 100, 30, argv...)
	h.waitForAttach("c2", 30*time.Second)
	t.Cleanup(func() { h.kill("c2") })

	// EXACTLY TWO SESSIONS, SHARING ONE WINDOW SET (same group).
	listing := strings.TrimSpace(e2eGuestOut(t, cli, name, "tmux", "list-sessions", "-F", "#{session_name}:#{session_group}"))
	lines := strings.Split(listing, "\n")
	if len(lines) != 2 {
		t.Fatalf("want exactly 2 guest tmux sessions after two independent attaches, got %d: %q", len(lines), listing)
	}
	var sawMain, sawGrouped bool
	group := ""
	for _, l := range lines {
		parts := strings.SplitN(l, ":", 2)
		if len(parts) != 2 {
			t.Fatalf("unexpected tmux list-sessions line: %q (full: %q)", l, listing)
		}
		sname, grp := parts[0], parts[1]
		if group == "" {
			group = grp
		} else if grp != group {
			t.Fatalf("the two sessions are not in the same tmux group (not sharing a window set): %q", listing)
		}
		switch {
		case sname == "main":
			sawMain = true
		case strings.HasPrefix(sname, "sand-"):
			sawGrouped = true
		}
	}
	if !sawMain || !sawGrouped {
		t.Fatalf("expected one %q session and one grouped %q session, got: %q", "main", "sand-*", listing)
	}

	// INDEPENDENCE: record c1's window and size, have c2 open a NEW window
	// (C-a c, the shipped prefix), and confirm c1 did not follow and was not
	// resized.
	c1WindowBefore := h.display("c1", "#{window_index}")
	c1SizeBefore := h.display("c1", "#{window_width}x#{window_height}")

	h.sendKeys("c2", "C-a", "c")
	time.Sleep(1 * time.Second)

	c1WindowAfter := h.display("c1", "#{window_index}")
	c1SizeAfter := h.display("c1", "#{window_width}x#{window_height}")
	if c1WindowAfter != c1WindowBefore {
		t.Fatalf("client 1's current window moved when client 2 switched windows (%s -> %s) — sessions are mirrored, not grouped", c1WindowBefore, c1WindowAfter)
	}
	if c1SizeAfter != c1SizeBefore {
		t.Fatalf("client 1's display was resized by client 2's activity (%s -> %s) — the display is being clamped, defeating the point of a grouped session", c1SizeBefore, c1SizeAfter)
	}
}

// THE ASSERTION THAT MATTERS MOST. Grouped-session cleanup is asymmetric by
// design: the grouped session gets `destroy-unattached on` (itself), `main`
// never does. Reversing those two lines is silent and catastrophic — it
// converts "your work survives a closed laptop" into "your work dies when you
// look away" — so this is asserted explicitly on `tmux list-sessions` output,
// twice: once right after the SECOND client detaches (grouped gone, main and
// its process alive), and once more after the LAST client detaches too (main
// still alive, unattached).
func TestE2EShellDestroyUnattachedAsymmetry(t *testing.T) {
	if os.Getenv("LIMA_E2E") == "" {
		t.Skip("set LIMA_E2E=1 (and -tags limae2e) to run the real-Lima e2e tests")
	}
	cli, _, _ := ensureSharedBase(t)

	const name = "sand-e2e-shell-destroy"
	e2eCloneAndStart(t, cli, name)
	argv := e2eAttachArgv(t, cli, name)

	h := newE2EHostTmux(t)
	h.newSession("c1", 200, 50, argv...)
	h.waitForAttach("c1", 30*time.Second)

	h.sendKeys("c1", "sleep 90 # sand-e2e-destroy-marker", "Enter")
	time.Sleep(2 * time.Second)

	h.newSession("c2", 100, 30, argv...)
	h.waitForAttach("c2", 30*time.Second)

	// Precondition: both sessions really are up before we test the asymmetry.
	pre := e2eGuestOut(t, cli, name, "tmux", "list-sessions", "-F", "#{session_name}")
	if !strings.Contains(pre, "main") || !strings.Contains(pre, "sand-") {
		t.Fatalf("precondition failed: expected both main and a grouped sand-* session before testing cleanup, got: %q", pre)
	}

	// Detach ONLY the second (grouped) client.
	h.kill("c2")
	time.Sleep(2 * time.Second)

	afterSecondDetach := e2eGuestOut(t, cli, name, "tmux", "list-sessions", "-F", "#{session_name}")
	if strings.Contains(afterSecondDetach, "sand-") {
		t.Fatalf("the grouped session should be gone after its own client detached (destroy-unattached), but tmux list-sessions still shows: %q", afterSecondDetach)
	}
	if !strings.Contains(afterSecondDetach, "main") {
		t.Fatalf("CRITICAL: 'main' is GONE after only the SECOND client detached — destroy-unattached has landed on the wrong session and just destroyed the user's persistent work. tmux list-sessions: %q", afterSecondDetach)
	}
	if out := e2eGuestOut(t, cli, name, "pgrep", "-af", "sleep 90"); !strings.Contains(out, "sleep 90") {
		t.Fatalf("CRITICAL: the marker process died when only the OTHER client's (grouped) session detached — main must never be torn down by a different client's detach. pgrep: %q", out)
	}

	// Detach the LAST client too. `main` itself carries no destroy-unattached,
	// so it must survive even fully unattended.
	h.kill("c1")
	time.Sleep(2 * time.Second)

	final := e2eGuestOut(t, cli, name, "tmux", "list-sessions", "-F", "#{session_name}:#{session_attached}")
	if !strings.Contains(final, "main:0") {
		t.Fatalf("'main' should still exist, unattached, after every client has detached; got: %q", final)
	}
	if out := e2eGuestOut(t, cli, name, "pgrep", "-af", "sleep 90"); !strings.Contains(out, "sleep 90") {
		t.Fatalf("CRITICAL: the marker process died after the LAST client detached — the core persistence guarantee of tmux-backed shells is broken. pgrep: %q", out)
	}
}
