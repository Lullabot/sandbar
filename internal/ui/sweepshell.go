package ui

// sweepshell.go is the checkout sweep's runtime: a SECOND long-lived
// `limactl shell` per running VM, a sibling of the stats heartbeat
// (heartbeat.go), not a passenger inside it.
//
// # Why a sibling, not an addition to the heartbeat loop
//
// The heartbeat's guest loop is SEQUENTIAL: cat /proc/stat, cat /proc/meminfo,
// df, sleep, repeat — every 2 seconds, so the gauges read as live. The sweep's
// guest side is a bounded `find` plus a handful of read-only `git` reads per
// discovered checkout (internal/checkouts.BuildSweepCommand): read-only, but
// not FAST — a guest with a few dozen checkouts can take real wall time to
// walk. Injecting that into the heartbeat's sequential loop would stall the
// cat/df/sleep cycle behind it, and the 2s gauges would freeze for exactly as
// long as the sweep takes. So the sweep gets its OWN shell, its OWN goroutine,
// its OWN connection, and its OWN cadence (sweepInterval, ~60s) — the plan's
// central constraint (plan 17, Component 1).
//
// # Sharing the heartbeat's hard-won lessons
//
// Everything heartbeat.go's doc comment says about a `limactl shell` stream
// applies here unchanged, because it is the exact same mechanism against the
// exact same guest transport:
//
//   - `limactl shell` FORKS an ssh client that inherits the pipes. Cancelling
//     the context kills limactl and ORPHANS the ssh unless something makes its
//     next Write fail — see sweepWriter.Write's ctx-aware select, copied from
//     sampleWriter's.
//   - A VM stopped underneath the stream ends the shell on its own within
//     ~300ms (`exit status 255`); the sweep needs no separate detection for
//     that, exactly like the heartbeat.
//   - Quitting sand needs no teardown of its own: process exit closes the read
//     ends, the orphaned ssh takes a SIGPIPE on its next write, and the guest
//     loop dies with the session.
//
// # The concurrency contract (identical to heartbeat.go's)
//
// Bubble Tea passes the model BY VALUE. sweepRegistry is a POINTER field so
// every model copy shares the one registry. A completed sweep pass travels
// from the sweeping goroutine into Update as a sweepResultMsg — never a direct
// write from the goroutine — and it is Update, under the checkouts.Registry's
// own mutex (internal/checkouts), that calls Set. That is the ONLY place the
// checkout registry is written. Readers (the badge, the delete guard, the
// Landing pane) call checkouts.Registry.Get and get a value copy.
//
// # Two streams, two delimiters, never confusable
//
// The sweep runs on its own connection, so its stream can never physically mix
// with the heartbeat's — but sweepEndMarker is still deliberately distinct
// from both heartbeatDelim (heartbeat.go) and internal/checkouts'
// sweepRecordDelim (which ends ONE checkout record, not one whole sweep pass),
// so a host-side bug that ever read the wrong stream fails loudly on a
// delimiter mismatch instead of silently interleaving records. This file's
// parser accumulates raw text between sweepEndMarker lines and hands that
// whole blob to checkouts.ParseSweep unmodified — the per-record delimiter
// inside it is task 2's concern entirely; this file never inspects it.

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/lullabot/sandbar/internal/checkouts"
	"github.com/lullabot/sandbar/internal/registry"

	tea "charm.land/bubbletea/v2"
)

const (
	// sweepInterval is how often the guest runs one full sweep pass. ~60s,
	// per the plan: slow enough that a `find` plus per-repo git reads across a
	// few dozen checkouts is negligible guest load, fast enough that the badge
	// and delete guard are never far behind reality.
	sweepInterval = 60 * time.Second

	// sweepRetry mirrors heartbeatRetry exactly, for exactly the same reason:
	// without it, a VM Lima calls Running but that cannot be shelled into (one
	// still booting, one with a wedged sshd) would have a fresh `limactl
	// shell` thrown at it by every single syncSweeps reconciliation. A
	// deliberate stop sets no cooldown, so coming back to the board is
	// instant.
	sweepRetry = 5 * time.Second

	// sweepEndMarker terminates ONE FULL SWEEP PASS in the guest stream —
	// distinct from heartbeatDelim (a different stream entirely) and from
	// internal/checkouts' sweepRecordDelim (which ends one CHECKOUT record
	// within a pass, not the pass itself). Nothing BuildSweepCommand's output
	// can produce collides with it.
	sweepEndMarker = "---sand-sweep-pass-end---"

	// sweepCarryLimit caps the partial LINE the parser holds across reads,
	// mirroring heartbeat.go's carryLimit: a guest that streams without a
	// newline cannot grow this without bound.
	sweepCarryLimit = 64 << 10

	// sweepBufferLimit caps how many bytes of a single sweep PASS the parser
	// accumulates before seeing sweepEndMarker. A real pass (sweepMaxCheckouts
	// records worth of key=value lines, internal/checkouts) is on the order
	// of a few tens of KB; this is a generous multiple of that, so it only
	// ever bites a pathological stream (one whose end marker never arrives) —
	// which is treated as noise and dropped, not as a fatal error, matching
	// every other tolerant-failure posture in this file and the heartbeat's.
	sweepBufferLimit = 4 << 20
)

// guestSweepScript is the in-guest loop: run the sweep command
// (internal/checkouts.BuildSweepCommand), print the end-of-pass marker, sleep,
// repeat. Like the heartbeat's guestScript, everything CLEVER lives on the
// host (internal/checkouts' classification, this file's lifecycle) — the
// guest side is the sweep command plus the dumbest possible wrapper loop.
func guestSweepScript(every time.Duration) string {
	secs := strconv.Itoa(int(every / time.Second))
	return "while true; do\n" + checkouts.BuildSweepCommand() +
		"echo '" + sweepEndMarker + "'\nsleep " + secs + "\ndone"
}

// sweepParser turns the guest sweep stream's bytes into completed sweep
// passes. It is fed whatever the pipe hands over — not passes, not even
// lines — for the same reason sampleParser is: a real `limactl shell` tears
// chunks across line (and record) boundaries.
type sweepParser struct {
	carry []byte       // the tail of the last chunk, up to the next newline
	buf   bytes.Buffer // raw text accumulated since the last completed pass
}

// feed folds one chunk in and returns every sweep pass completed by it —
// usually none or one, but a slow reader can hand over several at once.
func (p *sweepParser) feed(chunk []byte) []checkouts.VMCheckouts {
	var out []checkouts.VMCheckouts

	buf := chunk
	if len(p.carry) > 0 {
		buf = append(p.carry, chunk...)
		p.carry = nil
	}

	for {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			break
		}
		line := buf[:i]
		buf = buf[i+1:]

		if string(bytes.TrimSpace(bytes.TrimRight(line, "\r"))) == sweepEndMarker {
			// Hand the WHOLE accumulated pass to task 2's parser unmodified —
			// this file does no classification of its own, and never
			// inspects sweepRecordDelim; that is entirely ParseSweep's
			// concern.
			out = append(out, checkouts.ParseSweep(p.buf.String()))
			p.buf.Reset()
			continue
		}

		p.buf.Write(line)
		p.buf.WriteByte('\n')
		if p.buf.Len() > sweepBufferLimit {
			// A pass whose end marker never arrives (a guest stuck emitting
			// noise) must not grow this without bound. Dropped, not errored —
			// the same tolerant-noise posture the heartbeat parser takes on
			// an over-long carry.
			p.buf.Reset()
		}
	}

	if len(buf) > sweepCarryLimit {
		buf = nil
	}
	// COPY it — os/exec hands the same backing array to every Write, so a
	// retained slice would leave the carry silently rewritten by the next
	// read.
	p.carry = append([]byte(nil), buf...)
	return out
}

// sweepWriter is the io.Writer the streaming shell writes into: it parses
// inline, so the whole sweep is ONE goroutine per VM, mirroring sampleWriter
// exactly, including why its Write must never block forever (os/exec's copy
// goroutine backs cmd.Wait() with it) and why an erroring Write is what hands
// the orphaned ssh its SIGPIPE.
type sweepWriter struct {
	ctx context.Context
	out chan<- checkouts.VMCheckouts
	p   sweepParser
}

func (w *sweepWriter) Write(b []byte) (int, error) {
	for _, vc := range w.p.feed(b) {
		select {
		case w.out <- vc:
		case <-w.ctx.Done():
			return 0, w.ctx.Err()
		}
	}
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return len(b), nil
}

// sweepConn is one VM's live sweep connection. Like a heartbeat, it lives
// only inside the registry, always behind a pointer, and is never handed out.
type sweepConn struct {
	// epoch identifies THIS connection — see heartbeat's epoch doc for why: a
	// sweep stopped and restarted for the same VM must not let a sample in
	// flight from the old connection get folded into (or restart a second
	// read loop against) the new one.
	epoch  uint64
	cancel context.CancelFunc
	ch     chan checkouts.VMCheckouts
}

// sweepRegistry owns every live sweep connection, mirroring heartbeatRegistry
// field for field and method for method. A nil *sweepRegistry is safe to call
// every method on and reports "no sweeps", so a hand-built model needs none.
type sweepRegistry struct {
	mu     sync.Mutex
	sweeps map[vmHandle]*sweepConn

	// cooldown holds VMs whose sweep connection DIED ON ITS OWN, and until
	// when they may not be retried. See sweepRetry.
	cooldown map[vmHandle]time.Time

	nextEpoch uint64

	// shell resolves the backend seam for a sweep's scope — the SAME
	// shellFor/guestShell seam the heartbeat uses (fleetShellResolver), so a
	// remote-profile VM's sweep opens into the remote host exactly like its
	// heartbeat does.
	shell    shellFor
	interval time.Duration
	retry    time.Duration
}

// newSweeps builds a registry whose sweeps all open through the SAME shell,
// regardless of scope — the single-provider case and every test that passes
// one concrete shell (a nil shell means start opens nothing).
// newSweepsResolver is the fleet form that dispatches per scope.
func newSweeps(shell guestShell) *sweepRegistry {
	return newSweepsResolver(func(registry.Scope) guestShell { return shell })
}

func newSweepsResolver(resolve shellFor) *sweepRegistry {
	return &sweepRegistry{
		sweeps:   make(map[vmHandle]*sweepConn),
		cooldown: make(map[vmHandle]time.Time),
		shell:    resolve,
		interval: sweepInterval,
		retry:    sweepRetry,
	}
}

// setShell REBUILDS the registry's scope->shell resolver, mirroring
// heartbeatRegistry.setShell exactly (see its doc comment for the fleet
// -mutation rationale this exists for).
func (r *sweepRegistry) setShell(resolve shellFor) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.shell = resolve
	r.mu.Unlock()
}

// start opens a sweep connection for (scope, name) and spawns its runner. It
// reports false — and starts nothing — when one is already open, when the VM
// is in its post-failure cooldown, or when there is no shell to open (a
// hand-built model). Keyed on the full vmHandle so a VM under one scope never
// collides with a same-named VM under another.
func (r *sweepRegistry) start(scope registry.Scope, name string) (uint64, <-chan checkouts.VMCheckouts, bool) {
	if r == nil {
		return 0, nil, false
	}
	r.mu.Lock()
	resolve := r.shell
	r.mu.Unlock()
	if resolve == nil {
		return 0, nil, false
	}
	shell := resolve(scope)
	if shell == nil {
		return 0, nil, false
	}
	key := vmHandle{Scope: scope, Name: name}
	r.mu.Lock()
	if _, ok := r.sweeps[key]; ok {
		r.mu.Unlock()
		return 0, nil, false
	}
	if until, ok := r.cooldown[key]; ok && time.Now().Before(until) {
		r.mu.Unlock()
		return 0, nil, false
	}
	delete(r.cooldown, key)

	r.nextEpoch++
	epoch := r.nextEpoch
	ctx, cancel := context.WithCancel(context.Background())
	// Buffered by one so the sweeper can hand off a completed pass and get
	// straight back to reading the stream, without waiting for Update to come
	// round — mirrors heartbeat's channel exactly.
	ch := make(chan checkouts.VMCheckouts, 1)
	r.sweeps[key] = &sweepConn{epoch: epoch, cancel: cancel, ch: ch}
	interval := r.interval
	r.mu.Unlock()

	go func() {
		// The SENDER closes, always — including on every error path. It is
		// what wakes the blocked read command, which is what tells Update the
		// sweep connection is over.
		defer close(ch)
		w := &sweepWriter{ctx: ctx, out: ch}
		// The error is deliberately dropped, exactly as the heartbeat's is:
		// every way this returns (VM stopped, context cancelled, no /bin/sh)
		// means the same thing to the registry — this VM has no sweep
		// connection any more.
		_ = shell.ShellStreamOut(ctx, name, nil, w, "sh", "-c", guestSweepScript(interval))
	}()

	return epoch, ch, true
}

// stop ends (scope, name)'s sweep connection DELIBERATELY: because the VM
// left Running, or the board is no longer the screen the user is on. Scoped:
// stopping this VM must never reach into another scope's same-named sweep.
// No cooldown is recorded: this connection did not fail.
func (r *sweepRegistry) stop(scope registry.Scope, name string) {
	if r == nil {
		return
	}
	key := vmHandle{Scope: scope, Name: name}
	r.mu.Lock()
	sc, ok := r.sweeps[key]
	if ok {
		delete(r.sweeps, key)
	}
	r.mu.Unlock()

	if ok {
		sc.cancel()
	}
}

// stopAll ends every sweep connection — mirrors heartbeatRegistry.stopAll.
func (r *sweepRegistry) stopAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	stopping := make([]*sweepConn, 0, len(r.sweeps))
	for key, sc := range r.sweeps {
		stopping = append(stopping, sc)
		delete(r.sweeps, key)
	}
	r.mu.Unlock()

	for _, sc := range stopping {
		sc.cancel()
	}
}

// fold validates that a completed pass still belongs to the LIVE connection
// for (scope, name) and hands back the channel to read the next one from. A
// stale epoch — a pass from a connection that has since been stopped, and
// perhaps replaced — reports a nil channel, which ends that read loop instead
// of letting it double up on the live one. Unlike heartbeatRegistry.fold, it
// records nothing on the entry itself: the sweep's own registry holds no
// "latest" reading for a renderer to consult — the checkout registry
// (internal/checkouts, Update's sweepResultMsg handler) is that spine.
func (r *sweepRegistry) fold(scope registry.Scope, name string, epoch uint64) <-chan checkouts.VMCheckouts {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	sc, ok := r.sweeps[vmHandle{Scope: scope, Name: name}]
	if !ok || sc.epoch != epoch {
		return nil
	}
	return sc.ch
}

// ended is what the closed channel means: the stream finished on its own —
// most commonly a VM being stopped underneath it, exactly like the
// heartbeat's ended. The entry is dropped and a cooldown is set, mirroring
// heartbeatRegistry.ended precisely (see its doc for the reasoning). A stale
// epoch means this connection was already stopped deliberately and this is
// just its goroutine finishing: nothing to drop, nothing to cool down.
func (r *sweepRegistry) ended(scope registry.Scope, name string, epoch uint64) {
	if r == nil {
		return
	}
	key := vmHandle{Scope: scope, Name: name}
	r.mu.Lock()
	defer r.mu.Unlock()
	if sc, ok := r.sweeps[key]; ok && sc.epoch == epoch {
		delete(r.sweeps, key)
		r.cooldown[key] = time.Now().Add(r.retry)
	}
}

// names lists the VMs with a sweep connection open IN scope, in no order.
func (r *sweepRegistry) names(scope registry.Scope) []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.sweeps))
	for key := range r.sweeps {
		if key.Scope == scope {
			out = append(out, key.Name)
		}
	}
	return out
}

// forget drops scope's VMs' cooldowns that are not in keep, mirroring
// heartbeatRegistry.forget — scoped so it never clears another scope's
// cooldown entries.
func (r *sweepRegistry) forget(scope registry.Scope, keep map[string]bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.cooldown {
		if key.Scope == scope && !keep[key.Name] {
			delete(r.cooldown, key)
		}
	}
}

// sweepResultMsg carries one completed sweep pass — or, with ok false, the end
// of that VM's sweep stream — back into Update. Keyed by the full (scope, vm)
// handle and epoch, exactly like heartbeatSampleMsg, so a pass routes to the
// owning member's sweep and a stale connection's pass can never be recorded
// against its successor.
type sweepResultMsg struct {
	scope  registry.Scope
	vm     string
	epoch  uint64
	result checkouts.VMCheckouts
	ok     bool
}

// sweepReadCmd waits for one completed pass. Same shape as heartbeatReadCmd:
// the blocking receive happens on a tea.Cmd's goroutine, never in Update, and
// Update re-issues it for the next pass. The receive is what ends this
// goroutine when the sweeper closes the channel.
func sweepReadCmd(scope registry.Scope, name string, epoch uint64, ch <-chan checkouts.VMCheckouts) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		vc, ok := <-ch
		return sweepResultMsg{scope: scope, vm: name, epoch: epoch, result: vc, ok: ok}
	}
}

// syncSweeps reconciles the open sweep connections against the VMs that
// should have one, and is called after EVERY message (see Update) — mirroring
// syncHeartbeats' call site, its running-status + board-roster gate, AND its
// shouldTick idle gate, exactly.
//
// The plan's own clarification reads "always, for every running VM" — and a
// sweep gated on nothing but Status == limaRunning was this file's first
// draft — but that turned out to be a real behavioral change disguised as a
// narrow one: shouldTick's "gauges nobody can see are not worth an SSH
// connection" reasoning applies just as literally to a SECOND connection.
// Reusing shouldTick here (the task's explicitly offered alternative to
// running unconditionally) keeps the sweep's footprint identical to the
// heartbeat's — one extra idle-aware SSH connection per visible, running VM,
// never one held open behind a screen nobody is on or a session nobody is
// driving — and, concretely, avoids a real regression this file's first draft
// produced: any test (or real session) that drives a Running managed VM
// through a screen OTHER than the board (the secrets editor, the file
// browser, profile management) would otherwise pick up an extra background
// guest-shell connection outside the one screen that actually reads the
// registry. Since the badge/guard only ever render on the board anyway, a
// sweep that runs only while shouldTick holds loses nothing a user watching
// the board would notice — the registry catches back up within one
// sweepInterval of returning, exactly as the heartbeat's gauges do.
func (m model) syncSweeps() tea.Cmd {
	if m.sweeps == nil {
		return nil
	}
	if !m.shouldTick() {
		m.sweeps.stopAll()
		return nil
	}

	board := m.boardVMs()
	var cmds []tea.Cmd
	for i := range m.members {
		sc := m.members[i].scope
		want := map[string]bool{}
		for _, bv := range board {
			if bv.scope == sc && bv.Status == limaRunning {
				want[bv.Name] = true
			}
		}
		for _, name := range m.sweeps.names(sc) {
			if !want[name] {
				m.sweeps.stop(sc, name)
			}
		}
		for name := range want {
			if epoch, ch, ok := m.sweeps.start(sc, name); ok {
				cmds = append(cmds, sweepReadCmd(sc, name, epoch, ch))
			}
		}
		m.sweeps.forget(sc, want)
	}
	return tea.Batch(cmds...)
}

// sweepOnceTimeout bounds the delete-time refresh (sweepOnce). The guard must
// never become a way for a wedged guest to freeze the confirmation the user is
// trying to answer: past this, the guard falls back to the cached entry and
// says so. It is deliberately short — the sweep is a bounded, read-only pass
// that a healthy guest finishes well inside it.
const sweepOnceTimeout = 2 * time.Second

// sweepOnce runs ONE sweep pass against a running VM and returns the parsed
// result, for the delete guard's "is this cached picture still true" refresh.
//
// It runs the SAME read-only BuildSweepCommand the VM's live sweep loop is
// already executing every sweepInterval — this is deliberately not a new
// capability, just an early instance of an existing one, which is why it is
// gated on the VM being CURRENTLY RUNNING. A stopped VM is never swept here:
// starting one to inspect it is exactly the guest contact the delete guard
// exists to avoid (see deleteguard.go and AGENTS.md's Landing invariants).
//
// Unlike start, this opens and closes its own short-lived shell rather than
// borrowing the loop's: the loop is asleep between passes, and there is no
// portable way to wake it (the guest's /bin/sh is often dash, whose `read`
// has no timeout flag, so the sleep cannot be made interruptible without
// giving up the "dumbest possible guest script" rule this file is built on).
func (r *sweepRegistry) sweepOnce(ctx context.Context, scope registry.Scope, name string) (checkouts.VMCheckouts, error) {
	if r == nil {
		return checkouts.VMCheckouts{}, errors.New("no sweep registry")
	}
	r.mu.Lock()
	resolve := r.shell
	r.mu.Unlock()
	if resolve == nil {
		return checkouts.VMCheckouts{}, errors.New("no shell resolver")
	}
	shell := resolve(scope)
	if shell == nil {
		return checkouts.VMCheckouts{}, errors.New("no shell for scope")
	}

	ctx, cancel := context.WithTimeout(ctx, sweepOnceTimeout)
	defer cancel()

	var buf bytes.Buffer
	if err := shell.ShellStreamOut(ctx, name, nil, &buf, "sh", "-c", checkouts.BuildSweepCommand()); err != nil {
		return checkouts.VMCheckouts{}, err
	}
	return checkouts.ParseSweep(buf.String()), nil
}
