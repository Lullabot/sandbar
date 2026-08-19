// Package landreview runs ONE browser review session against ONE checkout
// inside a guest VM: it picks a workstation port, starts the guest's review
// server on it, makes that port reachable, waits for it to answer, opens a
// browser at it, and blocks until the reviewer finishes — which is the guest
// server writing review.xml into the checkout and exiting.
//
// It lives in internal/ rather than beside `sand land` because BOTH entry
// points need it: the CLI action (cmd/sand/land.go's --review) and the TUI's
// Landing pane, which cannot import a main package. Everything the session
// touches that is not pure — the backend, the browser, the port, the
// readiness probe, the forwarder child — arrives as a field, so the whole
// orchestration (including its teardown guarantees) is exercised with no VM,
// no ssh, no browser and no listening socket, following the same injection
// discipline as cmd/sand/land.go's landPR/landWeb.
//
// Two rules here are security properties, not style:
//
//   - The guest command is argv, never a shell string. A checkout path comes
//     from a sweep of the guest — the lowest-trust source in the system — so
//     it travels as its own argv element, exactly as Provider.RunArgv's
//     separate workdir parameter exists to enforce.
//   - The server binds the guest's loopback only, and reachability comes from
//     Lima's loopback-to-loopback forward or an explicit ssh -L that
//     terminates on the workstation's loopback. Unfinished code never becomes
//     reachable from the VM's network.
package landreview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lullabot/sandbar/internal/checkouts"
	"github.com/lullabot/sandbar/internal/vm"
)

// ServerPath is the guest path of the review server's entry module. It is
// hard-coded rather than discovered because the Ansible role installs the web
// app at exactly one fixed, untunable location — see
// roles/self-review/defaults/main.yml's selfreview_install_dir, whose own
// comment names this caller as the reason it must not become configurable.
const ServerPath = "/opt/sandbar/self-review/server/index.mjs"

// outputFile is the file the guest server writes a finished review to, inside
// the checkout. It matches @self-review/core's own default (config.outputFile),
// which a project could in principle override with a .self-review.yaml; the
// reported path is that default, since the browser — not sand — is the client
// that learns the real one.
const outputFile = "review.xml"

const (
	// defaultReadyTimeout bounds the wait for the guest server to answer:
	// long enough for a cold `node` start plus Lima noticing the new guest
	// listener (~1s) or an ssh -L completing its handshake, short enough that
	// a VM built without --with-review says so rather than appearing to hang.
	defaultReadyTimeout = 30 * time.Second
	// defaultPollInterval is the gap between readiness attempts.
	defaultPollInterval = 250 * time.Millisecond
	// probeTimeout bounds ONE readiness attempt, so a forward that accepts a
	// connection and then stalls cannot consume the whole budget in one go.
	probeTimeout = 2 * time.Second
	// forwardWaitDelay bounds how long a killed forwarder may hold its pipes
	// before they are closed out from under it — the same hazard, and the
	// same remedy, as internal/lima's runner (an ssh child can outlive the
	// process that spawned it and keep the pipes open forever).
	forwardWaitDelay = 2 * time.Second
	// diffBaseTimeout bounds the merge-base lookup, the FIRST thing Run does
	// and the only guest round trip that happens before any output reaches the
	// user. Generous enough for a cold ssh handshake plus a `git merge-base` on
	// a large repository, short enough that a stalled guest surfaces as a
	// working-tree review rather than a silent hang.
	diffBaseTimeout = 20 * time.Second
	// guestStopTimeout bounds the guest-side half of teardown. It is short
	// on purpose: this runs while the user is waiting for ctrl-C to take
	// effect, and a wedged guest must degrade to a warning rather than a
	// hang.
	guestStopTimeout = 10 * time.Second
)

// Provider is the narrow provider.Provider surface a review session needs:
// run the server in the guest, ask the guest one read-only git question, and
// learn how (or whether) to bridge the guest port to the workstation.
// Narrowing it here — rather than depending on the whole interface — is what
// lets a test drive the session with internal/providerfake and nothing else.
type Provider interface {
	Shell(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error
	ShellOut(ctx context.Context, name string, argv ...string) ([]byte, error)
	ForwardArgv(v vm.VM, hostPort, guestPort int) []string
}

// Session is one review of one checkout. The first four fields are the job;
// the rest are seams with production defaults, each used only when left nil
// or zero — the same defaulting contract internal/providerfake documents, so
// a caller states the job and nothing else.
type Session struct {
	// Provider is the backend the guest command runs through.
	Provider Provider
	// VM is the guest holding Checkout. It is passed to ForwardArgv, which
	// for Proxmox resolves the guest's address from it.
	VM vm.VM
	// Checkout is the swept checkout under review.
	Checkout checkouts.Checkout
	// Open opens a URL in the workstation's browser — landgh's OpenInBrowser
	// in production. A nil Open skips the browser entirely (the URL is still
	// written to Run's writer), and an error from it is reported but never
	// fatal: see Run.
	Open func(ctx context.Context, url string) error

	// PickPort returns a port free on the WORKSTATION, used on both ends.
	PickPort func() (int, error)
	// Probe reports whether the review UI answers at addr (host:port), nil
	// meaning ready.
	Probe func(ctx context.Context, addr string) error
	// StartForward runs a long-lived forwarder child, streaming its output to
	// out, and returns a stop func that kills AND reaps it.
	StartForward func(ctx context.Context, argv []string, out io.Writer) (stop func(), err error)
	// ReadyTimeout bounds the whole readiness wait; PollInterval is the gap
	// between attempts.
	ReadyTimeout time.Duration
	PollInterval time.Duration
}

// errServerGone reports that the guest command exited while the session was
// still waiting for it to become reachable. It is a sentinel rather than a
// message because the useful text is the server's OWN output, which only the
// caller has.
var errServerGone = errors.New("the review server exited before it was reachable")

// missingWebAppHint is appended to BOTH ways a review can fail to start,
// because both have the same overwhelmingly likely cause and a reader should
// not have to reach the second one to be told. A base image built without
// --with-review has no web app at all, so the guest's own message is a bare
// MODULE_NOT_FOUND that says nothing about which sand flag produces it.
const missingWebAppHint = "\n(if this VM's base image was built without `sand create --with-review`, " + ServerPath + " does not exist in the guest)"

// Run performs the whole session and returns the guest path the finished
// review was written to.
//
// It takes its context and its writer as parameters and installs no signal
// handler of its own, because the TUI calls it too: `sand land` supplies the
// context from its existing signal.NotifyContext and os.Stdout, while the
// Landing pane supplies a Bubble Tea command's context and a job log. Nothing
// here may write to os.Stdout directly or the board's frame would be
// corrupted.
//
// Cancelling ctx (ctrl-C on the CLI) tears the whole session down: the guest
// command is killed, the forwarder child is killed and reaped, and Run
// returns an error.
func (s *Session) Run(ctx context.Context, w io.Writer) (string, error) {
	port, err := s.pickPort()
	if err != nil {
		return "", fmt.Errorf("picking a free workstation port for the review server: %w", err)
	}

	// Discrete argv elements, never an `sh -c` string: see the package doc.
	// The diff range is optional by design — without it the server reviews
	// the working tree, which is a worse default but never a failure.
	argv := []string{"node", ServerPath, "--repo", s.Checkout.Path, "--port", strconv.Itoa(port)}
	base := s.diffBase(ctx)
	if base != "" {
		argv = append(argv, "--diff-args", base)
	}
	fmt.Fprintf(w, "reviewing %s in %s (%s)\n", s.Checkout.Path, s.VM.Name, describeBase(base))

	// Provider.Shell BLOCKS until the guest command exits, and the guest
	// server exits when the review is submitted — so running it in a
	// goroutine gives the completion signal and the process handle at once,
	// with no extra protocol.
	srvCtx, cancelServer := context.WithCancel(ctx)
	var srvOut lockedBuffer
	var srvErr error
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		srvErr = s.Provider.Shell(srvCtx, s.VM.Name, nil, &srvOut, argv...)
	}()
	// ONE deferred teardown covering every return path below, in the only
	// order that works: cancel FIRST (that is what makes the guest command
	// exit) and only then wait for the goroutine, so Run can never return
	// while a server child is still being supervised. Waiting is not
	// belt-and-braces — without it the goroutine outlives Run and keeps
	// writing to srvOut, and on the TUI's side would outlive the job it
	// belongs to. Receiving from a closed channel is fine, so the success
	// path having already waited costs nothing here.
	//
	// Cancelling is necessary but NOT sufficient, and that is measured on a
	// real Lima VM rather than assumed: `limactl shell` forks an ssh child
	// which the cancel does not reach (the hazard internal/lima's runner
	// documents), so the guest server keeps listening — and Lima keeps
	// forwarding its port — long after the CLI has exited. Every abnormal
	// exit therefore has to reach into the guest and stop it as well. A
	// submitted review does not: there the server has already exited on its
	// own, and the extra round trip would be pure latency on the path users
	// actually take.
	submitted := false
	defer func() {
		cancelServer()
		<-exited
		if !submitted {
			s.stopGuestServer(ctx, port, w)
		}
	}()

	// A nil ForwardArgv means the backend already puts the guest port on the
	// workstation's loopback (local Lima) — there is nothing to start, and
	// therefore nothing to tear down.
	var fwdOut lockedBuffer
	if fwdArgv := s.Provider.ForwardArgv(s.VM, port, port); len(fwdArgv) > 0 {
		stop, err := s.startForward(ctx, fwdArgv, &fwdOut)
		if err != nil {
			return "", fmt.Errorf("starting the port forward to %s: %w", s.VM.Name, err)
		}
		defer stop()
	}

	switch err := s.waitReady(ctx, port, exited); {
	case err == nil:
	case errors.Is(err, errServerGone):
		// exited is closed, so srvErr is safe to read here (and only here,
		// before the wait below) — the close/receive pair is what orders the
		// goroutine's write against this read.
		return "", fmt.Errorf("%w: %w%s%s", errServerGone, orExitedCleanly(srvErr),
			detail(srvOut.String(), fwdOut.String()), missingWebAppHint)
	default:
		return "", fmt.Errorf("the review server never answered at 127.0.0.1:%d (%w)%s%s",
			port, err, detail(srvOut.String(), fwdOut.String()), missingWebAppHint)
	}

	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	fmt.Fprintf(w, "review UI ready at %s\n", url)
	if s.Open != nil {
		if err := s.Open(ctx, url); err != nil {
			// Deliberately not fatal. The server is up and the URL is on
			// screen; a workstation with no opener (a headless ssh session, a
			// locked-down desktop) can still be reviewed from, and tearing a
			// working session down over the convenience layer would be the
			// worse outcome.
			fmt.Fprintf(w, "could not open a browser automatically (%v) — open the URL above yourself\n", err)
		}
	}
	fmt.Fprintln(w, "waiting for the review to be submitted…")

	<-exited
	if ctx.Err() != nil {
		return "", fmt.Errorf("the review was cancelled before it was submitted: %w", ctx.Err())
	}
	if srvErr != nil {
		return "", fmt.Errorf("the review server failed: %w%s", srvErr, detail(srvOut.String(), fwdOut.String()))
	}
	submitted = true
	return writtenPath(srvOut.String(), s.Checkout.Path), nil
}

// reviewWrittenPrefix is the marker the guest server prints on stdout naming
// the file it wrote. It must match server/index.mjs's REVIEW_WRITTEN_PREFIX
// exactly; both spell it out as a named constant so the pairing is findable
// from either side.
const reviewWrittenPrefix = "self-review: review written to "

// serverHeader is the response header the guest server sets on every response
// to identify itself to the readiness probe. It must match server/index.mjs's
// SERVER_HEADER exactly; both spell it out as a named constant so the pairing
// is findable from either side.
const serverHeader = "X-Sandbar-Review"

// writtenPath reports where the review actually landed, preferring what the
// guest server announced over this side's assumption.
//
// The assumption — <checkout>/review.xml — is only right when the project does
// not override it. A `.self-review.yaml` in the checkout can set outputFile to
// anything (the server chdir's into the repo precisely so that file applies),
// and reporting the default regardless named a path that did not exist, which
// is worse than useless: docs/using-sand/review.md tells the user to point
// their agent at it, so a wrong path sends the agent to read nothing.
//
// Falls back to the default when no marker is present, so a guest running an
// older build of the web app still reports something sensible rather than "".
func writtenPath(serverOut, checkoutPath string) string {
	for _, line := range strings.Split(serverOut, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, reviewWrittenPrefix); ok && after != "" {
			return after
		}
	}
	return path.Join(checkoutPath, outputFile)
}

// stopServerScript stops the review server the guest is running on port $1.
//
// It identifies the process by the LISTENING SOCKET rather than by matching a
// command line, because the port is the one thing this session knows for
// certain is its own, and confirms the match against /proc before signalling:
// killing "whatever is listening" would be a guest process of someone else's
// the moment a port is reused. `ss`'s own process column cannot be used for
// that confirmation — node reports there as `MainThread`, not `node` (observed
// on a real guest) — so the check reads the cmdline directly.
//
// Every failure is a no-op: nothing listening, a pid that is not the review
// server, a kernel without /proc. The worst outcome is a server that keeps
// running, which is exactly the state this is trying to improve on.
const stopServerScript = `set -f
pid=$(ss -H -ltnp "sport = :$1" 2>/dev/null | sed -n "s/.*pid=\([0-9]\{1,\}\),.*/\1/p" | head -n 1)
[ -n "$pid" ] || exit 0
grep -qa "self-review/server/index.mjs" "/proc/$pid/cmdline" 2>/dev/null || exit 0
kill "$pid" 2>/dev/null || true
exit 0
`

// stopGuestServer is the guest-side half of teardown: it kills a review
// server the cancelled guest command left behind. Killing it also releases
// the orphaned ssh child holding it, and with it the forwarded workstation
// port, so this one round trip resolves both halves of the leak.
//
// It runs on a DETACHED context because the usual reason to be here is that
// the caller's context was just cancelled — inheriting that would make the
// cleanup a guaranteed no-op precisely when it is needed. Best-effort by
// design: a failure is reported to w and never changes Run's result, because
// by this point Run's outcome is already decided and a cleanup problem must
// not be mistaken for a review problem.
func (s *Session) stopGuestServer(ctx context.Context, port int, w io.Writer) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), guestStopTimeout)
	defer cancel()

	if _, err := s.Provider.ShellOut(ctx, s.VM.Name, "sh", "-c", stopServerScript, "sh", strconv.Itoa(port)); err != nil {
		fmt.Fprintf(w, "could not stop the review server inside %s (%v); it may still be listening on port %d there\n", s.VM.Name, err, port)
	}
}

// waitReady polls until the review UI answers, the guest command exits, ctx is
// cancelled, or the budget runs out.
//
// Noticing the exit matters as much as noticing readiness: a missing `node`,
// or a VM built without --with-review, fails in under a second, and waiting
// out the full timeout to report a generic "not reachable" would bury the
// guest's own explanation.
func (s *Session) waitReady(ctx context.Context, port int, exited <-chan struct{}) error {
	addr := "127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(s.readyTimeout())
	for {
		if err := s.probe(ctx, addr); err == nil {
			return nil
		}
		// Checked without blocking, and before the timer, so an
		// already-dead server wins deterministically rather than racing the
		// tick in the select below.
		select {
		case <-exited:
			return errServerGone
		default:
		}
		select {
		case <-exited:
			return errServerGone
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.pollInterval()):
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("gave up after %s", s.readyTimeout())
		}
	}
}

// mergeBaseScript resolves, inside the guest, the commit this checkout's work
// began from: the merge base between HEAD and the repository's default
// branch. Printing nothing is a valid answer (a repo with no such branch, an
// unborn HEAD, unrelated histories) and the caller then leaves the range to
// the server's own default.
//
// It is a FIXED, LITERAL string. The checkout path and the sweep's
// default-branch name arrive as positional arguments ($1, $2) and are only
// ever expanded inside double quotes, so nothing guest-derived is ever parsed
// as shell syntax — the same rule the Landing pane's commitAndPushExpr keeps
// by passing its checkout through Provider.RunArgv's workdir element.
//
// The result is used as a bare `git diff <base>` argument rather than a
// three-dot `base...HEAD` range, and that difference is the point: two-dot
// against the working tree covers the branch's commits AND its uncommitted
// edits, which is what "review what I have here" means in a sandbox where
// nothing has been pushed yet.
const mergeBaseScript = `set -f
d=$1
for c in "$2" main master; do
  [ -n "$c" ] || continue
  for r in $(git -C "$d" remote 2>/dev/null); do
    if git -C "$d" rev-parse --verify -q "refs/remotes/$r/$c" >/dev/null 2>&1; then
      git -C "$d" merge-base "refs/remotes/$r/$c" HEAD 2>/dev/null && exit 0
    fi
  done
  if git -C "$d" rev-parse --verify -q "refs/heads/$c" >/dev/null 2>&1; then
    git -C "$d" merge-base "refs/heads/$c" HEAD 2>/dev/null && exit 0
  fi
done
exit 0
`

// objectID matches a git object name and nothing else. The match is a gate,
// not a formality: whatever comes back is handed to `git diff` in the guest,
// so anything that is not plainly an object name (a login banner, an error,
// something that would read as an option) is dropped in favour of no range at
// all.
var objectID = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// diffBase asks the guest for the merge base, returning "" for every failure —
// a broken lookup must degrade to reviewing the working tree, never fail the
// command.
func (s *Session) diffBase(ctx context.Context) string {
	// Bounded, like every other guest interaction in this package (waitReady's
	// ReadyTimeout, probeHTTP's probeTimeout, stopGuestServer's
	// guestStopTimeout) — and this one needs it most. It runs BEFORE Run has
	// printed a single line, so on a guest whose sshd accepts the connection
	// and then stalls, an unbounded ShellOut left `sand land --review` hanging
	// forever having produced no output at all: no port, no URL, no hint that
	// anything was happening. Its own contract already degrades every failure
	// to "review the working tree", so a timeout costs nothing but the
	// merge-base refinement.
	ctx, cancel := context.WithTimeout(ctx, diffBaseTimeout)
	defer cancel()
	out, err := s.Provider.ShellOut(ctx, s.VM.Name, "sh", "-c", mergeBaseScript, "sh", s.Checkout.Path, s.Checkout.DefaultBranch)
	if err != nil {
		return ""
	}
	return lastObjectID(string(out))
}

// lastObjectID returns the last line of out that is an object name. Last, not
// first, because the script prints its answer immediately before exiting: any
// login-shell noise a guest prepends is therefore behind it.
func lastObjectID(out string) string {
	var found string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); objectID.MatchString(line) {
			found = line
		}
	}
	return found
}

// describeBase renders the chosen range for the human reading Run's output,
// so an unexpectedly small (or large) diff is explainable without guessing.
func describeBase(base string) string {
	if base == "" {
		return "the working tree: no merge base with a default branch was found"
	}
	if len(base) > 12 {
		base = base[:12]
	}
	return "everything since " + base
}

// --- seams and their production defaults ---

func (s *Session) pickPort() (int, error) {
	if s.PickPort != nil {
		return s.PickPort()
	}
	return freePort()
}

func (s *Session) probe(ctx context.Context, addr string) error {
	if s.Probe != nil {
		return s.Probe(ctx, addr)
	}
	return probeHTTP(ctx, addr)
}

func (s *Session) startForward(ctx context.Context, argv []string, out io.Writer) (func(), error) {
	if s.StartForward != nil {
		return s.StartForward(ctx, argv, out)
	}
	return startForwardChild(ctx, argv, out)
}

func (s *Session) readyTimeout() time.Duration {
	if s.ReadyTimeout > 0 {
		return s.ReadyTimeout
	}
	return defaultReadyTimeout
}

func (s *Session) pollInterval() time.Duration {
	if s.PollInterval > 0 {
		return s.PollInterval
	}
	return defaultPollInterval
}

// freePort asks the kernel for an unused loopback port and immediately gives
// it back. The gap between letting go and the guest server claiming it is a
// real (if tiny) race, and it is unavoidable: local Lima forwards a guest
// loopback port to the SAME number on the host, so the host and guest ports
// cannot be chosen independently, and sand keeps one rule for all three
// backends. A collision surfaces as the readiness wait failing, not as
// silent misbehaviour.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// probeClient is the readiness prober's HTTP client. Keep-alives are off so a
// probe never leaves a pooled connection behind on a port that is about to be
// handed to a browser.
var probeClient = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

// probeHTTP reports whether the review UI actually answers at addr.
//
// It makes a real HTTP request rather than a bare net.Dial, and that is
// load-bearing on the forwarded backends: `ssh -N -L` binds the workstation's
// listening socket the instant it connects, so a TCP dial SUCCEEDS long
// before anything is listening in the guest — ssh only discovers the far end
// is refusing when it tries to open the channel for that connection. A
// dial-only probe would therefore declare readiness immediately on remote
// Lima and Proxmox and open the browser onto a connection error. A completed
// request round trip means the same thing on all three backends.
//
// Any STATUS counts, including an error status: the question is whether the
// server is there, not whether it likes the request. But it must be OUR server,
// which is what serverHeader settles.
//
// Accepting any HTTP responder was a real hazard, not a theoretical one. The
// port is chosen by what is free on the WORKSTATION and the same number is
// reused in the guest; on remote Lima it must ALSO be free on the remote host's
// loopback, where Lima's own auto-forward lands the guest port, and nothing
// verifies that. When it is not, the tunnel reaches whatever else holds that
// port — and a probe satisfied by any response would declare readiness, open
// the reviewer's browser onto an unrelated application, and then block forever
// waiting for a submission that could never arrive. Requiring the header turns
// every one of those into an ordinary readiness timeout naming the problem.
func probeHTTP(ctx context.Context, addr string) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/", nil)
	if err != nil {
		return err
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<12))
	if resp.Header.Get(serverHeader) == "" {
		return fmt.Errorf("something is listening on %s but it is not the review server "+
			"(no %s header) — most likely another process holds this port", addr, serverHeader)
	}
	return nil
}

// startForwardChild runs argv as a long-lived child and returns a stop
// function that kills it AND waits for it to be reaped, so a caller's
// `defer stop()` genuinely means "no forwarder outlives this call". A plain
// kill would leave a zombie and, worse, would return before the workstation
// port was released.
func startForwardChild(ctx context.Context, argv []string, out io.Writer) (func(), error) {
	if len(argv) == 0 {
		return nil, errors.New("no forwarder command to run")
	}
	// A child context, not ctx itself: the forward must also be killable on
	// the success path, where ctx is still very much alive.
	cctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.WaitDelay = forwardWaitDelay
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	reaped := make(chan struct{})
	go func() {
		defer close(reaped)
		_ = cmd.Wait()
	}()
	return func() {
		cancel()
		<-reaped
	}, nil
}

// --- small helpers ---

// lockedBuffer collects a child's output while another goroutine may be
// reading it — the readiness paths report what the server has said SO FAR,
// which is before the writing goroutine has finished.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// orExitedCleanly gives a nil error a printable form: a server that exits 0
// without ever answering has failed just as surely as one that crashed, and
// "%w" on a nil error prints "%!w(<nil>)".
func orExitedCleanly(err error) error {
	if err == nil {
		return errors.New("it exited reporting success")
	}
	return err
}

// detail appends whatever the children printed, which is where the real
// explanation lives (`node: command not found`, a module resolution failure,
// ssh's own refusal). It is a suffix rather than a wrapped error so the
// error's first line stays the summary.
func detail(outputs ...string) string {
	var b strings.Builder
	for _, out := range outputs {
		if out = strings.TrimSpace(out); out != "" {
			b.WriteString("\n")
			b.WriteString(out)
		}
	}
	return b.String()
}
