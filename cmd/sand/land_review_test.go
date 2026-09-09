package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/checkouts"
	"github.com/lullabot/sandbar/internal/landreview"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/vm"
)

// reviewHarness drives the whole --review action through fakes: a
// providerfake.Provider standing in for the backend seam (per AGENTS.md,
// consumers of provider.Provider fake the interface itself), the existing
// fakeGh double as the browser opener, and injected port/probe/forwarder
// funcs. Nothing here starts a VM, an ssh, a browser or a listening socket.
//
// The fake guest server blocks until the test releases it — that is what
// makes "the user finished the review" and "the user pressed ctrl-C"
// expressible as two different releases of the same seam.
type reviewHarness struct {
	mu sync.Mutex

	// serverCtx is the context Run derived for the guest command. The
	// teardown criterion is asserted against it directly: after Run returns
	// it must be Done on EVERY exit path, success included.
	serverCtx  context.Context
	serverArgv []string
	// shellOuts is every one-shot guest command the session ran: the
	// merge-base probe, and (only on an abnormal exit) the guest-side stop.
	shellOuts [][]string

	forwardArgv  []string
	forwardStops int

	// serverBody runs on the fake Shell's goroutine and decides when (and
	// how) the guest server "exits". The default blocks until ctx is done.
	serverBody func(ctx context.Context, out io.Writer) error

	// probeReady gates the readiness prober: nil means "ready immediately".
	probeReady func() bool

	// mergeBase is what the guest's merge-base probe reports (empty = none).
	mergeBase string

	release chan struct{} // closed to let the default serverBody return nil
}

func newReviewHarness() *reviewHarness {
	return &reviewHarness{release: make(chan struct{}), mergeBase: strings.Repeat("a1b2c3d4", 5)}
}

// provider builds the fake backend. forwardArgv is what ForwardArgv reports:
// nil models local Lima (already reachable), non-nil models remote Lima or
// Proxmox (an ssh -L child to start).
func (h *reviewHarness) provider(forwardArgv []string) *providerfake.Provider {
	return &providerfake.Provider{
		ShellFunc: func(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error {
			h.mu.Lock()
			h.serverCtx = ctx
			h.serverArgv = append([]string(nil), argv...)
			body := h.serverBody
			h.mu.Unlock()

			if body != nil {
				return body(ctx, out)
			}
			select {
			case <-h.release:
				return nil // the reviewer pressed Finish Review; the server exits 0
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		ShellOutFunc: func(ctx context.Context, name string, argv ...string) ([]byte, error) {
			h.mu.Lock()
			h.shellOuts = append(h.shellOuts, append([]string(nil), argv...))
			base := h.mergeBase
			h.mu.Unlock()
			// The guest-side stop runs during teardown, when the caller's
			// context is typically ALREADY cancelled (that is what ctrl-C
			// does). Refusing a cancelled context here is what makes the
			// detached-context requirement testable.
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return []byte(base + "\n"), nil
		},
		ForwardArgvFunc: func(_ vm.VM, hostPort, guestPort int) []string {
			if forwardArgv == nil {
				return nil
			}
			return append(append([]string(nil), forwardArgv...), fmt.Sprintf("%d:127.0.0.1:%d", hostPort, guestPort))
		},
	}
}

// session wires a Session over the harness with every real-world seam
// replaced. The timings are deliberately tiny so a readiness-timeout test
// costs milliseconds rather than the production 30s.
func (h *reviewHarness) session(p *providerfake.Provider, gh *fakeGh, co checkouts.Checkout) *landreview.Session {
	return &landreview.Session{
		Provider: p,
		VM:       vm.VM{Name: "box"},
		Checkout: co,
		Open:     gh.OpenInBrowser,
		PickPort: func() (int, error) { return 45123, nil },
		Probe: func(context.Context, string) error {
			h.mu.Lock()
			ready := h.probeReady
			h.mu.Unlock()
			if ready == nil || ready() {
				return nil
			}
			return errors.New("connection refused")
		},
		StartForward: func(_ context.Context, argv []string, _ io.Writer) (func(), error) {
			h.mu.Lock()
			h.forwardArgv = append([]string(nil), argv...)
			h.mu.Unlock()
			return func() {
				h.mu.Lock()
				h.forwardStops++
				h.mu.Unlock()
			}, nil
		},
		ReadyTimeout: 150 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
	}
}

func (h *reviewHarness) argv() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.serverArgv...)
}

func (h *reviewHarness) stops() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.forwardStops
}

// guestStops returns the one-shot guest commands that carry the review port —
// the guest-side half of teardown, which kills a server the host can no
// longer reach through the child it just cancelled.
func (h *reviewHarness) guestStops(port string) [][]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var found [][]string
	for _, argv := range h.shellOuts {
		for _, a := range argv {
			if a == port {
				found = append(found, argv)
				break
			}
		}
	}
	return found
}

// assertServerTornDown is the teardown criterion: the guest command's context
// must be cancelled by the time Run returns, so no server child outlives the
// call on any exit path.
func (h *reviewHarness) assertServerTornDown(t *testing.T) {
	t.Helper()
	h.mu.Lock()
	ctx := h.serverCtx
	h.mu.Unlock()
	if ctx == nil {
		t.Fatal("the guest review server was never started")
	}
	select {
	case <-ctx.Done():
	default:
		t.Error("the guest server's context is still live after landReview returned — the server child would be orphaned")
	}
}

func reviewCheckout(path string) checkouts.Checkout {
	return checkouts.Checkout{
		Path:          path,
		Kind:          checkouts.KindRepo,
		Branch:        "feature-x",
		DefaultBranch: "main",
		PushState:     checkouts.PushStateNever, // --review needs no pushed branch
	}
}

// --- happy path ---

func TestReviewHappyPathOnALocalBackend(t *testing.T) {
	h := newReviewHarness()
	gh := &fakeGh{}
	sess := h.session(h.provider(nil /* local Lima: nothing to forward */), gh, reviewCheckout("/home/dev/proj"))

	// The browser "opening" is the cue that the reviewer can finish: release
	// the fake server from inside the opener so the whole flow runs to a
	// natural completion without a sleep anywhere.
	realOpen := sess.Open
	sess.Open = func(ctx context.Context, url string) error {
		close(h.release)
		return realOpen(ctx, url)
	}

	var out strings.Builder
	if err := landReview(context.Background(), &out, sess); err != nil {
		t.Fatalf("landReview: unexpected error: %v", err)
	}

	if want := "http://127.0.0.1:45123"; len(gh.openCalls) != 1 || gh.openCalls[0] != want {
		t.Errorf("OpenInBrowser calls = %v, want exactly [%q]", gh.openCalls, want)
	}
	if want := "/home/dev/proj/review.xml"; !strings.Contains(out.String(), want) {
		t.Errorf("landReview output = %q, want it to report the guest path %q", out.String(), want)
	}
	if h.stops() != 0 {
		t.Errorf("forwarder stopped %d times, want 0 — a nil ForwardArgv means there is nothing to start", h.stops())
	}
	h.assertServerTornDown(t)
}

// --- the guest command is argv, never a shell string ---

func TestReviewGuestArgvIsDiscreteElements(t *testing.T) {
	h := newReviewHarness()
	gh := &fakeGh{}
	// A path a sweep could plausibly return and a shell would mangle: the
	// point of argv is that neither the space nor the metacharacters mean
	// anything to anyone.
	const nasty = "/home/dev/my repo; touch /tmp/pwned"
	sess := h.session(h.provider(nil), gh, reviewCheckout(nasty))
	realOpen := sess.Open
	sess.Open = func(ctx context.Context, url string) error {
		close(h.release)
		return realOpen(ctx, url)
	}

	if err := landReview(context.Background(), io.Discard, sess); err != nil {
		t.Fatalf("landReview: unexpected error: %v", err)
	}

	got := h.argv()
	want := []string{
		"node", "/opt/sandbar/self-review/server/index.mjs",
		"--repo", nasty,
		"--port", "45123",
		"--diff-args", strings.Repeat("a1b2c3d4", 5),
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("guest server argv =\n  %#v\nwant\n  %#v", got, want)
	}
	// The path must be ONE element, verbatim — not quoted, not escaped, not
	// concatenated into a neighbouring element.
	for _, a := range got {
		if a != nasty && strings.Contains(a, "my repo") {
			t.Errorf("argv element %q has the checkout path spliced into it; it must travel alone", a)
		}
	}
	// And no shell is invoked to launch the server at all.
	if got[0] == "sh" || got[0] == "bash" || got[0] == "sh -c" {
		t.Errorf("guest server argv[0] = %q, want the interpreter directly — never a shell", got[0])
	}

	// The merge-base probe follows the same rule: a fixed literal script with
	// the path and branch as positional arguments.
	h.mu.Lock()
	probe := append([]string(nil), h.shellOuts[0]...)
	h.mu.Unlock()
	if len(probe) < 3 || probe[0] != "sh" || probe[1] != "-c" {
		t.Fatalf("merge-base probe argv = %#v, want an `sh -c <script>` form", probe)
	}
	if strings.Contains(probe[2], nasty) {
		t.Errorf("merge-base script has the checkout path interpolated into it:\n%s", probe[2])
	}
	if probe[len(probe)-2] != nasty || probe[len(probe)-1] != "main" {
		t.Errorf("merge-base probe positional args = %#v, want the path and default branch as the last two elements", probe[len(probe)-2:])
	}
}

// --- forwarded backends ---

func TestReviewForwardedBackendStartsAndStopsTheForwarder(t *testing.T) {
	h := newReviewHarness()
	gh := &fakeGh{}
	sess := h.session(h.provider([]string{"ssh", "-N", "-L"}), gh, reviewCheckout("/home/dev/proj"))
	realOpen := sess.Open
	sess.Open = func(ctx context.Context, url string) error {
		close(h.release)
		return realOpen(ctx, url)
	}

	if err := landReview(context.Background(), io.Discard, sess); err != nil {
		t.Fatalf("landReview: unexpected error: %v", err)
	}

	h.mu.Lock()
	fwd := append([]string(nil), h.forwardArgv...)
	h.mu.Unlock()
	// Host port and guest port are the SAME number, because local Lima's own
	// forward is same-port and sand keeps one rule for all three backends.
	want := []string{"ssh", "-N", "-L", "45123:127.0.0.1:45123"}
	if strings.Join(fwd, " ") != strings.Join(want, " ") {
		t.Errorf("forwarder argv = %v, want %v", fwd, want)
	}
	if h.stops() != 1 {
		t.Errorf("forwarder stopped %d times, want exactly 1", h.stops())
	}
	h.assertServerTornDown(t)
}

// --- teardown on every exit path ---

// TestReviewTearsDownOnEveryExitPath is the anti-orphan criterion, asserted
// against all three ways the action can end. In each case the forwarder child
// must be killed and the guest command's context cancelled by the time
// landReview returns.
func TestReviewTearsDownOnEveryExitPath(t *testing.T) {
	cases := []struct {
		name    string
		wantErr string
		// arrange mutates the harness/session for this exit path and returns
		// the context landReview is called with.
		arrange func(t *testing.T, h *reviewHarness, sess *landreview.Session) context.Context
	}{
		{
			name: "success: the reviewer submits and the server exits 0",
			arrange: func(_ *testing.T, h *reviewHarness, sess *landreview.Session) context.Context {
				realOpen := sess.Open
				sess.Open = func(ctx context.Context, url string) error {
					close(h.release)
					return realOpen(ctx, url)
				}
				return context.Background()
			},
		},
		{
			name:    "readiness timeout: the server never answers",
			wantErr: "--with-review",
			arrange: func(_ *testing.T, h *reviewHarness, _ *landreview.Session) context.Context {
				h.mu.Lock()
				h.probeReady = func() bool { return false }
				h.mu.Unlock()
				return context.Background()
			},
		},
		{
			name:    "ctrl-C: the context is cancelled mid-review",
			wantErr: "cancel",
			arrange: func(_ *testing.T, h *reviewHarness, sess *landreview.Session) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				realOpen := sess.Open
				// Cancel once the browser is open — the moment a real ctrl-C
				// is most likely, and the only point at which the action is
				// otherwise blocked indefinitely.
				sess.Open = func(c context.Context, url string) error {
					defer cancel()
					return realOpen(c, url)
				}
				return ctx
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newReviewHarness()
			gh := &fakeGh{}
			sess := h.session(h.provider([]string{"ssh", "-N", "-L"}), gh, reviewCheckout("/home/dev/proj"))
			ctx := tc.arrange(t, h, sess)

			err := landReview(ctx, io.Discard, sess)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("landReview: unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("landReview error = %v, want it to mention %q", err, tc.wantErr)
			}

			if h.stops() != 1 {
				t.Errorf("forwarder stopped %d times, want exactly 1 — a surviving ssh child holds the workstation port open", h.stops())
			}
			h.assertServerTornDown(t)

			// Cancelling the guest command's context is necessary but NOT
			// sufficient, and this is measured behaviour on a real Lima VM
			// rather than caution: `limactl shell` forks an ssh child that
			// survives the kill, so the guest server goes on listening (and
			// Lima goes on forwarding its port) after the CLI has exited.
			// Every abnormal exit must therefore also reach into the guest
			// and stop it — and the successful one must NOT, because there
			// the server has already exited on its own and the round trip
			// would be pure latency on the path users actually take.
			stops := h.guestStops("45123")
			if tc.wantErr == "" {
				if len(stops) != 0 {
					t.Errorf("guest stop commands = %v, want none after a submitted review", stops)
				}
				return
			}
			if len(stops) != 1 {
				t.Fatalf("guest stop commands = %v, want exactly one carrying the review port", stops)
			}
		})
	}
}

// --- failure modes ---

// A server that dies on startup — no node, or a VM built without
// --with-review, so the module does not exist — must surface ITS OWN message
// straight away instead of the reader waiting out the readiness timeout for a
// generic "not reachable".
func TestReviewServerExitFailureBeatsTheReadinessTimeout(t *testing.T) {
	h := newReviewHarness()
	h.probeReady = func() bool { return false }
	h.serverBody = func(context.Context, io.Writer) error {
		return errors.New("bash: line 1: node: command not found")
	}
	gh := &fakeGh{}
	sess := h.session(h.provider(nil), gh, reviewCheckout("/home/dev/proj"))
	// A generous readiness budget: if the exit is not noticed, this test
	// takes 10s instead of milliseconds, which is the failure it guards.
	sess.ReadyTimeout = 10 * time.Second

	start := time.Now()
	err := landReview(context.Background(), io.Discard, sess)
	if err == nil {
		t.Fatal("landReview: want an error when the guest server exits before it is ready")
	}
	if !strings.Contains(err.Error(), "node: command not found") {
		t.Errorf("landReview error = %v, want it to carry the guest server's own message", err)
	}
	// A server that dies on startup is overwhelmingly a base image built
	// without the web app, so this arm has to name the flag too — not only
	// the readiness timeout, which is the arm a reader reaches LAST.
	if !strings.Contains(err.Error(), "--with-review") {
		t.Errorf("landReview error = %v, want it to name `sand create --with-review` as the likely cause", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("landReview took %s to notice the server had exited; it must not wait out the readiness timeout", elapsed)
	}
	if len(gh.openCalls) != 0 {
		t.Errorf("OpenInBrowser calls = %v, want none — nothing was ever listening", gh.openCalls)
	}
}

// A server that exits 0 without ever listening is still a failure — an exit
// status of zero must not be reported as an empty success, and the message
// has to say what happened rather than print a nil error.
func TestReviewServerExitingCleanlyBeforeReadyIsStillAFailure(t *testing.T) {
	h := newReviewHarness()
	h.probeReady = func() bool { return false }
	h.serverBody = func(context.Context, io.Writer) error { return nil }
	sess := h.session(h.provider(nil), &fakeGh{}, reviewCheckout("/home/dev/proj"))
	sess.ReadyTimeout = 10 * time.Second

	err := landReview(context.Background(), io.Discard, sess)
	if err == nil {
		t.Fatal("landReview: want an error when the server exits before answering, even with a zero status")
	}
	if !strings.Contains(err.Error(), "exited reporting success") {
		t.Errorf("landReview error = %v, want it to say the server exited reporting success", err)
	}
}

// Ctrl-C while still WAITING for the server is a different arm from ctrl-C
// after the browser is open, and it is the one a user hits when the review
// server is never coming up.
func TestReviewCancelledWhileWaitingForReadiness(t *testing.T) {
	h := newReviewHarness()
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	h.probeReady = func() bool {
		once.Do(cancel) // the user gives up on the first failed attempt
		return false
	}
	gh := &fakeGh{}
	sess := h.session(h.provider([]string{"ssh", "-N", "-L"}), gh, reviewCheckout("/home/dev/proj"))
	sess.ReadyTimeout = 10 * time.Second

	err := landReview(ctx, io.Discard, sess)
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("landReview error = %v, want a cancellation error", err)
	}
	if len(gh.openCalls) != 0 {
		t.Errorf("OpenInBrowser calls = %v, want none — the session was cancelled before it was ready", gh.openCalls)
	}
	if h.stops() != 1 {
		t.Errorf("forwarder stopped %d times, want exactly 1", h.stops())
	}
	h.assertServerTornDown(t)
}

func TestReviewPortPickerFailureIsReported(t *testing.T) {
	h := newReviewHarness()
	gh := &fakeGh{}
	sess := h.session(h.provider(nil), gh, reviewCheckout("/home/dev/proj"))
	wantErr := errors.New("no free port")
	sess.PickPort = func() (int, error) { return 0, wantErr }

	err := landReview(context.Background(), io.Discard, sess)
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("landReview error = %v, want it to wrap %v", err, wantErr)
	}
	if h.argv() != nil {
		t.Errorf("guest server argv = %v, want the server never started without a port", h.argv())
	}
}

// The browser is a convenience, not the feature: a workstation with no opener
// (a headless ssh session, a locked-down desktop) must still get its review,
// with the URL on screen to open by hand.
func TestReviewBrowserFailureDoesNotAbortTheReview(t *testing.T) {
	h := newReviewHarness()
	gh := &fakeGh{openErr: errors.New("exec: xdg-open: not found")}
	sess := h.session(h.provider(nil), gh, reviewCheckout("/home/dev/proj"))
	realOpen := sess.Open
	sess.Open = func(ctx context.Context, url string) error {
		close(h.release)
		return realOpen(ctx, url)
	}

	var out strings.Builder
	if err := landReview(context.Background(), &out, sess); err != nil {
		t.Fatalf("landReview: unexpected error when the browser could not be opened: %v", err)
	}
	for _, want := range []string{"http://127.0.0.1:45123", "xdg-open", "/home/dev/proj/review.xml"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("landReview output missing %q; got:\n%s", want, out.String())
		}
	}
}

// A checkout with no merge base to compute against (no remote, no main/master)
// must still be reviewable: the server's own default — the working tree —
// is the right fallback, not a failed command.
func TestReviewWithoutAMergeBaseOmitsDiffArgs(t *testing.T) {
	h := newReviewHarness()
	h.mergeBase = "" // the guest script found nothing to diff against
	gh := &fakeGh{}
	sess := h.session(h.provider(nil), gh, reviewCheckout("/home/dev/proj"))
	realOpen := sess.Open
	sess.Open = func(ctx context.Context, url string) error {
		close(h.release)
		return realOpen(ctx, url)
	}

	if err := landReview(context.Background(), io.Discard, sess); err != nil {
		t.Fatalf("landReview: unexpected error: %v", err)
	}
	for _, a := range h.argv() {
		if a == "--diff-args" {
			t.Fatalf("guest server argv = %v, want no --diff-args when no merge base was found", h.argv())
		}
	}
}
