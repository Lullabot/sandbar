package ui

import (
	"context"
	"errors"
	"io"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
)

// fakeShell is a guestShell under the test's control: it opens a stream per VM,
// writes exactly the bytes the test hands it, and ends when the test says so or
// when its context is cancelled.
//
// It faithfully models the one thing about the real stream that matters most, and
// that a naive fake would get wrong: a stream can IGNORE its context. On a real VM
// it does — `limactl shell` forks an ssh client, cancelling the context kills only
// limactl, and the orphaned ssh goes on writing into the pipe. The only thing that
// stops it is its Write failing. ignoresCtx reproduces that, so the teardown path
// is tested against the stream that actually exists rather than a polite one.
type fakeShell struct {
	mu    sync.Mutex
	opens map[string]int // how many shells have EVER been opened per VM
	feed  map[string]chan string
	end   map[string]chan error
	argv  map[string][]string // the argv each VM's shell was opened with

	// Events are COUNTED, not queued. Several VMs' streams open and close
	// concurrently, so a channel of events would let one await() swallow another
	// VM's event while hunting for its own — a hang that looks exactly like the
	// goroutine leak these tests exist to catch, and would have been debugged as one.
	events map[string]int // "open:web" / "close:web" -> times it has happened
	taken  map[string]int // times a test has already awaited it

	ignoresCtx bool
}

func newFakeShell() *fakeShell {
	return &fakeShell{
		opens:  map[string]int{},
		feed:   map[string]chan string{},
		end:    map[string]chan error{},
		argv:   map[string][]string{},
		events: map[string]int{},
		taken:  map[string]int{},
	}
}

// emit records that an event happened.
func (f *fakeShell) emit(event string) {
	f.mu.Lock()
	f.events[event]++
	f.mu.Unlock()
}

func (f *fakeShell) ShellStreamOut(ctx context.Context, name string, _ io.Reader, out io.Writer, argv ...string) error {
	f.mu.Lock()
	f.opens[name]++
	f.argv[name] = argv
	if f.feed[name] == nil {
		f.feed[name] = make(chan string)
		f.end[name] = make(chan error, 1)
	}
	feed, end, ignore := f.feed[name], f.end[name], f.ignoresCtx
	f.mu.Unlock()

	f.emit("open:" + name)
	defer f.emit("close:" + name)

	for {
		if ignore {
			// The orphaned-ssh stream: it never looks at ctx. It keeps writing, and only
			// a failing Write can end it — which is precisely how the real one behaves,
			// and precisely the leak the writer's ctx-aware Write exists to close.
			select {
			case chunk := <-feed:
				if _, err := out.Write([]byte(chunk)); err != nil {
					return err
				}
			case err := <-end:
				return err
			case <-time.After(time.Millisecond):
				if _, err := out.Write([]byte("noise\n")); err != nil {
					return err
				}
			}
			continue
		}
		select {
		case chunk := <-feed:
			if _, err := out.Write([]byte(chunk)); err != nil {
				return err
			}
		case err := <-end:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// await blocks until the NEXT occurrence of an event ("open:web", "close:web") the
// test has not already awaited.
func (f *fakeShell) await(t *testing.T, event string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		f.mu.Lock()
		if f.events[event] > f.taken[event] {
			f.taken[event]++
			f.mu.Unlock()
			return
		}
		f.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q", event)
		}
		time.Sleep(time.Millisecond)
	}
}

// write feeds one chunk into a VM's stream.
func (f *fakeShell) write(t *testing.T, name, chunk string) {
	t.Helper()
	f.mu.Lock()
	ch := f.feed[name]
	f.mu.Unlock()
	if ch == nil {
		t.Fatalf("%s has no open stream to write to", name)
	}
	select {
	case ch <- chunk:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: nobody read the chunk", name)
	}
}

// die ends a VM's stream with err, standing in for the VM being stopped underneath
// it. On a real VM that is `exit status 255` about 300ms after `limactl stop`.
func (f *fakeShell) die(t *testing.T, name string, err error) {
	t.Helper()
	f.mu.Lock()
	ch := f.end[name]
	f.mu.Unlock()
	if ch == nil {
		t.Fatalf("%s has no open stream to kill", name)
	}
	ch <- err
}

func (f *fakeShell) opened(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens[name]
}

func (f *fakeShell) argvFor(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.argv[name]
}

// record builds one guest record: an aggregate /proc/stat cpu line, the two
// /proc/meminfo lines that matter, and the delimiter — the same shape guestScript
// makes the guest emit.
func record(user, idle, memTotalKB, memAvailKB int) string {
	f := strconv.Itoa
	return "cpu  " + f(user) + " 0 0 " + f(idle) + " 0 0 0 0 0 0\n" +
		"cpu0 1 2 3 4\n" +
		"MemTotal:       " + f(memTotalKB) + " kB\n" +
		"MemFree:        " + f(memAvailKB/2) + " kB\n" +
		"MemAvailable:   " + f(memAvailKB) + " kB\n" +
		heartbeatDelim + "\n"
}

// heartbeatModel is a model wired to a fake guest shell instead of a real limactl.
func heartbeatModel(t *testing.T, sh *fakeShell) model {
	t.Helper()
	m := newTestModel(t)
	m.heartbeats = newHeartbeats(sh)
	return m
}

// running/stopped shorthand for a vmsLoadedMsg.
func vms(pairs ...string) []vm.VM {
	out := make([]vm.VM, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, vm.VM{Name: pairs[i], Status: pairs[i+1]})
	}
	return out
}

// A STOPPED VM GETS NO HEARTBEAT AND THEREFORE NO SAMPLE. This is the honesty
// requirement in its purest form: the tile renderer's job (task 07) is to draw the
// ABSENCE of a reading, and this test is what guarantees there is genuinely nothing
// to draw — not a zero, not a stale value, nothing.
func TestStoppedVMGetsNoHeartbeatAndNoSample(t *testing.T) {
	sh := newFakeShell()
	l := newTeaLoop(t, heartbeatModel(t, sh))

	l.send(vmsLoadedMsg{vms: vms("up", "Running", "down", "Stopped")})
	sh.await(t, "open:up")

	if got := sh.opened("down"); got != 0 {
		t.Fatalf("a stopped VM must never have a shell opened into it, got %d", got)
	}
	if _, ok := l.m.sampleOf("down"); ok {
		t.Fatal("a stopped VM must have NO sample — a zero here is a fabricated reading")
	}
	// And the running one has no sample either until its first record lands: a
	// heartbeat that is merely OPEN knows nothing yet.
	if _, ok := l.m.sampleOf("up"); ok {
		t.Fatal("no sample may exist before the first record arrives")
	}

	// ONE shell per VM, not one per sample tick. Three records, still one shell.
	sh.write(t, "up", record(100, 900, 1000, 600))
	l.pump("up's first sample", func(m model) bool { _, ok := m.sampleOf("up"); return ok })

	// THE FIRST SAMPLE CARRIES NO CPU. /proc/stat is cumulative; one reading is not a
	// rate. Memory, being absolute, is there immediately.
	first, _ := l.m.sampleOf("up")
	if first.HasCPU {
		t.Fatalf("the first sample must carry no cpu reading, got %.1f%%", first.CPUPct)
	}
	if !first.HasMem() || first.MemUsed != 400*1024 {
		t.Fatalf("the first sample should carry memory immediately, got %+v", first)
	}

	sh.write(t, "up", record(200, 1700, 1000, 600))
	l.pump("up's second sample", func(m model) bool { s, ok := m.sampleOf("up"); return ok && s.HasCPU })

	second, _ := l.m.sampleOf("up")
	// Δtotal = 900, Δidle = 800, so busy = 100/900 = 11.1%.
	if got := second.CPUPct; got < 11.0 || got > 11.2 {
		t.Fatalf("cpu = %.2f%%, want ~11.11%% (Δbusy/Δtotal across the two records)", got)
	}
	if n := sh.opened("up"); n != 1 {
		t.Fatalf("opened %d shells for one VM — it must be ONE long-lived shell, not one per sample (each spawn costs 150-400ms and a fresh SSH connection)", n)
	}
	// And it is the dumb loop, run through `sh -c`, exactly as verified against a
	// real VM.
	if argv := sh.argvFor("up"); len(argv) != 3 || argv[0] != "sh" || argv[1] != "-c" || !strings.Contains(argv[2], "/proc/stat") {
		t.Fatalf("argv = %q, want the in-guest /proc loop under `sh -c`", argv)
	}
}

// A heartbeat starts when a VM enters Running and stops when it leaves — driven off
// the ordinary vmsLoadedMsg refresh, with no new polling machinery.
func TestHeartbeatFollowsTheRunningTransition(t *testing.T) {
	sh := newFakeShell()
	l := newTeaLoop(t, heartbeatModel(t, sh))

	// Stopped: nothing.
	l.send(vmsLoadedMsg{vms: vms("web", "Stopped")})
	if n := len(l.m.heartbeats.names()); n != 0 {
		t.Fatalf("a stopped VM has %d heartbeats, want 0", n)
	}

	// It starts.
	l.send(vmsLoadedMsg{vms: vms("web", "Running")})
	sh.await(t, "open:web")
	sh.write(t, "web", record(100, 900, 1000, 600))
	l.pump("web's sample", func(m model) bool { _, ok := m.sampleOf("web"); return ok })

	// It stops. The heartbeat must go, and THE GAUGE MUST GO WITH IT: a reading left
	// behind would render as a stopped VM still burning cpu.
	l.send(vmsLoadedMsg{vms: vms("web", "Stopped")})
	sh.await(t, "close:web")

	if n := len(l.m.heartbeats.names()); n != 0 {
		t.Fatalf("a VM that left Running still has %d heartbeats", n)
	}
	if s, ok := l.m.sampleOf("web"); ok {
		t.Fatalf("a stopped VM's gauge is stuck at its last reading: %+v", s)
	}

	// And it comes back when the VM does. A deliberate stop sets no cooldown, so
	// this is immediate.
	l.send(vmsLoadedMsg{vms: vms("web", "Running")})
	sh.await(t, "open:web")
	if n := sh.opened("web"); n != 2 {
		t.Fatalf("web's shell was opened %d times, want 2 (once per Running spell)", n)
	}
}

// THE IDLE GATE, which is a hard requirement and not a polish item. Every heartbeat
// is an open SSH connection into a guest; sand in a backgrounded terminal, over SSH,
// on battery, must not quietly hold them all open for nobody.
func TestHeartbeatIsIdleGated(t *testing.T) {
	sh := newFakeShell()
	l := newTeaLoop(t, heartbeatModel(t, sh))

	l.send(vmsLoadedMsg{vms: vms("web", "Running")})
	sh.await(t, "open:web")

	// 1. THE BOARD IS NOT THE ACTIVE SCREEN. `n` opens the create form; the gauges
	//    behind it are not being looked at, so their connections close.
	l.send(runeKey('n'))
	if l.m.view != viewForm {
		t.Fatalf("expected the create form, got view %v", l.m.view)
	}
	sh.await(t, "close:web")
	if n := len(l.m.heartbeats.names()); n != 0 {
		t.Fatalf("%d heartbeats still open behind another screen", n)
	}
	if _, ok := l.m.sampleOf("web"); ok {
		t.Fatal("a paused heartbeat must leave no sample behind to go stale")
	}

	// Back to the board: they reopen.
	l.send(tea.KeyPressMsg{Code: tea.KeyEsc})
	if l.m.view != viewBoard {
		t.Fatalf("esc should return to the board, got view %v", l.m.view)
	}
	sh.await(t, "open:web")

	// 2. THE TERMINAL LOST FOCUS — it was backgrounded.
	l.send(tea.BlurMsg{})
	sh.await(t, "close:web")
	if l.m.shouldTick() {
		t.Fatal("a blurred terminal must close the gate")
	}

	l.send(tea.FocusMsg{})
	sh.await(t, "open:web")

	// 3. NOBODY IS THERE. Focus alone does not survive the user walking away from a
	//    foregrounded terminal, so stale input closes everything down too.
	l.m.lastInput = time.Now().Add(-2 * heartbeatIdleAfter)
	if l.m.shouldTick() {
		t.Fatal("an idle session must close the gate")
	}
	l.send(vmsLoadedMsg{vms: vms("web", "Running")}) // any message re-evaluates it
	sh.await(t, "close:web")

	// And ANY KEY wakes it: no timer runs while sand is idle — that is what idle
	// means — so the keypress that says "I'm back" is the thing that reopens it.
	l.send(runeKey('j'))
	sh.await(t, "open:web")
	if !l.m.shouldTick() {
		t.Fatal("a keypress must reopen the gate")
	}
}

// THE VM IS STOPPED UNDERNEATH THE STREAM. Observed on a real VM: `limactl stop`
// kills the shell within ~300ms and it returns `exit status 255`. The heartbeat must
// end itself on that — dropping the reading at once rather than freezing the gauge
// until some later refresh notices — and its goroutine must be reaped.
func TestHeartbeatDiesCleanlyWhenTheVMStopsUnderneathIt(t *testing.T) {
	sh := newFakeShell()
	l := newTeaLoop(t, heartbeatModel(t, sh))

	l.send(vmsLoadedMsg{vms: vms("web", "Running")})
	sh.await(t, "open:web")
	sh.write(t, "web", record(100, 900, 1000, 600))
	sh.write(t, "web", record(200, 1700, 1000, 600))
	l.pump("web's cpu reading", func(m model) bool { s, ok := m.sampleOf("web"); return ok && s.HasCPU })

	// The VM is stopped from outside sand. Lima has not told us yet — m.vms still
	// says Running — but the shell is already dead.
	sh.die(t, "web", errors.New("exit status 255"))
	sh.await(t, "close:web")

	l.pump("the heartbeat to end", func(m model) bool { return len(m.heartbeats.names()) == 0 })

	// NO STUCK GAUGE. The reading goes the moment the stream does, without waiting
	// for a list refresh to come round and notice.
	if s, ok := l.m.sampleOf("web"); ok {
		t.Fatalf("the gauge is stuck at the reading the VM died on: %+v", s)
	}

	// And sand does not now throw a fresh `limactl shell` at a VM it cannot reach on
	// every single refresh: a heartbeat that died on its own earns a cooldown.
	for i := 0; i < 5; i++ {
		l.send(vmsLoadedMsg{vms: vms("web", "Running")})
	}
	if n := sh.opened("web"); n != 1 {
		t.Fatalf("a VM whose shell keeps dying had %d opened at it across 5 refreshes — a doomed VM must not be retried on every tick", n)
	}

	// Once the cooldown lapses it does try again: this is a pause, not a blacklist.
	l.m.heartbeats.mu.Lock()
	l.m.heartbeats.cooldown["web"] = time.Now().Add(-time.Second)
	l.m.heartbeats.mu.Unlock()
	l.send(vmsLoadedMsg{vms: vms("web", "Running")})
	sh.await(t, "open:web")
}

// A LEAKED GOROUTINE IS A FAILURE, NOT A COSMETIC ISSUE — each one holds an SSH
// connection into a guest. The stream here IGNORES its context, exactly as the real
// orphaned ssh child does, so nothing but a failing Write can stop it. If the
// sampler's writer did not fail on a done context, this would hang forever and the
// count would never come back down.
func TestHeartbeatsLeaveNoGoroutinesBehind(t *testing.T) {
	sh := newFakeShell()
	sh.ignoresCtx = true

	r := newHeartbeats(sh)
	base := runtime.NumGoroutine()

	// Three VMs, each with a live stream and a reader draining it exactly as
	// heartbeatReadCmd does.
	var readers sync.WaitGroup
	for _, name := range []string{"a", "b", "c"} {
		epoch, ch, ok := r.start(name)
		if !ok {
			t.Fatalf("%s: start failed", name)
		}
		readers.Add(1)
		go func(name string, epoch uint64, ch <-chan guestSample) {
			defer readers.Done()
			// The read loop, as Update drives it: read, fold, read again — until the
			// sampler closes the channel. A sampler that never closes parks this
			// goroutine forever.
			for {
				s, open := <-ch
				if !open {
					return
				}
				if next := r.fold(name, epoch, s); next == nil {
					return
				}
			}
		}(name, epoch, ch)
		sh.await(t, "open:"+name)
	}

	sh.write(t, "a", record(100, 900, 1000, 600))
	sh.write(t, "a", record(200, 1700, 1000, 600))
	// The reader folds the sample on its own goroutine, so wait for it rather than
	// racing it.
	waitFor(t, "a's cpu reading", func() bool {
		s, ok := r.latest("a")
		return ok && s.HasCPU
	})

	// The gate slams shut: the user backgrounded the terminal.
	r.stopAll()

	for _, name := range []string{"a", "b", "c"} {
		sh.await(t, "close:"+name)
	}
	readers.Wait()

	// Every reading is gone with its heartbeat...
	if n := len(r.names()); n != 0 {
		t.Fatalf("%d heartbeats survived stopAll", n)
	}
	if _, ok := r.latest("a"); ok {
		t.Fatal("a stopped heartbeat must leave no reading behind")
	}
	// ...and so is every goroutine.
	waitForGoroutines(t, base)
}

// waitFor polls until want holds, failing the test if it never does.
func waitFor(t *testing.T, what string, want func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !want() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitForGoroutines fails unless the goroutine count settles back to at most base.
// Goroutines are torn down asynchronously, so it polls rather than sampling once.
func waitForGoroutines(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		runtime.Gosched()
		got := runtime.NumGoroutine()
		if got <= base {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("LEAKED GOROUTINES: %d now, %d at the start. Each one holds an SSH connection into a guest.\n\n%s", got, base, buf)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A sample from a connection that has already been replaced must be DROPPED, and its
// read loop must END. Record it against the new connection and the gauge shows a
// dead VM's numbers; keep reading and the two loops attach to the same channel and
// multiply.
func TestStaleSampleIsDroppedAndItsReadLoopEnds(t *testing.T) {
	sh := newFakeShell()
	r := newHeartbeats(sh)

	old, _, ok := r.start("web")
	if !ok {
		t.Fatal("start")
	}
	sh.await(t, "open:web")
	r.stop("web") // the user left the board
	sh.await(t, "close:web")

	fresh, _, ok := r.start("web") // and came straight back
	if !ok {
		t.Fatal("restart")
	}
	sh.await(t, "open:web")
	if fresh == old {
		t.Fatal("a new connection must get a new epoch, or a stale sample cannot be told from a live one")
	}

	if next := r.fold("web", old, guestSample{MemTotal: 99, MemUsed: 99}); next != nil {
		t.Fatal("a stale sample's read loop must end, not attach itself to the live connection")
	}
	if s, ok := r.latest("web"); ok {
		t.Fatalf("a dead connection's sample was recorded against the live one: %+v", s)
	}
	if next := r.fold("web", fresh, guestSample{MemTotal: 1, MemUsed: 1}); next == nil {
		t.Fatal("the live connection must keep reading")
	}
	if _, ok := r.latest("web"); !ok {
		t.Fatal("the live connection's sample should be recorded")
	}

	r.stopAll()
	sh.await(t, "close:web")
}
