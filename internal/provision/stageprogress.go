// stageprogress.go narrates a reset's file copies while they are running.
//
// It exists because staging is the one part of a reset that has no output of its
// own and no bound on how long it takes. Everything else a reset does reports
// itself: limactl and the PVE API print their own lifecycle lines, and the
// finalize playbook feeds the tile's task counter. A `tar` piped over ssh prints
// NOTHING until it is finished — and on a VM whose home is a few gigabytes of
// small files (a node_modules, a Go module cache, a year of agent logs) that is
// minutes of a completely silent screen, in the middle of an operation that has
// already destroyed, or is about to destroy, the only other copy of the user's
// work. Silence there does not read as "working", it reads as "hung", and the
// one thing a user must not do at that moment is kill the process.
//
// The numbers are counted on the HOST, at the end of the pipe sand itself owns:
// the archive file being written on stage-out, the archive file being read on
// stage-in. Nothing is asked of the guest, no second tool has to be installed in
// the image, and the count cannot disagree with reality — it is the bytes that
// actually crossed. It is also, incidentally, the only measurement of how fast
// a reset's transport really is, which is what makes it possible to tell a slow
// compressor from a slow network without guessing.
//
// Only the RESTORE can show a percentage, and that asymmetry is inherent rather
// than an omission. The archive's size is known before a restore starts, so the
// fraction is exact; during a stage-out the total is whatever the guest's tree
// turns out to compress to, and nobody — host or guest — knows it until the tar
// ends. An estimate would have to come from a second full walk of the same tree
// (`du -sb` over hundreds of thousands of files), which is paying a slice of the
// very cost being measured for a number that is still only a guess once the
// compressor gets hold of it. So a stage-out reports what is true — bytes so
// far, and the rate — and never a fabricated percentage.
package provision

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// stageProgressInterval is how often a running transfer reports. It is a var so
// the tests can drive a meter at a speed a test can wait for; nothing in
// production changes it.
//
// Two seconds is chosen against what the line is FOR. It is the only evidence
// that a silent minutes-long copy is alive, so it has to tick faster than a user
// starts to doubt it — and it is also one line in a log the user may read later,
// so it must not tick so fast that the interesting lines are buried. A copy
// short enough to finish inside one interval prints nothing at all (see stop):
// the small archives a reset also stages — ~/.claude.json is a few kilobytes —
// have nothing worth narrating.
var stageProgressInterval = 2 * time.Second

// stageMeter counts the bytes of one archive as they cross the host boundary and
// narrates them to out.
//
// A nil *stageMeter is a working no-op, because both backends' resets are
// legitimately called with a nil out (the buffered, non-streaming forms), and a
// transfer must not have to care which kind of caller it is serving.
type stageMeter struct {
	out io.Writer

	// verbing/verbed are the same action in progress and finished ("Backing up" /
	// "Backed up"), and subject is the short noun for what is moving ("home").
	//
	// Both are kept SHORT because of where this line ends up. The TUI renders a
	// job's latest `==>` banner as the building tile's status row, and a tile is
	// as narrow as 36 columns of content — anything past that is replaced with an
	// ellipsis. A line that leads with prose ("Copying the home directory out of
	// \"web\": 48% …") therefore loses exactly the digits it was written for,
	// while the words that survive say nothing the tile — which is already
	// labelled with the VM's name — was not already showing. So the noun is one
	// or two words, the VM name is not repeated, and the numbers come early.
	verbing string
	verbed  string
	subject string

	// total is the archive's size in bytes for a restore, or 0 when it cannot be
	// known — which is every stage-out (see this file's doc comment).
	total int64

	start time.Time
	now   func() time.Time // injectable clock; nil means time.Now

	// n is written by the transfer's own goroutine (through the metered
	// reader/writer, which runs inline with the copy) and read by the ticker, so
	// it has to be atomic.
	n atomic.Int64

	done chan struct{}
	wg   sync.WaitGroup

	// spoke records that at least one progress line was printed, which is what
	// makes the closing summary conditional: a transfer that finished before the
	// first tick was never announced as running and does not need to be announced
	// as finished.
	spoke bool
}

// newStageMeter announces the transfer and starts reporting on it. The caller
// must call stop exactly once, which is also what stops the goroutine.
func newStageMeter(out io.Writer, verbing, verbed, subject string, total int64) *stageMeter {
	if out == nil {
		return nil
	}
	m := &stageMeter{
		out:     out,
		verbing: verbing,
		verbed:  verbed,
		subject: subject,
		total:   total,
		done:    make(chan struct{}),
	}
	m.start = m.clock()
	// The opening line is printed before a single byte moves, deliberately: on a
	// large tree the first bytes can be tens of seconds away (tar walks before it
	// writes, and the guest may still be waking its disk), so the transfer that
	// has not produced a number yet is precisely the one that most needs to say
	// it has begun.
	//
	// It is the only one of this meter's lines that goes through step, whose
	// leading blank line separates one phase of a reset from the next. The
	// readings that follow are the SAME phase reporting itself over and over — a
	// blank line between each would double a long transfer's share of the log
	// (a five-minute copy is ~150 readings) and push everything else out of
	// view. They stack directly under this banner instead.
	step(m.out, "%s %s…", m.verbing, m.subject)
	m.wg.Add(1)
	go m.loop()
	return m
}

// clock reads the injected time source, defaulting to the real one.
func (m *stageMeter) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// add records bytes that have crossed the boundary.
func (m *stageMeter) add(n int64) {
	if m != nil {
		m.n.Add(n)
	}
}

// loop reports every interval until stop closes done.
func (m *stageMeter) loop() {
	defer m.wg.Done()
	t := time.NewTicker(stageProgressInterval)
	defer t.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-t.C:
			m.spoke = true
			m.say(m.progressLine())
		}
	}
}

// stop ends the reporting and, if the transfer ran long enough to have been
// narrated at all, prints its totals.
//
// It waits for the ticker goroutine before writing, so the meter is never two
// concurrent writers on out — a job's output pipe is read by the TUI's line
// parser, and interleaved partial writes there would corrupt the very banners
// the parser is matching.
func (m *stageMeter) stop() {
	if m == nil {
		return
	}
	close(m.done)
	m.wg.Wait()
	if m.spoke {
		m.say(m.summaryLine())
	}
}

// say writes one reading, carrying the same "==> " prefix the TUI's parser
// matches to lift a line onto the building tile (see internal/ui/ansible.go).
//
// It writes the prefix itself rather than calling step, for the log-density
// reason given in newStageMeter. That is only safe because nothing else writes
// to out while a transfer is running — the guest command's own output goes to
// the archive file or is discarded, and the meter's goroutine is the single
// writer for the whole window — so the stream is always at the start of a line
// here, which is the one thing step would otherwise guarantee.
func (m *stageMeter) say(line string) {
	fmt.Fprintf(m.out, "==> %s\n", line)
}

// progressLine is one in-flight reading: a percentage when the total is known,
// bytes and rate always.
func (m *stageMeter) progressLine() string {
	n := m.n.Load()
	elapsed := m.clock().Sub(m.start)
	if m.total > 0 {
		return fmt.Sprintf("%s %s: %d%% of %s, %s",
			m.verbing, m.subject, percent(n, m.total), humanBytes(m.total), rate(n, elapsed))
	}
	return fmt.Sprintf("%s %s: %s, %s", m.verbing, m.subject, humanBytes(n), rate(n, elapsed))
}

// summaryLine closes out a transfer with what it moved and how fast.
func (m *stageMeter) summaryLine() string {
	n := m.n.Load()
	elapsed := m.clock().Sub(m.start)
	return fmt.Sprintf("%s %s: %s in %s, %s", m.verbed, m.subject, humanBytes(n), humanDuration(elapsed), rate(n, elapsed))
}

// meteredWriter counts what is written through it. Stage-out's archive file is
// wrapped in one, so the count is of bytes that actually reached the host disk
// rather than bytes the guest claims to have sent.
type meteredWriter struct {
	w io.Writer
	m *stageMeter
}

func (mw meteredWriter) Write(p []byte) (int, error) {
	n, err := mw.w.Write(p)
	mw.m.add(int64(n))
	return n, err
}

// meteredReader counts what is read through it. Stage-in's archive file is
// wrapped in one: the guest pulls the archive in over stdin, so a read that has
// happened is a byte the guest has taken, and the count tracks the restore's
// real pace rather than the host's eagerness to hand bytes over.
type meteredReader struct {
	r io.Reader
	m *stageMeter
}

func (mr meteredReader) Read(p []byte) (int, error) {
	n, err := mr.r.Read(p)
	mr.m.add(int64(n))
	return n, err
}

// percent is n as a whole-number percentage of total, clamped to 100 so an
// archive that grew between the stat and the read cannot report 103%.
func percent(n, total int64) int {
	if total <= 0 {
		return 0
	}
	p := int(n * 100 / total)
	if p > 100 {
		return 100
	}
	return p
}

// humanBytes renders a byte count in the units a person reading a transfer
// thinks in. Decimal (1000) rather than binary units, because this is a MOVEMENT
// of bytes — the same thing a network link and a disk are rated in — not an
// allocation of them; the binary spelling lives in internal/ui/humanizeBytes,
// which formats Lima's GiB-denominated sizes and is a different question.
func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	// One decimal below 10 units ("1.4 GB"), none above ("412 MB"): the extra
	// digit is information at the low end and noise at the high end.
	value := float64(n) / float64(div)
	if value < 10 {
		return fmt.Sprintf("%.1f %cB", value, "kMGTPE"[exp])
	}
	return fmt.Sprintf("%.0f %cB", value, "kMGTPE"[exp])
}

// rate is the average throughput over the whole transfer so far. It is an
// average and not an instantaneous reading on purpose: the instantaneous rate of
// a tar over ssh swings wildly between a directory of huge files and a directory
// of tiny ones, and a number that jumps every two seconds invites the reader to
// interpret noise. The average answers the question actually being asked — how
// long is this going to take.
func rate(n int64, elapsed time.Duration) string {
	if elapsed <= 0 {
		return "…"
	}
	perSec := float64(n) / elapsed.Seconds()
	return humanBytes(int64(perSec)) + "/s"
}

// humanDuration renders an elapsed time as whole seconds under a minute and
// m/s above it. A staging copy is measured in seconds and minutes; Go's own
// formatting ("1m24.3271s") carries a precision the reader has no use for.
func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()+0.5))
	}
	total := int(d.Seconds() + 0.5)
	return fmt.Sprintf("%dm%02ds", total/60, total%60)
}
