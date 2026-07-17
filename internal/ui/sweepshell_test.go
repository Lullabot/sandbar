package ui

import (
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/checkouts"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
)

// sweepRecordDelimLiteral duplicates internal/checkouts' unexported
// sweepRecordDelim exactly (its own sweep_test.go pins the same literal
// against its own package). This file has no access to that private constant
// and needs it only to build a SYNTHETIC checkout record that ParseSweep will
// recognize — proving the message plumbing carries a real parsed row, not
// just an empty VMCheckouts.
const sweepRecordDelimLiteral = "---sand-sweep-record---"

// fakeCheckoutRecord builds one synthetic `key=value` checkout record, in
// exactly the shape internal/checkouts.ParseSweep expects from a real guest
// sweep: a pushed branch with a remote, no ahead/behind, clean.
func fakeCheckoutRecord(path, branch string) string {
	return "path=" + path + "\n" +
		"kind=repo\n" +
		"branch=" + branch + "\n" +
		"remote=origin\n" +
		"url=https://github.com/acme/widget.git\n" +
		"tracking=1\n" +
		"ahead=0\n" +
		"behind=0\n" +
		"dirty=0\n" +
		sweepRecordDelimLiteral + "\n"
}

// fakeSweepPass joins zero or more synthetic records into ONE complete sweep
// pass, terminated by sweepEndMarker — the unit sweepParser (and, through it,
// a sweepConn's stream) completes a checkouts.VMCheckouts on.
func fakeSweepPass(records ...string) string {
	return strings.Join(records, "") + sweepEndMarker + "\n"
}

// sweepModel is heartbeatModel's sweep counterpart: it wires m.sweeps to a
// fake shell entirely under the test's control, leaving m.heartbeats as
// newTestModel built it (a real, fleet-resolved registry over the harmless
// fakeRunner-backed provider every other TUI test already tolerates) so the
// two subsystems never share — and can never be confused with — the same
// fake shell's open/close event counters.
func sweepModel(t *testing.T, sh *fakeShell, managed ...string) model {
	t.Helper()
	m := newTestModel(t)
	for _, name := range managed {
		if err := m.reg.Add(vm.CreateConfig{Name: name, BaseName: "sandbar-base"}); err != nil {
			t.Fatalf("seed %s as managed: %v", name, err)
		}
	}
	m.sweeps = newSweeps(sh)
	return m
}

// A sweep is a SIBLING of the heartbeat, not a passenger inside it: it opens
// on the same running-state transition, through its own connection, with its
// own guest command — never the heartbeat's /proc loop.
func TestSweepFollowsTheRunningTransitionOnItsOwnConnection(t *testing.T) {
	sh := newFakeShell()
	l := newTeaLoop(t, sweepModel(t, sh, "web"))

	// Stopped: no sweep.
	l.send(vmsLoadedMsg{vms: vms("web", "Stopped")})
	if n := len(l.m.sweeps.names(registry.LocalScope)); n != 0 {
		t.Fatalf("a stopped VM has %d sweep connections, want 0", n)
	}

	// It starts.
	l.send(vmsLoadedMsg{vms: vms("web", "Running")})
	sh.await(t, "open:web")

	if argv := sh.argvFor("web"); len(argv) != 3 || argv[0] != "sh" || argv[1] != "-c" ||
		!strings.Contains(argv[2], "find") || !strings.Contains(argv[2], sweepEndMarker) {
		t.Fatalf("argv = %q, want the sweep loop (find + sweepEndMarker) under `sh -c`", argv)
	}

	// ONE shell per VM. A second refresh while it is already Running must not
	// open a second connection.
	l.send(vmsLoadedMsg{vms: vms("web", "Running")})
	if n := sh.opened("web"); n != 1 {
		t.Fatalf("opened %d sweep shells for one VM, want 1 (idempotent start)", n)
	}

	// It stops when the VM leaves Running.
	l.send(vmsLoadedMsg{vms: vms("web", "Stopped")})
	sh.await(t, "close:web")
	if n := len(l.m.sweeps.names(registry.LocalScope)); n != 0 {
		t.Fatalf("a VM that left Running still has %d sweep connections", n)
	}

	// And it comes back when the VM does, as a fresh connection.
	l.send(vmsLoadedMsg{vms: vms("web", "Running")})
	sh.await(t, "open:web")
	if n := sh.opened("web"); n != 2 {
		t.Fatalf("web's sweep shell was opened %d times, want 2 (once per Running spell)", n)
	}
}

// Mirrors TestHeartbeatNeverOpensForAnUnmanagedVM: gauges (and now sweeps)
// nobody can see are not worth an SSH connection, so an unmanaged Lima
// instance — Running, but no tile on the board — must never get a sweep
// shell either.
func TestSweepNeverOpensForAnUnmanagedVM(t *testing.T) {
	sh := newFakeShell()
	l := newTeaLoop(t, sweepModel(t, sh)) // nothing recorded as managed

	l.send(vmsLoadedMsg{vms: vms("web", "Running", "stray", "Running")})
	l.send(vmsLoadedMsg{vms: vms("web", "Running", "stray", "Running")})

	if n := len(l.m.sweeps.names(registry.LocalScope)); n != 0 {
		t.Fatalf("%d sweep connections open for a fleet with no managed VM at all", n)
	}
	if got := sh.opened("web"); got != 0 {
		t.Fatalf("web is unmanaged: its sweep shell must never open, got %d opens", got)
	}
	if got := sh.opened("stray"); got != 0 {
		t.Fatalf("stray is unmanaged: its sweep shell must never open, got %d opens", got)
	}
}

// The sweep reuses the SAME shouldTick idle gate as the heartbeat (see
// syncSweeps' doc comment for why the plan's "always, for every running VM"
// reading was rejected in favor of this): leaving the board closes it, and
// coming back reopens it — an idle-aware second SSH connection, never an
// unconditional one.
func TestSweepIsIdleGatedLikeTheHeartbeat(t *testing.T) {
	sh := newFakeShell()
	l := newTeaLoop(t, sweepModel(t, sh, "web"))

	l.send(vmsLoadedMsg{vms: vms("web", "Running")})
	sh.await(t, "open:web")

	// The board is no longer the active screen: the sweep must close, exactly
	// like the heartbeat.
	l.send(runeKey('n'))
	if l.m.view != viewForm {
		t.Fatalf("expected the create form, got view %v", l.m.view)
	}
	sh.await(t, "close:web")
	if n := len(l.m.sweeps.names(registry.LocalScope)); n != 0 {
		t.Fatalf("%d sweep connections still open behind another screen", n)
	}

	// Back to the board: it reopens.
	l.send(tea.KeyPressMsg{Code: tea.KeyEsc})
	if l.m.view != viewBoard {
		t.Fatalf("esc should return to the board, got view %v", l.m.view)
	}
	sh.await(t, "open:web")

	// A long-idle session closes it too, even while nominally on the board.
	l.m.lastInput = time.Now().Add(-2 * heartbeatIdleAfter)
	if l.m.shouldTick() {
		t.Fatal("precondition: the idle gate should be shut")
	}
	l.send(vmsLoadedMsg{vms: vms("web", "Running")})
	sh.await(t, "close:web")
}

// The core message-plumbing contract: a completed sweep pass reaches the
// checkout registry ONLY via Update handling sweepResultMsg — never a direct
// write from the sweeping goroutine — and Set is what a reader (the badge,
// the delete guard, task 4/5/7) will see.
func TestSweepResultFlowsIntoCheckoutRegistryOnlyViaUpdate(t *testing.T) {
	sh := newFakeShell()
	l := newTeaLoop(t, sweepModel(t, sh, "web"))

	l.send(vmsLoadedMsg{vms: vms("web", "Running")})
	sh.await(t, "open:web")

	if _, ok := l.m.checkouts.Get(registry.LocalScope, "web"); ok {
		t.Fatal("no sweep pass has completed yet; the registry must have nothing for web")
	}

	sh.write(t, "web", fakeSweepPass(fakeCheckoutRecord("/home/user/widget", "feature-x")))
	l.pump("web's first sweep pass", func(m model) bool {
		_, ok := m.checkouts.Get(registry.LocalScope, "web")
		return ok
	})

	vc, ok := l.m.checkouts.Get(registry.LocalScope, "web")
	if !ok {
		t.Fatal("expected a recorded VMCheckouts for web")
	}
	if len(vc.Checkouts) != 1 {
		t.Fatalf("got %d checkouts, want 1: %+v", len(vc.Checkouts), vc.Checkouts)
	}
	row := vc.Checkouts[0]
	if row.Path != "/home/user/widget" || row.Branch != "feature-x" {
		t.Fatalf("unexpected row: %+v", row)
	}
	if row.PushState != checkouts.PushStatePushed {
		t.Fatalf("push state = %v, want pushed (tracking=1, ahead=0)", row.PushState)
	}

	// A SECOND pass, with a different checkout, replaces (does not append to)
	// the VM's recorded set — each pass is the sweep's full current picture.
	sh.write(t, "web", fakeSweepPass(fakeCheckoutRecord("/home/user/other", "main")))
	l.pump("web's second sweep pass", func(m model) bool {
		vc, ok := m.checkouts.Get(registry.LocalScope, "web")
		return ok && len(vc.Checkouts) == 1 && vc.Checkouts[0].Path == "/home/user/other"
	})
}

// THE VM IS STOPPED UNDERNEATH THE STREAM, mirroring
// TestHeartbeatDiesCleanlyWhenTheVMStopsUnderneathIt: the sweep must end
// itself, and a VM whose shell keeps dying must not be retried on every
// single message — it earns a cooldown, not a blacklist.
func TestSweepDiesCleanlyWhenTheVMStopsUnderneathIt(t *testing.T) {
	sh := newFakeShell()
	l := newTeaLoop(t, sweepModel(t, sh, "web"))

	l.send(vmsLoadedMsg{vms: vms("web", "Running")})
	sh.await(t, "open:web")
	sh.write(t, "web", fakeSweepPass(fakeCheckoutRecord("/home/user/widget", "main")))
	l.pump("web's sweep pass", func(m model) bool {
		_, ok := m.checkouts.Get(registry.LocalScope, "web")
		return ok
	})

	sh.die(t, "web", errors.New("exit status 255"))
	sh.await(t, "close:web")
	l.pump("the sweep connection to end", func(m model) bool {
		return len(m.sweeps.names(registry.LocalScope)) == 0
	})

	for i := 0; i < 5; i++ {
		l.send(vmsLoadedMsg{vms: vms("web", "Running")})
	}
	if n := sh.opened("web"); n != 1 {
		t.Fatalf("a VM whose sweep shell keeps dying had %d opened at it across 5 refreshes — must not retry every tick", n)
	}

	// The cooldown lapses: sand tries again.
	l.m.sweeps.mu.Lock()
	l.m.sweeps.cooldown[vmHandle{Scope: registry.LocalScope, Name: "web"}] = time.Now().Add(-time.Second)
	l.m.sweeps.mu.Unlock()
	l.send(vmsLoadedMsg{vms: vms("web", "Running")})
	sh.await(t, "open:web")
}

// A LEAKED GOROUTINE IS A FAILURE: each one holds an SSH connection into a
// guest. This mirrors TestHeartbeatsLeaveNoGoroutinesBehind exactly, over a
// stream that IGNORES its context — precisely the orphaned-ssh hazard the
// heartbeat's teardown was built against — so nothing but a failing Write can
// end it. Reusing that same ctx-aware Write (sweepWriter, copied from
// sampleWriter) is what this test proves works for the sweep too.
func TestSweepsLeaveNoGoroutinesBehind(t *testing.T) {
	sh := newFakeShell()
	sh.ignoresCtx = true

	r := newSweeps(sh)
	base := runtime.NumGoroutine()

	// seen records the last pass each VM's reader goroutine folded, guarded by
	// its own mutex since it is written from three reader goroutines and read
	// from the test goroutine below.
	var mu sync.Mutex
	seen := map[string]int{}

	var readers sync.WaitGroup
	for _, name := range []string{"a", "b", "c"} {
		epoch, ch, ok := r.start(registry.LocalScope, name)
		if !ok {
			t.Fatalf("%s: start failed", name)
		}
		readers.Add(1)
		go func(name string, epoch uint64, ch <-chan checkouts.VMCheckouts) {
			defer readers.Done()
			// The read loop, as Update drives it: read, fold (validate the
			// connection is still live), read again — until the sweeper
			// closes the channel. A sweeper that never closes parks this
			// goroutine forever, which is exactly the leak this test exists
			// to catch.
			for {
				vc, open := <-ch
				if !open {
					return
				}
				if next := r.fold(registry.LocalScope, name, epoch); next == nil {
					return
				}
				mu.Lock()
				seen[name] += len(vc.Checkouts)
				mu.Unlock()
			}
		}(name, epoch, ch)
		sh.await(t, "open:"+name)
	}

	sh.write(t, "a", fakeSweepPass(fakeCheckoutRecord("/home/user/a-repo", "main")))
	waitFor(t, "a's sweep pass", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return seen["a"] == 1
	})

	// The gate slams shut: the user backgrounded the terminal, or the VM left
	// Running — either way, every connection must go.
	r.stopAll()

	for _, name := range []string{"a", "b", "c"} {
		sh.await(t, "close:"+name)
	}
	readers.Wait()

	if n := len(r.names(registry.LocalScope)); n != 0 {
		t.Fatalf("%d sweep connections survived stopAll", n)
	}
	waitForGoroutines(t, base)
}

// A pass from a connection that has already been replaced must be DROPPED,
// and its read loop must END — mirrors TestStaleSampleIsDroppedAndItsReadLoopEnds.
// Recording it against the new connection (or letting two loops attach to the
// same channel) is exactly the hazard epoch-keying exists to prevent.
func TestStaleSweepPassIsDroppedAndItsReadLoopEnds(t *testing.T) {
	sh := newFakeShell()
	r := newSweeps(sh)

	old, _, ok := r.start(registry.LocalScope, "web")
	if !ok {
		t.Fatal("start")
	}
	sh.await(t, "open:web")
	r.stop(registry.LocalScope, "web")
	sh.await(t, "close:web")

	fresh, _, ok := r.start(registry.LocalScope, "web")
	if !ok {
		t.Fatal("restart")
	}
	sh.await(t, "open:web")
	if fresh == old {
		t.Fatal("a new connection must get a new epoch, or a stale pass cannot be told from a live one")
	}

	if next := r.fold(registry.LocalScope, "web", old); next != nil {
		t.Fatal("a stale connection's read loop must end, not attach itself to the live one")
	}
	if next := r.fold(registry.LocalScope, "web", fresh); next == nil {
		t.Fatal("the live connection must keep reading")
	}

	r.stopAll()
	sh.await(t, "close:web")
}

// --- sweepParser unit tests: the pure parsing logic, independent of any shell. ---

// One pass split across several chunks — including mid-line and mid-marker
// tears — must still complete exactly once, on the end marker.
func TestSweepParserAssemblesATornPass(t *testing.T) {
	var p sweepParser

	full := fakeSweepPass(fakeCheckoutRecord("/home/user/widget", "main"))
	mid := len(full) / 2

	out := p.feed([]byte(full[:mid]))
	if len(out) != 0 {
		t.Fatalf("a torn pass must not complete early, got %d", len(out))
	}
	out = p.feed([]byte(full[mid:]))
	if len(out) != 1 {
		t.Fatalf("got %d completed passes, want 1", len(out))
	}
	if len(out[0].Checkouts) != 1 || out[0].Checkouts[0].Path != "/home/user/widget" {
		t.Fatalf("unexpected parse result: %+v", out[0])
	}
}

// Several passes delivered in ONE chunk must all complete, in order.
func TestSweepParserCompletesMultiplePassesInOneChunk(t *testing.T) {
	var p sweepParser

	chunk := fakeSweepPass(fakeCheckoutRecord("/a", "main")) +
		fakeSweepPass(fakeCheckoutRecord("/b", "main"))

	out := p.feed([]byte(chunk))
	if len(out) != 2 {
		t.Fatalf("got %d passes, want 2", len(out))
	}
	if out[0].Checkouts[0].Path != "/a" || out[1].Checkouts[0].Path != "/b" {
		t.Fatalf("passes out of order or wrong content: %+v", out)
	}
}

// A pass whose end marker never arrives must not grow the parser's buffer
// without bound — it is dropped as noise, mirroring the heartbeat parser's
// own over-long-carry tolerance.
func TestSweepParserDropsAnOverlongPass(t *testing.T) {
	var p sweepParser

	// Feed well past sweepBufferLimit with no end marker.
	junk := strings.Repeat("noise-line-that-is-not-a-field\n", (sweepBufferLimit/32)+10)
	p.feed([]byte(junk))
	if p.buf.Len() > sweepBufferLimit {
		t.Fatalf("buffer grew to %d bytes past the %d limit", p.buf.Len(), sweepBufferLimit)
	}

	// The parser must still be usable afterward: a fresh pass completes
	// normally.
	out := p.feed([]byte(fakeSweepPass(fakeCheckoutRecord("/still/works", "main"))))
	if len(out) != 1 || out[0].Checkouts[0].Path != "/still/works" {
		t.Fatalf("parser did not recover after an overlong pass: %+v", out)
	}
}

// guestSweepScript must wrap BuildSweepCommand in the dumb while/sleep loop,
// terminated by sweepEndMarker, and must never be confusable with the
// heartbeat's own guest script or delimiter.
func TestGuestSweepScriptShape(t *testing.T) {
	script := guestSweepScript(sweepInterval)

	if !strings.Contains(script, "find") {
		t.Fatalf("script does not contain the sweep's find command: %q", script)
	}
	if !strings.Contains(script, sweepEndMarker) {
		t.Fatal("script must emit sweepEndMarker at the end of each pass")
	}
	if strings.Contains(script, heartbeatDelim) {
		t.Fatal("the sweep script must never contain the heartbeat's delimiter")
	}
	if sweepEndMarker == heartbeatDelim {
		t.Fatal("sweepEndMarker must be distinct from heartbeatDelim")
	}
	if !strings.Contains(script, "sleep 60") {
		t.Fatalf("script must sleep for sweepInterval's seconds, got %q", script)
	}
}
