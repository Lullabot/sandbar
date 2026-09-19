package landreview

import (
	"context"
	"errors"
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
	// The port must have been RELEASED, not held: an ssh -L is about to bind
	// this same number as the near end of the forward.
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("listening on the port freePort just returned: %v", err)
	}
	_ = l.Close()
}

// --- probeHTTP ---

// reviewServerStub answers probePath the way @self-review/serve does. The
// shape is taken from a real 1.45.0 response, trimmed to the part the probe
// actually reads.
func reviewServerStub() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != probePath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"config":{"theme":"system","outputFile":"./review.xml"}}`))
	})
}

func TestProbeHTTPAcceptsTheReviewServer(t *testing.T) {
	srv := httptest.NewServer(reviewServerStub())
	defer srv.Close()

	if err := probeHTTP(context.Background(), strings.TrimPrefix(srv.URL, "http://")); err != nil {
		t.Fatalf("probeHTTP against a live review server: %v, want nil", err)
	}
}

// TestProbeHTTPRejectsAForeignListener covers the port-collision hazard.
// Lima's auto-forward lands a guest loopback port on the SAME number on the
// host, and nothing reserves that number there. When something else already
// holds it, the connection reaches that other process — and a probe satisfied
// by any HTTP response would declare readiness, open the reviewer's browser
// onto an unrelated application, then block forever waiting for a submission
// that could never come.
//
// The plain HTML case is why asking for `/` was not enough: any number of
// things serve a page there.
func TestProbeHTTPRejectsAForeignListener(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "a healthy but entirely unrelated web application",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("<html>some other app</html>"))
			},
		},
		{
			name: "an unrelated JSON API that happens to answer this route",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"ok","version":3}`))
			},
		},
		{
			name: "something that refuses the route outright",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "forbidden", http.StatusForbidden)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			err := probeHTTP(context.Background(), strings.TrimPrefix(srv.URL, "http://"))
			if err == nil {
				t.Fatal("probeHTTP accepted a listener that is not the review server — the browser would open onto the wrong application and the session would hang forever")
			}
			if !strings.Contains(err.Error(), "another process holds this port") {
				t.Fatalf("probeHTTP error = %v, want it to name the collision plainly", err)
			}
		})
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
		port, cmd := listen(t, "/opt/sandbar/self-review/node_modules/.bin/self-review-serve")
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

// TestWrittenPathPrefersWhatTheServerAnnounced covers the output-path fidelity
// defect: a project's .self-review.yaml can point outputFile anywhere, and this
// side used to report <checkout>/review.xml regardless — naming a file that did
// not exist, which matters because the docs tell the user to point their agent
// at that path.
func TestWrittenPathPrefersWhatTheServerAnnounced(t *testing.T) {
	for _, tc := range []struct {
		name      string
		serverOut string
		want      string
	}{
		{
			name:      "a configured outputFile is honoured",
			serverOut: "[serve] Output path: /src/repo/reviews/latest.xml\n[serve] Review written to /src/repo/reviews/latest.xml\n",
			want:      "/src/repo/reviews/latest.xml",
		},
		{
			name: "the completion line wins over the startup announcement",
			// They agree in practice, but only one of them is a statement
			// about what actually happened.
			serverOut: "[serve] Output path: /src/repo/review.xml\n[serve] Review written to /src/repo/elsewhere.xml\n",
			want:      "/src/repo/elsewhere.xml",
		},
		{
			name: "a torn-down server still reports where it was going to write",
			// No completion line: the process was killed mid-review. The
			// startup announcement is the best answer available, and it is a
			// far better one than the checkout default when the project has
			// redirected outputFile.
			serverOut: "[serve] Output path: /src/repo/reviews/latest.xml\n[serve] Review ready at http://127.0.0.1:41234/\n",
			want:      "/src/repo/reviews/latest.xml",
		},
		{
			name:      "no marker at all falls back to the documented default",
			serverOut: "[serve] Startup mode: directory\n",
			want:      "/src/repo/review.xml",
		},
		{
			name:      "login-shell noise before the marker does not hide it",
			serverOut: "Welcome to Debian\nMOTD line\n[serve] Review written to /src/repo/review.xml\n",
			want:      "/src/repo/review.xml",
		},
		{
			name:      "an empty path is ignored rather than reported as \"\"",
			serverOut: "[serve] Review written to \n",
			want:      "/src/repo/review.xml",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := writtenPath(tc.serverOut, "/src/repo"); got != tc.want {
				t.Fatalf("writtenPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- the readiness banner, which is the only way the port is learned ---

// TestParseServePortReadsUpstreamsBanner pins the one string this side
// depends on upstream printing. Upstream has no --port flag, so if this line
// ever changes shape the review command stops working entirely — the test
// exists so that breaks here, loudly, rather than as a 30s timeout in a VM.
func TestParseServePortReadsUpstreamsBanner(t *testing.T) {
	// Verbatim from @self-review/serve 1.45.0, in the order it prints them.
	const real = "[serve] Output path: /work/repo/review.xml\n" +
		"[serve] Startup mode: git\n" +
		"[serve] Loaded 3 files\n" +
		"[serve] Review ready at http://127.0.0.1:33749/\n" +
		"[serve] Completing the review writes /work/repo/review.xml and stops this process.\n" +
		"[serve] The listener is loopback-only and unauthenticated.\n"

	for _, tc := range []struct {
		name string
		out  string
		want int
	}{
		{"the real banner", real, 33749},
		{"nothing printed yet", "", 0},
		{"started but not yet listening", "[serve] Output path: /work/repo/review.xml\n", 0},
		{"login-shell noise ahead of it does not hide it",
			"Welcome to Debian\n[serve] Review ready at http://127.0.0.1:41234/\n", 41234},
		{"a partial line is not a port yet", "[serve] Review ready at http://127.0.0.1:", 0},
		// A host this side cannot bridge must not be read as reachable.
		{"a non-loopback bind is not accepted",
			"[serve] Review ready at http://0.0.0.0:41234/\n", 0},
		// Refused rather than trusted: this number is about to be used to
		// build a forward.
		{"an out-of-range port is refused",
			"[serve] Review ready at http://127.0.0.1:99999/\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseServePort(tc.out); got != tc.want {
				t.Fatalf("parseServePort() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAwaitGuestPortReturnsAsSoonAsTheBannerLands(t *testing.T) {
	s := &Session{ReadyTimeout: 2 * time.Second, PollInterval: time.Millisecond}
	var buf lockedBuffer
	exited := make(chan struct{})

	go func() {
		time.Sleep(10 * time.Millisecond)
		_, _ = buf.Write([]byte("[serve] Review ready at http://127.0.0.1:41234/\n"))
	}()

	port, err := s.awaitGuestPort(context.Background(), &buf, exited)
	if err != nil {
		t.Fatalf("awaitGuestPort: %v", err)
	}
	if port != 41234 {
		t.Fatalf("awaitGuestPort = %d, want 41234", port)
	}
}

// A server that announces and then immediately dies still told us its port,
// and teardown needs that number to stop it in the guest. Losing the race to
// the exit signal must not throw the answer away.
func TestAwaitGuestPortPrefersALateBannerOverTheExit(t *testing.T) {
	s := &Session{ReadyTimeout: time.Second, PollInterval: 5 * time.Millisecond}
	var buf lockedBuffer
	_, _ = buf.Write([]byte("[serve] Review ready at http://127.0.0.1:41234/\n"))
	exited := make(chan struct{})
	close(exited) // the command has ALREADY finished

	port, err := s.awaitGuestPort(context.Background(), &buf, exited)
	if err != nil {
		t.Fatalf("awaitGuestPort: %v, want the port it plainly announced", err)
	}
	if port != 41234 {
		t.Fatalf("awaitGuestPort = %d, want 41234", port)
	}
}

func TestAwaitGuestPortReportsASilentExit(t *testing.T) {
	s := &Session{ReadyTimeout: time.Second, PollInterval: time.Millisecond}
	var buf lockedBuffer
	exited := make(chan struct{})
	close(exited)

	if _, err := s.awaitGuestPort(context.Background(), &buf, exited); !errors.Is(err, errServerGone) {
		t.Fatalf("awaitGuestPort error = %v, want errServerGone", err)
	}
}

func TestAwaitGuestPortGivesUp(t *testing.T) {
	s := &Session{ReadyTimeout: 30 * time.Millisecond, PollInterval: time.Millisecond}
	var buf lockedBuffer

	_, err := s.awaitGuestPort(context.Background(), &buf, make(chan struct{}))
	if err == nil || errors.Is(err, errServerGone) {
		t.Fatalf("awaitGuestPort error = %v, want a plain timeout", err)
	}
}

func TestAwaitGuestPortHonoursCancellation(t *testing.T) {
	s := &Session{ReadyTimeout: 10 * time.Second, PollInterval: time.Millisecond}
	var buf lockedBuffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.awaitGuestPort(ctx, &buf, make(chan struct{})); !errors.Is(err, context.Canceled) {
		t.Fatalf("awaitGuestPort error = %v, want context.Canceled", err)
	}
}

// unreachableHint speaks only to the backend it can say something true about.
func TestUnreachableHintOnlyExplainsTheSamePortCase(t *testing.T) {
	if got := unreachableHint(41234, 41234); !strings.Contains(got, "41234") {
		t.Errorf("unreachableHint(same, same) = %q, want it to explain the collision", got)
	}
	if got := unreachableHint(45123, 41234); got != "" {
		t.Errorf("unreachableHint(different) = %q, want nothing — a forwarded backend picked its own near end", got)
	}
}
