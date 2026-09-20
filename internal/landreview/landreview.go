// Package landreview runs ONE browser review session against ONE checkout
// inside a guest VM: it starts upstream @self-review/serve in the guest, reads
// back the port that server chose for itself, makes that port reachable from
// the workstation, waits for it to answer, opens a browser at it, and blocks
// until the reviewer finishes — which is the guest server writing review.xml
// into the checkout and exiting.
//
// The order is dictated by upstream and is the thing most likely to surprise
// a reader: @self-review/serve binds an EPHEMERAL port and has no flag to ask
// for a particular one, so sand cannot choose the port and then connect. It
// must start the server first, parse the port out of the banner the server
// prints, and only then build the bridge.
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
	"encoding/json"
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

// ServeBinary is the guest command that serves the review UI: upstream
// @self-review/serve's own CLI, put on PATH by a global npm install in
// roles/self-review. It is a bare NAME rather than an absolute path on
// purpose — npm owns where a global bin lands, and hard-coding that layout
// here would break the moment the role's install method changed.
const ServeBinary = "self-review-serve"

// serveScript runs ServeBinary with the checkout as its working directory,
// because @self-review/serve takes no --repo flag: it reviews the repository
// it is started in, and resolves its output file relative to that same
// directory (so a checkout's own .self-review.yaml applies).
//
// It is a FIXED, LITERAL string, and the checkout path and diff base arrive
// as positional arguments that are only ever expanded inside double quotes —
// the same rule diffBaseScript and the Landing pane's commitAndPushExpr
// keep, for the same reason: both values come from a sweep of the guest, the
// lowest-trust source in the system, and neither may ever be parsed as shell
// syntax.
//
// `exec` matters beyond tidiness. Without it the guest command is the shell,
// and the server is its child — so the teardown that kills what the transport
// started would reap the shell and leave the server listening. Replacing the
// shell makes the process sand supervises the process that serves.
const serveScript = `set -f
d=$1
shift
cd "$d" || exit 1
exec ` + ServeBinary + ` "$@"
`

// skillsStageDir is where roles/self-review stages the pinned assistant
// skills in the BASE IMAGE: a root-owned, pristine copy that every checkout's
// copy is made from. A guest whose base predates that role simply has no such
// directory, and a review still runs — see installSkills.
const skillsStageDir = "/opt/sandbar/self-review/skills"

// skillsTargetDir is where they are installed INSIDE the checkout, relative
// to its root. Per-checkout is not a preference: upstream issue #162 ("Allow
// skills to be installed globally") is open, and the skills hard-code this
// path in the commands they tell an assistant to run — `xmllint --schema
// .agents/skills/self-review-apply/assets/self-review-v3.xsd` — so even the
// one assistant that could find a guest-wide copy would be following
// instructions that no longer resolve.
//
// roles/self-review adds this path, and the review output beside it, to the
// guest user's global git excludes, so none of it surfaces as untracked work
// in the project being reviewed.
const skillsTargetDir = ".agents/skills"

// installSkillsScript copies each staged skill into the checkout.
//
// It is a FIXED, LITERAL string taking the checkout and the staging directory
// as positional arguments, for the reason every guest script in this package
// is one: the checkout path comes from a sweep of the guest, the
// lowest-trust source in the system.
//
// The rule with teeth here is the tracked-file check. Upstream's own install
// instruction is `cp -r` into your project, so a project may legitimately
// keep these skills under version control — and overwriting a tracked file
// would drop an edit nobody asked for into someone's working tree, which the
// global excludes cannot hide, because ignore rules do not apply to files git
// already tracks. A skill git tracks is therefore left exactly as it is, and
// only untracked copies (the ones sand itself put there) are refreshed.
const installSkillsScript = `set -f
d=$1
src=$2
[ -d "$src" ] || exit 0
cd "$d" || exit 0
# Pathname expansion is off everywhere else in this package precisely so a
# guest-derived value can never be expanded — but this one loop needs it to
# enumerate what the base staged, and its pattern has no guest-derived part:
# $src is skillsStageDir, a compile-time constant. It goes back off as soon
# as the loop ends, and every expansion inside the loop is quoted.
set +f
for s in "$src"/self-review-*; do
  [ -d "$s" ] || continue
  n=${s##*/}
  dest=` + skillsTargetDir + `/$n
  if git ls-files --error-unmatch "$dest" >/dev/null 2>&1; then
    printf 'skipped=%s\n' "$n"
    continue
  fi
  mkdir -p ` + skillsTargetDir + ` || exit 0
  rm -rf "$dest"
  cp -R "$s" "$dest" || continue
  printf 'installed=%s\n' "$n"
done
set -f
exit 0
`

// removeOutputScript deletes a finished review and its walkthrough sidecar
// from the checkout, which is what "start this review over" has to mean:
// upstream has no notion of a review being done or discarded and never
// removes either file itself, so without this the next run would resume from
// comments the user just asked to abandon.
//
// Both names are FIXED and the checkout arrives as $1, so those two literals
// are the only things this can ever delete.
const removeOutputScript = `set -f
d=$1
cd "$d" || exit 1
rm -f ` + outputFile + ` ` + guideFile + `
exit 0
`

// outputFile is the file a finished review lands in, inside the checkout. It
// matches @self-review/core's own default (config.outputFile). It is only a
// FALLBACK: the server announces the real path itself (see writtenPath), which
// is what a project's .self-review.yaml can redirect.
const outputFile = "review.xml"

// guideFile is the walkthrough sidecar upstream reads at startup when it sits
// beside the output path: self-review-guide writes it, and self-review-
// critique runs that skill as its first step. sand never writes it and never
// reads it — it only needs the name in order to remove it alongside the
// review it belongs to.
const guideFile = "review.guide.xml"

const (
	// defaultReadyTimeout bounds BOTH waits — for the server to announce its
	// port, and then for that port to answer. Long enough for a cold `node`
	// start plus Lima noticing the new guest listener (~1s) or an ssh -L
	// completing its handshake, short enough that a VM whose base predates
	// the review tool says so rather than appearing to hang.
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
	// diffBaseTimeout bounds the base lookup, the FIRST thing Run does and
	// the only guest round trip that happens before any output reaches the
	// user. Generous enough for a cold ssh handshake, a revision walk and a
	// name-only tree diff on a large repository, short enough that a stalled
	// guest surfaces as a working-tree review rather than a silent hang.
	diffBaseTimeout = 30 * time.Second
	// maxDiffFiles is the changed-file count above which Run refuses to start
	// a review at all.
	//
	// The ceiling being defended is upstream's: @self-review/serve reads
	// `git diff` through a 50MB execFile buffer, and a diff past it dies with
	// a Node buffer error that names neither the base nor the size. That
	// message sends the reader hunting for a bug in the review tool when the
	// real fault is a base thousands of commits too old, which is exactly the
	// wrong place to look — so this refuses first, in a sentence that says
	// which commit was picked, how old it is, and how big the diff would be.
	//
	// It counts FILES rather than bytes because the file count comes from a
	// name-only tree diff costing milliseconds, while measuring bytes means
	// generating the entire patch — the very work being guarded against. Five
	// thousand files is far above any diff a human reviews in a browser and
	// far below the ~37,000 a wrong base produced in the case this guard was
	// written for, so it discriminates without ever needing to be tuned.
	maxDiffFiles = 5000
	// installSkillsTimeout bounds the one-shot skill install. It copies about
	// 100KB inside the guest, so it is generous enough to be invisible and
	// short enough that a wedged guest costs a review its skills rather than
	// the review itself — installSkills treats every failure as non-fatal.
	installSkillsTimeout = 20 * time.Second
	// removeOutputTimeout bounds the two `rm -f`s behind "review afresh".
	// Unlike the install, this one's failure IS fatal to the action it serves:
	// starting a fresh review over a review.xml that is still there would
	// resume from the comments the user asked to discard.
	removeOutputTimeout = 10 * time.Second
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

	// PickPort returns a port free on the WORKSTATION, used as the near end
	// of a forward. Backends that need no forward (local Lima) ignore it and
	// browse the guest's own port.
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
	// MaxDiffFiles caps how many changed files a review may cover before Run
	// refuses to start it; zero means maxDiffFiles.
	MaxDiffFiles int
	// Fresh starts the review over: any previous review.xml (and its
	// walkthrough sidecar) is REMOVED from the checkout before the server
	// starts, and nothing is carried in.
	//
	// Removal is the session's job rather than the caller's because the two
	// halves are one decision. A caller that deleted the file itself and left
	// this false would race its own probe; a caller that set this without
	// deleting would leave a review.xml the NEXT run silently resumes from.
	Fresh bool
}

// errServerGone reports that the guest command exited while the session was
// still waiting for it to become reachable. It is a sentinel rather than a
// message because the useful text is the server's OWN output, which only the
// caller has.
var errServerGone = errors.New("the review server exited before it was reachable")

// missingToolHint is appended to BOTH ways a review can fail to start,
// because both have the same overwhelmingly likely cause and a reader should
// not have to reach the second one to be told. A base image built without the
// review tool has no ServeBinary at all, so the guest's own message is a bare
// `command not found` that says nothing about which sand flag produces it.
//
// The review tool is not a tool-set selection, so the only way to be missing
// it is a base image older than the role itself — and the next create fixes
// that on its own, because adding the role changed the playbook hash and that
// is what marks a base stale (internal/provision's baseStale). There is no
// flag to pass and nothing to opt into, so the hint says the one thing that is
// actually true.
//
// This used to name a flag, and naming one was the bug: while the tool was a
// selection, an unpassed --with-* adopted the existing base's stamp, so the
// advice a user could act on and the advice that worked were different
// sentences.
const missingToolHint = "\n(if this VM's base image predates the review tool, " + ServeBinary +
	" does not exist in the guest — the next `sand create` brings the base up to date and installs it)"

// serveReadyRe matches the line @self-review/serve prints once its listener
// is up, which is the ONLY way to learn the port: upstream binds an ephemeral
// port (listenLoopback's `listen(0)`) and offers no flag to dictate one, so
// the guest chooses and sand is told after the fact. That inverts the usual
// order — the forward cannot be built until the server is already running.
//
// Anchored on 127.0.0.1 because that is what upstream binds and all this side
// knows how to bridge; a future upstream that bound something else must not be
// silently misread as reachable.
var serveReadyRe = regexp.MustCompile(`Review ready at http://127\.0\.0\.1:([0-9]{1,5})/`)

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
	// Discrete argv elements around a FIXED script, never a shell string
	// built from data: see the package doc and serveScript. The diff range is
	// optional by design — without it the server reviews the working tree,
	// which is a worse default but never a failure.
	argv := []string{"sh", "-c", serveScript, "sh", s.Checkout.Path}

	// Before the probe, not after: the probe is what reports a resumable
	// review, and removing the file first means it simply has nothing to
	// report. Fatal on failure — a "fresh" review that quietly resumed from
	// the comments the user asked to discard is the one outcome this verb
	// exists to prevent.
	if s.Fresh {
		if err := s.removeOutput(ctx); err != nil {
			return "", err
		}
	}

	base := s.diffBase(ctx)
	if base.Commit != "" {
		// Refused BEFORE the server starts, so the user gets a sentence about
		// the base instead of a Node buffer error several seconds later. See
		// maxDiffFiles.
		if limit := s.maxDiffFiles(); base.Files > limit {
			return "", diffTooLargeError(base, limit)
		}
	}
	// Flags first, then the range: upstream scans the whole argv and treats
	// everything it does not recognise as a `git diff` argument, so the order
	// is for the reader rather than the parser.
	// The Fresh arm above already removed the file, so the probe cannot have
	// reported one; the second half of this condition is belt and braces
	// against a probe that somehow saw it anyway.
	resuming := base.Resume && !s.Fresh
	if resuming {
		argv = append(argv, "--resume-from", path.Join(s.Checkout.Path, outputFile))
	}
	if base.Commit != "" {
		argv = append(argv, base.Commit)
	}

	s.installSkills(ctx, w)
	fmt.Fprintf(w, "reviewing %s in %s (%s)\n", s.Checkout.Path, s.VM.Name, describeBase(base))
	if resuming {
		fmt.Fprintf(w, "carrying in the comments already in %s\n", outputFile)
	}

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
	// guestPort is not known until the server announces it, so teardown must
	// tolerate never having learned it: 0 means the server never got that
	// far, and there is correspondingly nothing in the guest to stop.
	guestPort := 0
	submitted := false
	defer func() {
		cancelServer()
		<-exited
		if !submitted && guestPort != 0 {
			s.stopGuestServer(ctx, guestPort, w)
		}
	}()

	// FIRST wait for the guest's own banner, because upstream picks the port
	// itself and no forward can be built before that number is known. This is
	// also where a guest with no review tool fails, in well under a second.
	guestPort, err := s.awaitGuestPort(ctx, &srvOut, exited)
	if err != nil {
		if errors.Is(err, errServerGone) {
			// exited is closed, so srvErr is safe to read here (and only
			// here, before the wait below) — the close/receive pair is what
			// orders the goroutine's write against this read.
			return "", fmt.Errorf("%w: %w%s%s", errServerGone, orExitedCleanly(srvErr),
				detail(srvOut.String()), missingToolHint)
		}
		return "", fmt.Errorf("the review server never reported a URL (%w)%s%s",
			err, detail(srvOut.String()), missingToolHint)
	}

	// A nil ForwardArgv means the backend already puts the guest port on the
	// workstation's loopback (local Lima, whose auto-forward uses the SAME
	// number on both sides) — there is nothing to start, and therefore
	// nothing to tear down, and the port to browse is the guest's own.
	//
	// Whether a forward is needed does not depend on the port numbers, so
	// asking with the guest's own port settles "does this backend need one at
	// all?" before any workstation port is reserved. That ordering is
	// deliberate: local Lima has nothing to reserve, and a port-picker failure
	// must not fail a review that was never going to use the picker.
	//
	// It does mean the seam is asked twice, and ForwardArgv is not quite as
	// pure as its doc claims: the Proxmox implementation resolves the guest's
	// address on each call (bounded, and cached after the first), so the second
	// ask can in principle fail where the first succeeded. That degrades to a
	// failArgv child which exits at once and whose message is carried into the
	// readiness error below (fwdOut), rather than to a silent wrong forward.
	hostPort := guestPort
	fwdArgv := s.Provider.ForwardArgv(s.VM, guestPort, guestPort)
	var fwdOut lockedBuffer
	if fwdArgv != nil {
		picked, err := s.pickPort()
		if err != nil {
			return "", fmt.Errorf("picking a free workstation port for the review forward: %w", err)
		}
		hostPort = picked
		if hostPort != guestPort {
			fwdArgv = s.Provider.ForwardArgv(s.VM, hostPort, guestPort)
		}
		stop, err := s.startForward(ctx, fwdArgv, &fwdOut)
		if err != nil {
			return "", fmt.Errorf("starting the port forward to %s: %w", s.VM.Name, err)
		}
		defer stop()
	}

	switch err := s.waitReady(ctx, hostPort, exited); {
	case err == nil:
	case errors.Is(err, errServerGone):
		return "", fmt.Errorf("%w: %w%s", errServerGone, orExitedCleanly(srvErr),
			detail(srvOut.String(), fwdOut.String()))
	default:
		return "", fmt.Errorf("the review UI never answered at 127.0.0.1:%d (%w)%s%s",
			hostPort, err, detail(srvOut.String(), fwdOut.String()), unreachableHint(hostPort, guestPort))
	}

	url := fmt.Sprintf("http://127.0.0.1:%d", hostPort)
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

// The two lines @self-review/serve prints about where a review lands.
// reviewWrittenPrefix is emitted on success and names the file it actually
// wrote; outputPathPrefix is emitted at startup and names where it INTENDS to
// write, which is the best answer available if the process is torn down
// before it finishes.
const (
	reviewWrittenPrefix = "[serve] Review written to "
	outputPathPrefix    = "[serve] Output path: "
)

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
// Falls back to the startup announcement, and then to the default, so a guest
// running a build that words either line differently still reports something
// sensible rather than "".
func writtenPath(serverOut, checkoutPath string) string {
	var announced string
	for _, line := range strings.Split(serverOut, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, reviewWrittenPrefix); ok && after != "" {
			return after
		}
		if after, ok := strings.CutPrefix(line, outputPathPrefix); ok && after != "" {
			announced = after
		}
	}
	if announced != "" {
		return announced
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
grep -qa "self-review-serve" "/proc/$pid/cmdline" 2>/dev/null || exit 0
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

// awaitGuestPort waits for the port @self-review/serve announces at startup.
//
// It polls the buffer the server goroutine is filling rather than reading a
// pipe, and that is what keeps the whole flow testable with no VM:
// Provider.Shell takes an io.Writer, so the banner arrives by exactly the same
// path on a fake provider as on a real guest. (Upstream prints it to STDERR,
// which every guest-command transport in sand merges into that one writer.)
//
// Noticing the exit matters as much as noticing the banner: a guest with no
// review tool fails in well under a second, and waiting out the full timeout
// to report a generic "no URL" would bury the guest's own explanation.
func (s *Session) awaitGuestPort(ctx context.Context, out *lockedBuffer, exited <-chan struct{}) (int, error) {
	deadline := time.Now().Add(s.readyTimeout())
	for {
		if port := parseServePort(out.String()); port != 0 {
			return port, nil
		}
		select {
		case <-exited:
			// The goroutine writes every byte before it closes this channel,
			// so one more look settles the case where the banner and the exit
			// arrive together: a server that announced its port and then died
			// still told us the number teardown needs.
			if port := parseServePort(out.String()); port != 0 {
				return port, nil
			}
			return 0, errServerGone
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(s.pollInterval()):
		}
		if !time.Now().Before(deadline) {
			// One last look before giving up, for the same reason the exit
			// branch takes one: this polls a BUFFER another goroutine is
			// filling, so the banner may well have landed during the sleep
			// that just expired the budget. Returning without re-reading
			// would throw away an answer already in hand.
			if port := parseServePort(out.String()); port != 0 {
				return port, nil
			}
			return 0, fmt.Errorf("gave up after %s", s.readyTimeout())
		}
	}
}

// parseServePort extracts the guest port from whatever the server has printed
// so far, returning 0 when it has not announced one yet. A number outside the
// port range is treated as no answer rather than trusted: it is about to be
// used to build a forward.
func parseServePort(out string) int {
	m := serveReadyRe.FindStringSubmatch(out)
	if m == nil {
		return 0
	}
	port, err := strconv.Atoi(m[1])
	if err != nil || port <= 0 || port > 65535 {
		return 0
	}
	return port
}

// unreachableHint explains the likeliest reason a server that DID announce a
// port is nonetheless unreachable. It only has something to say about local
// Lima, where the two port numbers are necessarily the same.
//
// Lima's auto-forward puts the guest's port on the workstation under the SAME
// number, so the one thing that can go wrong is that number already being
// taken here — which Lima reports only in its own log. sand cannot prevent
// it: upstream chooses the guest port and offers no way to ask for another,
// so the collision is either explained here or not at all.
func unreachableHint(hostPort, guestPort int) string {
	if hostPort != guestPort {
		return ""
	}
	return fmt.Sprintf("\n(the guest chose port %d, and Lima forwards it to the same port on this "+
		"machine — if something here already holds %d that forward cannot bind; "+
		"running the review again picks a different guest port)", guestPort, hostPort)
}

// waitReady polls until the review UI answers, the guest command exits, ctx is
// cancelled, or the budget runs out.
//
// Noticing the exit matters as much as noticing readiness: a server that
// announced a port and then died fails in under a second, and waiting out the
// full timeout to report a generic "not reachable" would bury the guest's own
// explanation.
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

// diffBaseScript resolves, inside the guest, the commit a review of this
// checkout should start from, and measures how big that review would be.
//
// It asks the question that needs no guessing FIRST: which commits exist
// nowhere but this VM. `rev-list HEAD --not --remotes` excludes everything
// reachable from ANY remote-tracking ref, so the oldest commit it returns is
// the first piece of local work and its parent is the review's base. That is
// immune to which remote is stale, to a rebase rewriting every hash, and to
// the repository's branch naming, because it names no branch at all.
//
// Only when there is no local-only history — every commit already published
// somewhere — does it fall back to a merge base with a trunk candidate, and
// then it takes the CLOSEST base among all candidates rather than the first
// ref that happens to exist. "First that exists" is what made this script
// dangerous: with `refs/remotes/<remote>/HEAD` unset the sweep reports no
// default branch, the candidate list falls through to `main` and then
// `master`, and on a fork `master` is frozen at the moment the fork was
// taken. One real case resolved to a 2021 commit and asked for a 240MB,
// 37,000-file diff of a repository the user had five commits in. Ranking by
// distance cannot make that mistake: the trunk the work actually branched
// from is always the nearest one.
//
// It is a FIXED, LITERAL string. The checkout path and the sweep's
// default-branch name arrive as positional arguments ($1, $2) and are only
// ever expanded inside double quotes, so nothing guest-derived is ever parsed
// as shell syntax — the same rule the Landing pane's commitAndPushExpr keeps.
//
// The base is used as a bare two-dot `git diff <base>` argument rather than a
// three-dot `base...HEAD` range, and that difference is the point: two-dot
// against the working tree covers the branch's commits AND its uncommitted
// edits, which is what "review what I have here" means in a sandbox where
// nothing has been pushed yet.
//
// Printing nothing is a valid answer (no remotes and no trunk, an unborn
// HEAD, unrelated histories); the caller then leaves the range to the
// server's own default.
//
// It also reports whether a previous review is sitting in the checkout, so
// the caller can carry its comments in. That check is deliberately narrow: it
// looks for the DEFAULT output path only, and stays silent when either config
// file sets `output-file`, because a project that redirects its output has a
// path only upstream's own config precedence can resolve. Guessing it wrong
// would resume from somebody else's review, which is worse than not resuming.
//
// Only a flag comes back, never a path — Go composes that from the checkout
// it already holds, so nothing reaching `--resume-from` originates in the
// guest.
const diffBaseScript = `set -f
d=$1
db=$2
base=

if [ -f "$d/` + outputFile + `" ] \
  && ! grep -q '^[[:space:]]*output-file:' "$d/.self-review.yaml" 2>/dev/null \
  && ! grep -q '^[[:space:]]*output-file:' "$HOME/.config/self-review/config.yaml" 2>/dev/null; then
  printf 'sandresume=1\n'
fi

oldest=$(git -C "$d" rev-list --topo-order HEAD --not --remotes 2>/dev/null | tail -n 1)
if [ -n "$oldest" ]; then
  base=$(git -C "$d" rev-parse --verify -q "$oldest^" 2>/dev/null)
fi

if [ -z "$base" ]; then
  bestn=
  for c in "$db" main master; do
    [ -n "$c" ] || continue
    for r in $(git -C "$d" remote 2>/dev/null); do
      git -C "$d" rev-parse --verify -q "refs/remotes/$r/$c" >/dev/null 2>&1 || continue
      b=$(git -C "$d" merge-base "refs/remotes/$r/$c" HEAD 2>/dev/null)
      [ -n "$b" ] || continue
      n=$(git -C "$d" rev-list --count "$b..HEAD" 2>/dev/null)
      [ -n "$n" ] || continue
      if [ -z "$bestn" ] || [ "$n" -lt "$bestn" ]; then bestn=$n; base=$b; fi
    done
    if git -C "$d" rev-parse --verify -q "refs/heads/$c" >/dev/null 2>&1; then
      b=$(git -C "$d" merge-base "refs/heads/$c" HEAD 2>/dev/null)
      n=
      [ -n "$b" ] && n=$(git -C "$d" rev-list --count "$b..HEAD" 2>/dev/null)
      if [ -n "$n" ] && { [ -z "$bestn" ] || [ "$n" -lt "$bestn" ]; }; then bestn=$n; base=$b; fi
    fi
  done
fi

[ -n "$base" ] || exit 0
printf 'sandbase=%s\n' "$base"
printf 'sanddate=%s\n' "$(git -C "$d" log -1 --format=%cs "$base" 2>/dev/null)"
printf 'sandcommits=%s\n' "$(git -C "$d" rev-list --count "$base..HEAD" 2>/dev/null)"
printf 'sandfiles=%s\n' "$(git -C "$d" diff --name-only --no-renames "$base" 2>/dev/null | grep -c .)"
exit 0
`

// objectID matches a git object name and nothing else. The match is a gate,
// not a formality: whatever comes back is handed to `git diff` in the guest,
// so anything that is not plainly an object name (a login banner, an error,
// something that would read as an option) is dropped in favour of no range at
// all.
var objectID = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// diffBaseInfo is what the guest reports about the commit a review should
// start from: the commit itself, plus enough about the resulting diff to
// state the range out loud and to refuse an impossible one.
type diffBaseInfo struct {
	// Commit is the base object name, or "" when the guest found none usable
	// and the server should fall back to its own default.
	Commit string
	// Date is Commit's committer date as YYYY-MM-DD, "" when unknown. It is
	// shown because age is the single most legible symptom of a wrong base.
	Date string
	// Commits is how many commits separate Commit from HEAD.
	Commits int
	// Files is how many files the review would cover.
	Files int

	// Resume reports that a previous review is sitting at the checkout's
	// default output path and can be carried in. See diffBaseScript for why
	// this is a flag rather than a path.
	Resume bool
}

// diffBase asks the guest where a review should start and how big it would
// be, returning a zero diffBaseInfo for every failure — a broken lookup must
// degrade to reviewing the working tree, never fail the command.
func (s *Session) diffBase(ctx context.Context) diffBaseInfo {
	// Bounded, like every other guest interaction in this package (waitReady's
	// ReadyTimeout, probeHTTP's probeTimeout, stopGuestServer's
	// guestStopTimeout) — and this one needs it most. It runs BEFORE Run has
	// printed a single line, so on a guest whose sshd accepts the connection
	// and then stalls, an unbounded ShellOut left `sand land --review` hanging
	// forever having produced no output at all: no port, no URL, no hint that
	// anything was happening. Its own contract already degrades every failure
	// to "review the working tree", so a timeout costs nothing but the
	// refinement.
	ctx, cancel := context.WithTimeout(ctx, diffBaseTimeout)
	defer cancel()
	out, err := s.Provider.ShellOut(ctx, s.VM.Name, "sh", "-c", diffBaseScript, "sh", s.Checkout.Path, s.Checkout.DefaultBranch)
	if err != nil {
		return diffBaseInfo{}
	}
	return parseDiffBase(string(out))
}

// installSkills copies the base image's staged assistant skills into the
// checkout, so `/self-review-critique` before a review and
// `/self-review-apply` after one both work in the guest with no setup.
//
// Every failure is reported and none is fatal. The skills bracket a review;
// they are not part of serving one, and a guest whose base predates them (or
// whose copy cannot be written) still has a perfectly good review to run. The
// opposite choice — failing the review because an optional convenience could
// not be installed — would trade the feature for its accessory.
func (s *Session) installSkills(ctx context.Context, w io.Writer) {
	ctx, cancel := context.WithTimeout(ctx, installSkillsTimeout)
	defer cancel()

	out, err := s.Provider.ShellOut(ctx, s.VM.Name, "sh", "-c", installSkillsScript, "sh", s.Checkout.Path, skillsStageDir)
	if err != nil {
		fmt.Fprintf(w, "could not install the review skills into %s: %v\n", s.Checkout.Path, err)
		return
	}

	installed, skipped := countSkillReport(string(out))
	switch {
	case installed == 0 && skipped == 0:
		// Nothing staged: a base image older than the skills. Said once, in
		// the same shape as missingToolHint, because the next create fixes it
		// on its own and there is no flag to pass.
		fmt.Fprintf(w, "no review skills staged in this VM's base image; the next `sand create` brings them in\n")
	case skipped > 0:
		fmt.Fprintf(w, "review skills in %s/: %d installed, %d left alone (this repo tracks its own)\n",
			skillsTargetDir, installed, skipped)
	default:
		fmt.Fprintf(w, "review skills installed in %s/ (%d)\n", skillsTargetDir, installed)
	}
}

// countSkillReport tallies the install script's key=value lines. Anything
// else on the stream — a login banner, a motd — is noise, the same tolerance
// every other parser in this package extends to a real login shell.
func countSkillReport(out string) (installed, skipped int) {
	for _, line := range strings.Split(out, "\n") {
		switch key, _, ok := strings.Cut(strings.TrimSpace(line), "="); {
		case !ok:
		case key == "installed":
			installed++
		case key == "skipped":
			skipped++
		}
	}
	return installed, skipped
}

// removeOutput deletes the checkout's review output and walkthrough sidecar
// inside the guest. It is the whole of "start this review over": upstream
// never removes either file, and a review.xml left in place is silently
// resumed by the next run.
//
// Unexported on purpose. Removing the file and not resuming are two halves of
// one decision, so the only way to ask for either is Session.Fresh — there is
// no way for a caller to do one and forget the other.
func (s *Session) removeOutput(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, removeOutputTimeout)
	defer cancel()

	if _, err := s.Provider.ShellOut(ctx, s.VM.Name, "sh", "-c", removeOutputScript, "sh", s.Checkout.Path); err != nil {
		return fmt.Errorf("removing %s in %s: %w", outputFile, s.VM.Name, err)
	}
	return nil
}

// maxDiffFiles returns the effective refusal threshold.
func (s *Session) maxDiffFiles() int {
	if s.MaxDiffFiles > 0 {
		return s.MaxDiffFiles
	}
	return maxDiffFiles
}

// safeDate matches the one date shape the guest is asked for (git's %cs). It
// is a gate for the same reason objectID is: the value is guest-derived and
// ends up in output the user reads, so anything that is not plainly a date —
// a login banner, an error, a line of someone's motd — is dropped rather
// than printed as though sand had computed it.
var safeDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// parseDiffBase reads the guest's key=value report. Later lines win, because
// the script prints its answer immediately before exiting: any login-shell
// noise a guest prepends is therefore behind it — the same reasoning the
// sweep parser and the heartbeat parser use for the same hazard.
//
// A record whose base fails objectID yields the zero value, never a partial
// one: without a usable commit the counts describe nothing, and reporting
// them beside a working-tree review would be a confident lie.
func parseDiffBase(out string) diffBaseInfo {
	var info diffBaseInfo
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "sandbase":
			if objectID.MatchString(value) {
				info.Commit = value
			}
		case "sanddate":
			if safeDate.MatchString(value) {
				info.Date = value
			}
		case "sandcommits":
			if n, err := strconv.Atoi(value); err == nil && n >= 0 {
				info.Commits = n
			}
		case "sandfiles":
			if n, err := strconv.Atoi(value); err == nil && n >= 0 {
				info.Files = n
			}
		case "sandresume":
			info.Resume = value == "1"
		}
	}
	if info.Commit == "" {
		// The counts describe a commit that was just rejected, so they go —
		// but Resume does not depend on the base at all, and a checkout with
		// no usable base still has a review worth carrying in.
		return diffBaseInfo{Resume: info.Resume}
	}
	return info
}

// describeBase renders the chosen range for the human reading Run's output.
//
// The size and the date are in it deliberately. A wrong base is not otherwise
// visible until the review either fails or opens onto thousands of files
// nobody touched, and "everything since c10bf079 (2021-02-11, 4409 commits,
// 37082 files)" is a sentence that diagnoses itself at a glance — whereas the
// bare object name this used to print told a reader nothing they could check.
func describeBase(info diffBaseInfo) string {
	if info.Commit == "" {
		return "the working tree: no commit older than this VM's own work was found"
	}
	short := info.Commit
	if len(short) > 12 {
		short = short[:12]
	}
	var detail []string
	if info.Date != "" {
		detail = append(detail, info.Date)
	}
	detail = append(detail, plural(info.Commits, "commit"), plural(info.Files, "file"))
	return "everything since " + short + " (" + strings.Join(detail, ", ") + ")"
}

// diffTooLargeError explains a refusal in terms the reader can act on: which
// commit was chosen, how old it is, and the one guest command that shows
// whether the checkout's history and its remotes have drifted apart — which
// is what a base this old always means.
func diffTooLargeError(info diffBaseInfo, limit int) error {
	return fmt.Errorf(
		"the review would cover %s changed since %s — more than the %d-file limit, and past what the browser review tool can load "+
			"(it reads `git diff` through a 50MB buffer and would fail with an unexplained buffer error)\n"+
			"a base that old means this checkout's history and its remote-tracking refs have drifted apart: in the guest, "+
			"`git rev-list --count HEAD --not --remotes` should report the work you expect to review, and `git fetch` the "+
			"remote the branch was built on if it does not",
		plural(info.Files, "file"), describeBase(info), limit)
}

// plural formats a count with its noun, so a one-commit range does not read
// as "1 commits" in the line every review prints.
func plural(n int, singular string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %ss", n, singular)
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
// it back, for use as the near end of an ssh -L. The gap between letting go
// and ssh binding it is a real (if tiny) race, and a loss surfaces as the
// forwarder failing to start rather than as silent misbehaviour.
//
// This is only ever the WORKSTATION's end. The guest's port is upstream's to
// choose, and nothing here can influence it.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// probePath is the route the readiness probe asks for: upstream's smallest
// JSON endpoint. See probeHTTP for why identifying the responder matters.
const probePath = "/api/config"

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
// It must also be OUR server, and accepting any HTTP responder was a real
// hazard rather than a theoretical one. Lima's auto-forward lands a guest
// loopback port on the SAME number on the host — the workstation for local
// Lima, the remote host for remote Lima — and nothing reserves that number
// there. When something else already holds it, the connection reaches that
// other process instead, and a probe satisfied by any response would declare
// readiness, open the reviewer's browser onto an unrelated application, and
// then block forever waiting for a submission that could never arrive.
//
// probePath settles it. It is upstream's own small JSON route, so a 200 whose
// body carries a `config` object is a strong statement that the thing on the
// far end is @self-review/serve — where the bare `/` this used to request is
// just an HTML page, which any number of things serve. Everything else becomes
// an ordinary readiness timeout naming the problem.
func probeHTTP(ctx context.Context, addr string) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+probePath, nil)
	if err != nil {
		return err
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("something is listening on %s but answered %s for %s — "+
			"most likely another process holds this port", addr, resp.Status, probePath)
	}
	var probed struct {
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(body, &probed); err != nil || len(probed.Config) == 0 {
		return fmt.Errorf("something is listening on %s but %s did not answer like the review "+
			"server — most likely another process holds this port", addr, probePath)
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
