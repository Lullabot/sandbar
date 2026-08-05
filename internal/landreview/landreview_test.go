package landreview

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// This file covers the pieces a Session test cannot: the seams that are
// injected away in cmd/sand's --review tests precisely because they touch the
// real world. The orchestration's branching and teardown guarantees are
// asserted there, over fakes; what is asserted here is that the production
// defaults those fakes stand in for actually behave as claimed.

// --- freePort ---

func TestFreePortReturnsABindableLoopbackPort(t *testing.T) {
	port, err := freePort()
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	if port <= 0 || port > 65535 {
		t.Fatalf("freePort = %d, want a usable TCP port", port)
	}
	// The port must have been RELEASED, not held: the guest server is about
	// to claim this same number, and on local Lima the host and guest ports
	// are necessarily identical.
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("listening on the port freePort just returned: %v", err)
	}
	_ = l.Close()
}

// --- probeHTTP ---

func TestProbeHTTPAcceptsAnyResponse(t *testing.T) {
	// An error status still means "the server is there", which is the only
	// question readiness asks — a guest whose dist/ was not built answers 500
	// and is nonetheless ready to be looked at.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "dist/ not built", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if err := probeHTTP(context.Background(), strings.TrimPrefix(srv.URL, "http://")); err != nil {
		t.Fatalf("probeHTTP against a live server: %v, want nil", err)
	}
}

func TestProbeHTTPFailsWhenNothingIsListening(t *testing.T) {
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	if err := probeHTTP(context.Background(), "127.0.0.1:"+strconv.Itoa(port)); err == nil {
		t.Fatal("probeHTTP against a closed port returned nil, want an error")
	}
}

// TestProbeHTTPRejectsAListenerThatNeverAnswers is the reason the prober does
// a request instead of a dial. `ssh -N -L` binds the workstation's socket the
// moment it connects, so a TCP connect succeeds long before anything is
// listening in the guest — a dial-only probe would call that ready and open a
// browser onto a broken connection. Both shapes an unready forward can take
// are covered: one that accepts and then hangs, and one that accepts and then
// drops the connection (what ssh does once the far end refuses).
func TestProbeHTTPRejectsAListenerThatNeverAnswers(t *testing.T) {
	cases := []struct {
		name  string
		serve func(c net.Conn)
	}{
		{name: "accepts and hangs", serve: func(c net.Conn) { time.Sleep(2 * time.Second); _ = c.Close() }},
		{name: "accepts and drops", serve: func(c net.Conn) { _ = c.Close() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			go func() {
				for {
					c, err := l.Accept()
					if err != nil {
						return
					}
					go tc.serve(c)
				}
			}()

			// A short parent deadline so the hanging case does not spend the
			// production probe budget; probeHTTP's own timeout is a ceiling,
			// not a floor.
			ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
			defer cancel()
			if err := probeHTTP(ctx, l.Addr().String()); err == nil {
				t.Error("probeHTTP reported ready against a socket that never answered — a bare dial would have, which is exactly the bug this guards")
			}
		})
	}
}

// --- startForwardChild ---

// TestStartForwardChildKillsAndReapsTheChild is the anti-orphan guarantee at
// the level that actually spawns a process. stop() returning is only
// meaningful because it waits on cmd.Wait(), which the OS answers solely once
// the child is gone — so a stop() that returns in well under the child's own
// 60-second lifetime IS the proof that it was killed and reaped, with no pid
// probing (and no platform assumption) needed.
func TestStartForwardChildKillsAndReapsTheChild(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh binary; this test needs a real long-lived child")
	}

	var out lockedBuffer
	// `exec sleep` so the process the child context kills is the sleeping one
	// itself, not a shell holding it.
	stop, err := startForwardChild(context.Background(), []string{sh, "-c", "echo forwarding; exec sleep 60"}, &out)
	if err != nil {
		t.Fatalf("startForwardChild: %v", err)
	}

	// Wait for the child to prove it is really running before killing it,
	// so a start that silently failed cannot pass this test.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "forwarding") {
		if time.Now().After(deadline) {
			t.Fatalf("the forwarder child never produced output; got %q", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	start := time.Now()
	stop()
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("stop() took %s; it must kill the forwarder, not wait out its 60s lifetime", elapsed)
	}
}

func TestStartForwardChildReportsAStartFailure(t *testing.T) {
	if _, err := startForwardChild(context.Background(), []string{filepath.Join(t.TempDir(), "no-such-binary")}, os.Stderr); err == nil {
		t.Error("startForwardChild with a nonexistent binary returned nil, want an error")
	}
	if _, err := startForwardChild(context.Background(), nil, os.Stderr); err == nil {
		t.Error("startForwardChild with an empty argv returned nil, want an error")
	}
}

// --- the merge-base script, against real git ---

func requireTools(t *testing.T, bins ...string) {
	t.Helper()
	for _, bin := range bins {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available; the merge-base script needs it", bin)
		}
	}
}

// gitEnv is a deterministic, user-config-free environment for the throwaway
// repos below, so a developer's own git config cannot change what these
// assert — the same isolation internal/checkouts' sweep integration test uses.
func gitEnv(home string) []string {
	return append(os.Environ(),
		"HOME="+home,
		"GIT_CONFIG_GLOBAL="+filepath.Join(home, ".gitconfig-absent"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(home, ".gitconfig-absent-system"),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
}

func git(t *testing.T, dir, home string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv(home)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s in %s: %v", strings.Join(args, " "), dir, err)
	}
	return strings.TrimSpace(string(out))
}

// runMergeBase executes the REAL script the guest runs, with the checkout
// path and default branch arriving as positional arguments exactly as
// Provider.ShellOut delivers them.
func runMergeBase(t *testing.T, home, repo, defaultBranch string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", mergeBaseScript, "sh", repo, defaultBranch)
	cmd.Env = gitEnv(home)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("merge-base script: %v", err)
	}
	return lastObjectID(string(out))
}

// TestMergeBaseScriptAgainstRealGit runs the script the guest actually runs
// against real repositories. A synthetic test could only assert the answers
// the script was ASSUMED to give; this pins the ones git really produces —
// most importantly that the base is the point the branch diverged, and that
// `git diff <base>` from it therefore covers committed AND uncommitted work,
// which is the whole reason a two-dot base is passed rather than a three-dot
// range.
func TestMergeBaseScriptAgainstRealGit(t *testing.T) {
	requireTools(t, "git", "sh")

	home := t.TempDir()
	remote := filepath.Join(home, "remote.git")
	work := filepath.Join(home, "work")
	git(t, home, home, "init", "-q", "--bare", "-b", "main", remote)
	git(t, home, home, "init", "-q", "-b", "main", work)
	git(t, work, home, "remote", "add", "origin", remote)

	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("a.txt", "base\n")
	git(t, work, home, "add", "a.txt")
	git(t, work, home, "commit", "-q", "-m", "base")
	git(t, work, home, "push", "-q", "origin", "main")
	mainTip := git(t, work, home, "rev-parse", "HEAD")

	// A feature branch with one commit on top, plus an uncommitted edit —
	// the ordinary shape of work in a sandbox. main moves on afterwards, so
	// a merge BASE and main's current tip are genuinely different commits and
	// the test cannot pass by reading the wrong one.
	git(t, work, home, "checkout", "-q", "-b", "feature")
	write("a.txt", "base\nfeature\n")
	git(t, work, home, "commit", "-q", "-am", "feature work")

	git(t, work, home, "checkout", "-q", "main")
	write("other.txt", "landed elsewhere\n")
	git(t, work, home, "add", "other.txt")
	git(t, work, home, "commit", "-q", "-m", "unrelated main commit")
	git(t, work, home, "push", "-q", "origin", "main")
	git(t, work, home, "checkout", "-q", "feature")

	// The uncommitted edit comes last, so the branch switches above run
	// against a clean tree.
	write("a.txt", "base\nfeature\nwip\n")

	got := runMergeBase(t, home, work, "main")
	if got != mainTip {
		t.Fatalf("merge base = %q, want the commit the branch diverged at, %q", got, mainTip)
	}

	// The load-bearing consequence: diffing from that base against the
	// WORKING TREE shows both the branch's commit and the uncommitted edit,
	// and nothing that landed on main in the meantime.
	diff := git(t, work, home, "diff", got)
	if !strings.Contains(diff, "+feature") || !strings.Contains(diff, "+wip") {
		t.Errorf("git diff %s missed committed and/or uncommitted work:\n%s", got, diff)
	}
	if strings.Contains(diff, "other.txt") {
		t.Errorf("git diff %s included an unrelated commit that landed on main:\n%s", got, diff)
	}

	// The sweep's default-branch field can be empty (a clone whose
	// origin/HEAD was never set); the script's own main/master fallback must
	// still find the same answer.
	if got := runMergeBase(t, home, work, ""); got != mainTip {
		t.Errorf("merge base with no default branch given = %q, want the same %q via the main fallback", got, mainTip)
	}
}

// A repo with no remote and no main/master to compare against — and a path
// that is not a repo at all — must yield NO base, so the session falls back
// to the server's own default rather than failing the review.
func TestMergeBaseScriptYieldsNothingWhenThereIsNoBase(t *testing.T) {
	requireTools(t, "git", "sh")

	home := t.TempDir()
	solo := filepath.Join(home, "solo")
	git(t, home, home, "init", "-q", "-b", "scratch", solo)
	if err := os.WriteFile(filepath.Join(solo, "a.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, solo, home, "add", "a.txt")
	git(t, solo, home, "commit", "-q", "-m", "only commit")

	if got := runMergeBase(t, home, solo, ""); got != "" {
		t.Errorf("merge base for a remote-less repo on a non-default branch = %q, want none", got)
	}
	if got := runMergeBase(t, home, filepath.Join(home, "not-a-repo"), "main"); got != "" {
		t.Errorf("merge base for a path that is not a repo = %q, want none", got)
	}
}

// --- the guest stop script, against a real listening process ---

// TestStopServerScriptKillsOnlyTheReviewServer runs the REAL teardown script
// against real processes. It is the anti-orphan guarantee's guest half, and
// the half a fake Provider cannot check at all: whether `ss` + /proc really
// finds the process holding a port, and — the part that matters more —
// whether it declines to signal one that merely happens to hold it.
//
// Linux-only by construction (it reads /proc and uses iproute2's `ss`), which
// is what the guest always is; it skips elsewhere rather than pretending to
// be a portability claim.
func TestStopServerScriptKillsOnlyTheReviewServer(t *testing.T) {
	requireTools(t, "sh", "ss", "python3")
	if _, err := os.Stat("/proc/self/cmdline"); err != nil {
		t.Skip("no /proc; the stop script identifies the listener through it")
	}

	// listen starts a process that holds a port until killed, with marker as
	// an extra argv element so its /proc cmdline decides whether the script
	// considers it the review server.
	listen := func(t *testing.T, marker string) (int, *exec.Cmd) {
		t.Helper()
		port, err := freePort()
		if err != nil {
			t.Fatal(err)
		}
		const script = `import socket, sys, time
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", int(sys.argv[1])))
s.listen()
print("listening", flush=True)
time.sleep(120)`
		cmd := exec.Command("python3", "-c", script, strconv.Itoa(port), marker)
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		buf := make([]byte, len("listening\n"))
		if _, err := io.ReadFull(out, buf); err != nil {
			t.Fatalf("the fixture listener never came up: %v", err)
		}
		return port, cmd
	}

	run := func(t *testing.T, port int) {
		t.Helper()
		cmd := exec.Command("sh", "-c", stopServerScript, "sh", strconv.Itoa(port))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("stop script: %v\n%s", err, out)
		}
	}

	waitExit := func(t *testing.T, cmd *exec.Cmd) error {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			return nil // still alive; the caller decides whether that is wrong
		}
	}

	t.Run("kills the review server holding the port", func(t *testing.T) {
		port, cmd := listen(t, "/opt/sandbar/self-review/server/index.mjs")
		run(t, port)
		if err := waitExit(t, cmd); err == nil {
			t.Error("the review server survived the stop script — this is the orphan the script exists to prevent")
		}
	})

	t.Run("leaves an unrelated process holding the port alone", func(t *testing.T) {
		port, cmd := listen(t, "some-unrelated-guest-service")
		run(t, port)
		// A short wait is enough: the signal would have landed immediately.
		time.Sleep(500 * time.Millisecond)
		if cmd.ProcessState != nil {
			t.Error("the stop script signalled a process that is not the review server")
		}
		if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
			t.Errorf("the unrelated process holding the port is gone: %v", err)
		}
	})

	t.Run("is a harmless no-op when nothing is listening", func(t *testing.T) {
		port, err := freePort()
		if err != nil {
			t.Fatal(err)
		}
		run(t, port) // must exit 0, which run() already asserts
	})
}

// --- pure helpers ---

func TestLastObjectIDIgnoresNoise(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "bare sha", in: sha + "\n", want: sha},
		{name: "login noise before the answer", in: "Welcome to Debian\nLast login: today\n" + sha + "\n", want: sha},
		{name: "nothing to report", in: "\n\n", want: ""},
		{name: "an option-shaped answer is refused", in: "--output=/etc/passwd\n", want: ""},
		{name: "a ref name is refused", in: "origin/main\n", want: ""},
		{name: "too short to be an object name", in: "abc\n", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lastObjectID(tc.in); got != tc.want {
				t.Errorf("lastObjectID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSessionSeamDefaults(t *testing.T) {
	var s Session
	if s.readyTimeout() != defaultReadyTimeout {
		t.Errorf("readyTimeout with no override = %s, want %s", s.readyTimeout(), defaultReadyTimeout)
	}
	if s.pollInterval() != defaultPollInterval {
		t.Errorf("pollInterval with no override = %s, want %s", s.pollInterval(), defaultPollInterval)
	}
	s.ReadyTimeout, s.PollInterval = time.Second, time.Millisecond
	if s.readyTimeout() != time.Second || s.pollInterval() != time.Millisecond {
		t.Errorf("an explicit timeout/interval was ignored: %s/%s", s.readyTimeout(), s.pollInterval())
	}
}

func TestDetailAppendsOnlyWhatWasSaid(t *testing.T) {
	if got := detail("", "   \n"); got != "" {
		t.Errorf("detail with nothing to report = %q, want empty", got)
	}
	if got := detail("node: not found\n", "", "ssh: permission denied"); got != "\nnode: not found\nssh: permission denied" {
		t.Errorf("detail = %q, want both non-empty outputs on their own lines", got)
	}
}

func TestDescribeBaseNamesTheRange(t *testing.T) {
	if got := describeBase(""); !strings.Contains(got, "working tree") {
		t.Errorf("describeBase(\"\") = %q, want it to say the working tree is being reviewed", got)
	}
	const sha = "0123456789abcdef0123456789abcdef01234567"
	got := describeBase(sha)
	if !strings.Contains(got, sha[:12]) || strings.Contains(got, sha) {
		t.Errorf("describeBase(%q) = %q, want an abbreviated object name", sha, got)
	}
}
