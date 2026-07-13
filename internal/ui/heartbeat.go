package ui

// heartbeat.go puts REAL utilization on the board, and is mostly an exercise in
// refusing to make one up.
//
// # Why this exists
//
// vm.VM carries CPUs and Memory. Those are ALLOCATIONS — what Lima was told to
// give the guest — and drawing an allocation as a utilization bar would imply
// telemetry sand does not have. A tile that says "4 CPUs" beside a bar filled to
// 4/4 is not a gauge; it is a lie with a progress bar around it. The only honest
// source of a utilization number is the guest itself.
//
// # The shape
//
// ONE long-lived `limactl shell` per RUNNING VM, running a loop that cats
// /proc/stat and /proc/meminfo every heartbeatInterval and prints a delimiter. The
// host parses the stream. The guest side is deliberately the dumbest thing that
// works: a clever guest script is a thing that breaks on a distro nobody tested.
//
// It is one shell per VM, NOT one per sample tick: each `limactl shell` costs
// 150–400ms and a fresh SSH connection, so a per-tick spawn would cost more than
// the numbers are worth. The price is one SSH connection and one goroutine per
// running VM, which at this tool's scale (1–3 VMs typically, ~10 for a power user)
// is nothing. It would not be nothing at 100 VMs. sand does not have 100 VMs.
//
// # Two things learned from a real VM rather than assumed
//
//  1. `limactl shell` FORKS an ssh client that inherits the pipes. Cancelling the
//     context kills limactl and ORPHANS the ssh, which keeps running, keeps the
//     guest loop alive, and holds the pipes open forever. See lima.waitDelay: the
//     fix lives in the runner, because the bug was in the runner.
//  2. When the VM is stopped underneath the stream, the shell dies on its own
//     within ~300ms with `exit status 255`. So a stopped VM's heartbeat ends itself;
//     it does not need to be noticed and killed. The gauge disappears at once,
//     rather than freezing at its last value until the next list refresh.
//
// Quitting sand needs no teardown of its own, which was also checked rather than
// assumed: the process exiting closes the read ends of these pipes, the orphaned ssh
// takes a SIGPIPE on its next write (so, within one interval), and the guest loop
// dies with the session. Nothing is left running.
//
// # The concurrency contract (the same one jobs.go established)
//
// Bubble Tea passes the model BY VALUE, so mutable state that outlives one Update
// cannot live on it. The registry is a POINTER field and guards everything with a
// mutex, so every model copy shares one registry. Samples travel from the sampler
// goroutine into Update as a message — not by writing the model — and it is Update,
// under the lock, that records them. Readers (the tile renderer, task 07) call
// latest() and get a value copy.

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

// limaRunning is the status Lima reports for a booted instance — the same literal
// deriveStatus tests against, named once here because syncHeartbeats now asks the
// same question for a different reason (is there a guest to open a shell into).
const limaRunning = "Running"

const (
	// heartbeatInterval is how often the guest emits a record. Two seconds reads as
	// live without making the stream (about 1.4 KB/s per VM) worth thinking about.
	heartbeatInterval = 2 * time.Second

	// heartbeatDelim ends one record. Nothing in /proc/stat or /proc/meminfo can
	// collide with it.
	heartbeatDelim = "---sand-heartbeat---"

	// heartbeatRetry is how long a VM waits before sand re-opens a heartbeat that
	// DIED ON ITS OWN. Without it, a VM that Lima calls Running but that cannot be
	// shelled into — one still booting, one whose sshd is wedged — would have a
	// fresh `limactl shell` thrown at it on every single refresh. A deliberate stop
	// (the user left the board, the VM was stopped) sets no cooldown, so coming back
	// to the board is instant.
	heartbeatRetry = 5 * time.Second

	// heartbeatIdleAfter is how long sand may sit with no input from the user before
	// it decides nobody is watching and drops the connections. See shouldTick.
	heartbeatIdleAfter = 5 * time.Minute

	// carryLimit caps the partial line the parser holds across reads, so a guest
	// that streams megabytes without a newline cannot grow the buffer without bound.
	carryLimit = 64 << 10
)

// guestScript is the in-guest loop: cat the two files, print a delimiter, sleep.
// That is the whole of it, on purpose. Everything clever happens on the host, where
// it can be tested. `limactl shell` escapes this correctly on its way through ssh
// into the guest's login shell — verified against a real VM, not assumed.
func guestScript(every time.Duration) string {
	secs := strconv.Itoa(int(every / time.Second))
	return "while true; do cat /proc/stat /proc/meminfo; echo '" + heartbeatDelim + "'; sleep " + secs + "; done"
}

// guestSample is ONE utilization reading from inside a running guest, and it is the
// type the tile renderer (task 07) draws its gauges from.
//
// Every "Has" here earns its keep. A tile must be able to tell "this VM is idle"
// from "sand does not know yet", because they look identical if the second one is
// rendered as a zero — and a zero is exactly what a naive struct would give it.
type guestSample struct {
	// CPUPct is the guest's busy share of all its vCPUs, 0–100. It is only
	// meaningful when HasCPU is true.
	CPUPct float64

	// HasCPU is FALSE for the first sample of every connection, and it is the whole
	// reason this field exists. /proc/stat's counters are cumulative since boot, so
	// a single reading is a total, not a rate: a percentage needs two. Until the
	// second record arrives (one heartbeatInterval later), the honest answer is "no
	// reading" — render nothing, NOT zero.
	HasCPU bool

	// MemUsed and MemTotal are bytes. used = MemTotal - MemAvailable.
	//
	// MemAvailable, never MemFree. MemFree excludes the page cache, which Linux
	// fills with everything it has ever read; a freshly-booted guest that has done
	// nothing but boot reports most of its RAM "not free" and would render as a VM
	// on the edge of OOM. On the real guest this parser was built against, MemFree
	// said 316 MB while MemAvailable said 1637 MB of 2015 MB.
	MemUsed  uint64
	MemTotal uint64

	// At is when the host parsed the record. A tile can use it to grey out a reading
	// that has gone stale.
	At time.Time
}

// HasMem reports whether the sample carries a memory reading. Unlike cpu, memory is
// an absolute — it is valid from the very first record.
func (s guestSample) HasMem() bool { return s.MemTotal > 0 }

// cpuTimes is one /proc/stat aggregate reading, in jiffies since boot.
type cpuTimes struct {
	total uint64 // user+nice+system+idle+iowait+irq+softirq+steal
	idle  uint64 // idle+iowait: time the guest had nothing to do
}

// sampleParser turns the guest's byte stream into samples. It is a pure function of
// the bytes fed to it plus the previous record's cpu counters — no clock, no I/O —
// which is what lets it be tested against real captured /proc text.
//
// It is fed whatever the pipe hands over, which is NOT records and NOT even lines:
// a real `limactl shell` delivered 2642-byte chunks that sometimes arrived as
// 2631 + 11, tearing a record (and a line) in half. Hence the carry.
type sampleParser struct {
	carry   []byte   // the tail of the last chunk, up to the next newline
	cur     cpuTimes // the record being accumulated
	haveCPU bool     // the current record has had its `cpu ` line
	mem     struct {
		total, avail uint64
		haveTotal    bool
		haveAvail    bool
	}
	prev     cpuTimes // the PREVIOUS record's counters — the other half of every delta
	havePrev bool
}

// feed folds one chunk of the stream in and returns every sample completed by it —
// usually none or one, but a slow reader can hand over several records at once.
func (p *sampleParser) feed(chunk []byte) []guestSample {
	var out []guestSample

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
		if s, ok := p.line(buf[:i]); ok {
			out = append(out, s)
		}
		buf = buf[i+1:]
	}

	// Whatever is left is a partial line; hold it for the next read. A guest that
	// never sends a newline must not be able to grow this without bound.
	if len(buf) > carryLimit {
		buf = nil
	}
	// COPY it. buf may still alias the caller's chunk, and os/exec hands the same
	// backing array to every Write — so retaining a slice of it would leave the
	// carry silently rewritten by the next read, and the torn line would reassemble
	// into garbage.
	p.carry = append([]byte(nil), buf...)
	return out
}

// line folds one complete line in, and completes a sample on the delimiter.
// Anything it does not recognise is IGNORED, not an error: `limactl shell` runs the
// command through a login shell, so a motd or a profile's warning can land in the
// stream, and neither may corrupt a reading.
func (p *sampleParser) line(line []byte) (guestSample, bool) {
	line = bytes.TrimRight(line, "\r")

	switch {
	case bytes.Equal(bytes.TrimSpace(line), []byte(heartbeatDelim)):
		return p.complete()

	// The AGGREGATE cpu line, and only it: `cpu ` with a space. The per-core `cpu0`,
	// `cpu1` … lines must not be mistaken for it.
	case bytes.HasPrefix(line, []byte("cpu ")):
		if t, ok := parseCPU(line[4:]); ok {
			p.cur, p.haveCPU = t, true
		}

	case bytes.HasPrefix(line, []byte("MemTotal:")):
		if kb, ok := parseKB(line[len("MemTotal:"):]); ok {
			p.mem.total, p.mem.haveTotal = kb, true
		}

	case bytes.HasPrefix(line, []byte("MemAvailable:")):
		if kb, ok := parseKB(line[len("MemAvailable:"):]); ok {
			p.mem.avail, p.mem.haveAvail = kb, true
		}
	}
	return guestSample{}, false
}

// complete turns the accumulated record into a sample and re-arms for the next one.
func (p *sampleParser) complete() (guestSample, bool) {
	var s guestSample

	if p.haveCPU {
		// THE DELTA, which is the only thing /proc/stat can honestly give.
		//
		// Guard the subtraction rather than trusting it: if the guest rebooted under
		// the stream the counters restart from near zero, and unsigned arithmetic
		// would turn that into a percentage in the billions. A backwards counter
		// yields NO reading, and re-baselines so the next record reads again.
		if p.havePrev && p.cur.total >= p.prev.total && p.cur.idle >= p.prev.idle {
			dTotal := p.cur.total - p.prev.total
			dIdle := p.cur.idle - p.prev.idle
			if dTotal > 0 && dIdle <= dTotal {
				s.CPUPct = float64(dTotal-dIdle) / float64(dTotal) * 100
				s.HasCPU = true
			}
		}
		p.prev, p.havePrev = p.cur, true
	}

	if p.mem.haveTotal && p.mem.haveAvail && p.mem.total > 0 {
		s.MemTotal = p.mem.total * 1024
		avail := min(p.mem.avail, p.mem.total)
		s.MemUsed = (p.mem.total - avail) * 1024
	}

	// A record with neither reading (a delimiter and nothing else — a guest with no
	// /proc, say) is not a sample. Reporting it would put an empty tile's gauges
	// through a pointless repaint and, worse, would look like a successful reading.
	if !s.HasCPU && !s.HasMem() {
		p.reset()
		return guestSample{}, false
	}

	p.reset()
	return s, true
}

// reset clears the per-record accumulators. prev/havePrev deliberately SURVIVE:
// they are the other half of the next delta.
func (p *sampleParser) reset() {
	p.haveCPU = false
	p.cur = cpuTimes{}
	p.mem.total, p.mem.avail = 0, 0
	p.mem.haveTotal, p.mem.haveAvail = false, false
}

// parseCPU reads the fields after `cpu `: user nice system idle iowait irq softirq
// steal [guest guest_nice].
//
// It sums the first EIGHT only. guest and guest_nice are already counted inside
// user and nice — the kernel double-books them — so adding them again inflates the
// total and quietly deflates every percentage, on exactly the machines (a guest
// running its own VMs) where the number matters most.
func parseCPU(rest []byte) (cpuTimes, bool) {
	var t cpuTimes
	var n int
	for _, f := range bytes.Fields(rest) {
		if n == 8 {
			break
		}
		v, err := strconv.ParseUint(string(f), 10, 64)
		if err != nil {
			return cpuTimes{}, false
		}
		t.total += v
		if n == 3 || n == 4 { // idle, iowait
			t.idle += v
		}
		n++
	}
	// Old kernels emit as few as four fields (user nice system idle); anything
	// shorter is not a cpu line we can use.
	if n < 4 {
		return cpuTimes{}, false
	}
	return t, true
}

// parseKB reads a /proc/meminfo value: `        2015496 kB`.
func parseKB(rest []byte) (uint64, bool) {
	f := bytes.Fields(rest)
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseUint(string(f[0]), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// sampleWriter is the io.Writer the streaming shell writes into: it parses inline,
// so the whole heartbeat is ONE goroutine per VM (the one blocked in
// ShellStreamOut) rather than a producer plus a reader.
//
// Its Write must never block forever, and that is not a nicety. os/exec copies the
// child's stdout into this writer on a goroutine that cmd.Wait() joins, so a Write
// that parks on a channel nobody is draining parks cmd.Wait() with it — and the
// heartbeat can no longer be torn down at all. Hence the select on ctx.Done, and
// hence the error return: an erroring Write also makes os/exec close the pipe,
// which is what hands the orphaned ssh its SIGPIPE.
type sampleWriter struct {
	ctx context.Context
	out chan<- guestSample
	p   sampleParser
}

func (w *sampleWriter) Write(b []byte) (int, error) {
	for _, s := range w.p.feed(b) {
		s.At = time.Now()
		select {
		case w.out <- s:
		case <-w.ctx.Done():
			return 0, w.ctx.Err()
		}
	}
	// Even a chunk that completes no record must fail once the heartbeat is done, so
	// a cancelled stream tears down at the next byte rather than the next record.
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return len(b), nil
}

// guestShell is the one thing the heartbeat needs from lima.Client. Naming it here
// (rather than taking a *lima.Client) is what lets the lifecycle tests drive a
// stream they control — one that blocks, or dies, or ignores its context — without
// a VM, a limactl, or a subprocess.
type guestShell interface {
	ShellStreamOut(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error
}

// heartbeat is one VM's live connection. Like a job, it lives only inside the
// registry, always behind a pointer, and is never handed out.
type heartbeat struct {
	// epoch identifies THIS connection, and it is what makes a stale message
	// harmless. A heartbeat can be stopped and restarted for the same VM (the user
	// leaves the board and comes back), and a sample already in flight from the old
	// connection would otherwise be recorded against the new one — or, worse, would
	// start a SECOND read loop on it, and they would multiply.
	epoch  uint64
	cancel context.CancelFunc
	ch     chan guestSample

	last guestSample
	seen bool
}

// heartbeatRegistry owns every live heartbeat. A nil *heartbeatRegistry is safe to
// call every method on and reports "no heartbeats", so a model built by hand needs
// none.
type heartbeatRegistry struct {
	mu    sync.Mutex
	beats map[string]*heartbeat

	// cooldown holds VMs whose heartbeat DIED ON ITS OWN, and until when they may
	// not be retried. See heartbeatRetry.
	cooldown map[string]time.Time

	nextEpoch uint64

	// shell is the seam onto lima.Client; interval and retry are settable so the
	// lifecycle tests need not sleep for real seconds.
	shell    guestShell
	interval time.Duration
	retry    time.Duration
}

func newHeartbeats(shell guestShell) *heartbeatRegistry {
	return &heartbeatRegistry{
		beats:    make(map[string]*heartbeat),
		cooldown: make(map[string]time.Time),
		shell:    shell,
		interval: heartbeatInterval,
		retry:    heartbeatRetry,
	}
}

// start opens a heartbeat for name and spawns its sampler. It reports false — and
// starts nothing — when one is already open, when the VM is in its post-failure
// cooldown, or when there is no shell to open (a hand-built model).
func (r *heartbeatRegistry) start(name string) (uint64, <-chan guestSample, bool) {
	if r == nil || r.shell == nil {
		return 0, nil, false
	}
	r.mu.Lock()
	if _, ok := r.beats[name]; ok {
		r.mu.Unlock()
		return 0, nil, false
	}
	if until, ok := r.cooldown[name]; ok && time.Now().Before(until) {
		r.mu.Unlock()
		return 0, nil, false
	}
	delete(r.cooldown, name)

	r.nextEpoch++
	epoch := r.nextEpoch
	ctx, cancel := context.WithCancel(context.Background())
	// Buffered by one so the sampler can hand off a sample and get straight back to
	// reading the stream, without waiting for Update to come round.
	ch := make(chan guestSample, 1)
	r.beats[name] = &heartbeat{epoch: epoch, cancel: cancel, ch: ch}
	shell, interval := r.shell, r.interval
	r.mu.Unlock()

	go func() {
		// The SENDER closes, always — including on every error path. It is what wakes
		// the blocked read command, which is what tells Update the heartbeat is over.
		// Skip it and that command's goroutine is parked on this channel forever.
		defer close(ch)
		w := &sampleWriter{ctx: ctx, out: ch}
		// The error is deliberately dropped. Every way this returns — the VM stopped
		// (`exit status 255`), the shell was cancelled (`signal: killed`), the guest
		// has no /proc — means the same thing to the board: this VM has no reading any
		// more. Surfacing it on the status line would spam the user with a message
		// about a subsystem they never asked for, every time they stopped a VM.
		_ = shell.ShellStreamOut(ctx, name, nil, w, "sh", "-c", guestScript(interval))
	}()

	return epoch, ch, true
}

// stop ends name's heartbeat DELIBERATELY: because the VM left Running, or because
// the board is no longer the screen the user is on. The entry goes at once, so the
// gauge disappears now rather than freezing at its last value.
//
// No cooldown is recorded: this heartbeat did not fail, so returning to the board
// must bring it straight back.
func (r *heartbeatRegistry) stop(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	hb, ok := r.beats[name]
	if ok {
		delete(r.beats, name)
	}
	r.mu.Unlock()

	// Outside the lock: cancelling runs arbitrary teardown (it kills a subprocess),
	// and nothing here needs to hold the registry while it does.
	if ok {
		hb.cancel()
	}
}

// stopAll ends every heartbeat. This is the idle gate slamming shut — the user
// backgrounded the terminal, or walked away — and it is the whole reason sand can
// be left open over SSH without holding N connections into N guests.
func (r *heartbeatRegistry) stopAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	stopping := make([]*heartbeat, 0, len(r.beats))
	for name, hb := range r.beats {
		stopping = append(stopping, hb)
		delete(r.beats, name)
	}
	r.mu.Unlock()

	for _, hb := range stopping {
		hb.cancel()
	}
}

// fold records a sample and hands back the channel to read the next one from. A
// stale epoch — a sample from a connection that has since been stopped, and perhaps
// replaced — is DROPPED, and the nil channel ends its read loop instead of letting
// it double up on the live one.
func (r *heartbeatRegistry) fold(name string, epoch uint64, s guestSample) <-chan guestSample {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	hb, ok := r.beats[name]
	if !ok || hb.epoch != epoch {
		return nil
	}
	hb.last, hb.seen = s, true
	return hb.ch
}

// ended is what the closed channel means: the stream finished on its own. Against a
// real VM that is what a `limactl stop` looks like from here — the shell dies within
// ~300ms with `exit status 255` — so this is the ordinary path for a VM being
// stopped, not an exceptional one.
//
// The entry is dropped, which is what stops a gauge freezing at the value it held
// when the VM went down, and a cooldown is set, which is what stops a VM that Lima
// calls Running but cannot be shelled into from drawing a fresh `limactl shell` on
// every refresh forever.
//
// A stale epoch means the heartbeat was ALREADY stopped deliberately and this is
// just its goroutine finishing: nothing to drop, and nothing to cool down.
func (r *heartbeatRegistry) ended(name string, epoch uint64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if hb, ok := r.beats[name]; ok && hb.epoch == epoch {
		delete(r.beats, name)
		r.cooldown[name] = time.Now().Add(r.retry)
	}
}

// latest is the reader's entry point — the one the tile renderer (task 07) calls.
// It returns a value copy, safe to render from any goroutine, and reports false when
// there is NOTHING TO SHOW: no heartbeat (the VM is stopped, or the board is not on
// screen) or one that has not produced its first record yet.
//
// False is not zero. A tile handed false must render the ABSENCE of a reading; a
// tile that renders it as 0% has invented a fact.
func (r *heartbeatRegistry) latest(name string) (guestSample, bool) {
	if r == nil {
		return guestSample{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	hb, ok := r.beats[name]
	if !ok || !hb.seen {
		return guestSample{}, false
	}
	return hb.last, true
}

// names lists the VMs with a heartbeat open, in no order.
func (r *heartbeatRegistry) names() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.beats))
	for name := range r.beats {
		out = append(out, name)
	}
	return out
}

// forget drops a VM's cooldown so it is not remembered after the VM itself is gone.
func (r *heartbeatRegistry) forget(keep map[string]bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.cooldown {
		if !keep[name] {
			delete(r.cooldown, name)
		}
	}
}

// heartbeatSampleMsg carries one reading — or, with ok false, the end of that VM's
// stream — back into Update. Like every job message it is VM-keyed, and it is epoch-
// keyed too, so a reading from a connection that has since been replaced cannot be
// recorded against its successor.
type heartbeatSampleMsg struct {
	vm     string
	epoch  uint64
	sample guestSample
	ok     bool
}

// heartbeatReadCmd waits for one sample. It is the same shape as readNextCmd: the
// blocking receive happens on a tea.Cmd's goroutine, never in Update, and Update
// re-issues it for the next one. The receive is what ends this goroutine when the
// sampler closes the channel — which is why the sampler closes it on EVERY path.
func heartbeatReadCmd(name string, epoch uint64, ch <-chan guestSample) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		s, ok := <-ch
		return heartbeatSampleMsg{vm: name, epoch: epoch, sample: s, ok: ok}
	}
}

// shouldTick IS THE IDLE GATE: the single predicate deciding whether sand's
// recurring background work may run at all. Today that is the guest heartbeat.
// Task 09's poller wants exactly this predicate — it is deliberately named for the
// general question, not the specific caller.
//
// This is a hard requirement, not a polish item. Every heartbeat is an open SSH
// connection into a guest. sand left running in a backgrounded terminal, over SSH,
// on a laptop on battery, must not quietly hold N of them open and keep N guests
// spinning in a cat/sleep loop for nobody. Three conditions, and all must hold:
//
//   - THE BOARD IS THE SCREEN THE USER IS ON. Gauges nobody can see are not worth an
//     SSH connection. The tile grid (viewBoard, board.go) is the only screen that
//     draws them, so it is the only screen that may hold the connections open.
//   - THE TERMINAL HAS FOCUS. A blurred terminal is a backgrounded one. Terminals
//     that do not report focus never send a BlurMsg, so they simply never trip this
//     — the gate degrades to "on", which is the only safe default: a heartbeat that
//     never runs is a board that never fills in.
//   - SOMEONE IS STILL THERE. Focus alone does not survive the user walking away
//     from a foregrounded terminal, so input older than heartbeatIdleAfter closes
//     everything down. The very next keypress reopens it: any key is a message, every
//     message re-evaluates this gate, and lastInput is fresh again.
func (m model) shouldTick() bool {
	return m.view == viewBoard &&
		m.focused &&
		time.Since(m.lastInput) < heartbeatIdleAfter
}

// syncHeartbeats reconciles the open heartbeats against the VMs that should have
// one, and is called after EVERY message (see Update) — which is what makes the gate
// above continuous rather than something checked at a few remembered places.
//
// A heartbeat is opened for a VM that LIMA says is Running AND that has a tile
// on the board (m.boardVMs(), board.go) — not deriveStatus's Running: a VM
// mid-provision is Building to the board, but its guest is up and shellable,
// and it is the one VM on the board whose cpu is genuinely worth watching.
// What Lima's Running means here is precisely "there is a guest to open a
// shell into", which is the only question this asks.
//
// THE ROSTER CHECK IS THE SECOND HALF OF THE GATE, and it earns its keep the
// same way shouldTick does: task 08 filtered the board to managed clones
// only, so an unmanaged VM Lima reports Running now has NO TILE to show a
// gauge on — and "gauges nobody can see are not worth an SSH connection" is
// exactly shouldTick's own rationale, restated for a VM instead of a screen.
// Without this, every unmanaged Lima instance on the host gets a live shell
// held open into it for nothing: a real resource cost (one SSH connection,
// one goroutine, one guest cat/sleep loop, per invisible VM), not a
// cosmetic one.
//
// A STOPPED VM NEVER GETS ONE, which is how "no gauge" is guaranteed to mean no
// gauge: there is no heartbeat, so there is no sample, so latest() reports false and
// the tile has nothing to draw. That absence is the point. It cannot be faked with a
// zero.
func (m model) syncHeartbeats() tea.Cmd {
	if m.heartbeats == nil {
		return nil
	}
	if !m.shouldTick() {
		m.heartbeats.stopAll()
		return nil
	}

	roster := make(map[string]bool, len(m.vms))
	for _, v := range m.boardVMs() {
		roster[v.Name] = true
	}
	want := make(map[string]bool, len(m.vms))
	for _, v := range m.vms {
		if v.Status == limaRunning && roster[v.Name] {
			want[v.Name] = true
		}
	}

	// Close what should no longer be open: a VM that left Running, or was deleted.
	for _, name := range m.heartbeats.names() {
		if !want[name] {
			m.heartbeats.stop(name)
		}
	}
	// And open what should be. start is a no-op for a VM that already has one, so
	// this is idempotent — which it must be, being called on every message.
	var cmds []tea.Cmd
	for _, v := range m.vms {
		if !want[v.Name] {
			continue
		}
		if epoch, ch, ok := m.heartbeats.start(v.Name); ok {
			cmds = append(cmds, heartbeatReadCmd(v.Name, epoch, ch))
		}
	}
	m.heartbeats.forget(want)
	return tea.Batch(cmds...)
}

// sampleOf is the tile renderer's accessor (task 07): the live reading for a VM, or
// false if there is none to show.
func (m model) sampleOf(name string) (guestSample, bool) {
	return m.heartbeats.latest(name)
}
