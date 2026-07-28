package ui

// jobstream.go is the buffer between a run and the screen, and it exists because
// the screen is allowed to stop looking.
//
// # Why an io.Pipe was wrong here
//
// Every streamed run (a build, a file copy — beginStream, progress.go) used to hand
// its provisioner an io.PipeWriter and drain the paired reader from a tea.Cmd. An
// io.Pipe has NO buffer at all: a Write blocks until a Read takes it. That is a
// perfectly good design when the reader is always there, and this reader is not.
//
// The `s` verb's suspending branch (commands.go) runs the guest shell through
// tea.ExecProcess, which blocks Bubble Tea's ENTIRE event loop — `case execMsg: //
// NB: this blocks` in bubbletea's own event loop — for as long as the user is
// attached. No Update runs, so readNextCmd is never re-issued, so nothing drains the
// pipe. What followed was two distinct bugs from the one cause:
//
//   - THE BUILD STOPPED. After the ~100 KiB the chain happens to hold (4 KiB in the
//     message already in flight, os/exec's 32 KiB copy buffer, the 64 KiB kernel
//     pipe), ansible itself blocked on write. A user who opened a shell to watch a
//     build was pausing that build by watching it.
//   - THE TILE REPLAYED. On detach the backlog drained 4 KiB at a time, one message
//     and one repaint each, so the tile's progress bar walked through every Ansible
//     task at once.
//
// So the buffer belongs HERE, on the host, where it costs memory rather than a
// stalled provisioner: Write never blocks, Read hands over everything buffered at
// once, and a run whose UI has gone away simply keeps running.
//
// # Why not just wrap the pipe in a goroutine
//
// A pump goroutine copying into a growing buffer needs exactly this mutex, this
// condition variable and this close bookkeeping, plus a goroutine to leak. Replacing
// the pipe outright is the smaller thing.
//
// # What it still owes io.Pipe
//
// The close semantics, because jobs.go depends on them. CloseWrite(nil) surfaces
// io.EOF to the reader and CloseWrite(err) surfaces err, exactly as
// PipeWriter.CloseWithError does — readNextCmd reads that difference as "finished"
// versus "failed". closeRead makes every LATER Write fail, which is what unblocks a
// run function that ignores its context and is what makes reaping a job (stopper.stop,
// jobs.go) a guarantee rather than a request.

import (
	"io"
	"sync"
)

// maxJobBuffer caps the undrained output one run may hold. It is a backstop, not a
// working limit: a whole verbose provision is a few hundred KiB, so nothing short of
// a run that has gone haywire (or a shell left attached for hours over a screenful a
// second) reaches it. The registry's retained log (job.output, jobs.go) is unbounded
// and always was, so this bound does not make the memory story worse — it only stops
// a runaway producer growing a SECOND unbounded copy while nobody reads.
const maxJobBuffer = 8 << 20

// jobReadChunk is how much one readNextCmd takes at a time. Big enough that the
// backlog from a suspended Update loop comes back in a few messages, small enough
// that a live build still streams onto the screen line by line — a run producing a
// line at a time never fills it, because Read returns what it has rather than
// waiting for a full buffer.
const jobReadChunk = 32 << 10

// jobElisionNotice marks the gap when maxJobBuffer is hit. Dropping silently would
// leave a log that reads as complete and is not — the one outcome worse than a
// truncated log. It is fenced in newlines so it lands on its own line, and so the
// Ansible progress parser (ansible.go) sees a clean line boundary either side of it.
const jobElisionNotice = "\n… earlier output dropped: this run produced more than sand would hold …\n"

// jobStream is one run's output buffer: an io.Writer for the provisioner and an
// io.Reader for readNextCmd. The writer never blocks; the reader blocks until there
// is something to hand over, the writer is done, or the job is reaped.
type jobStream struct {
	mu   sync.Mutex
	wake *sync.Cond

	buf []byte

	// elided records that maxJobBuffer has been hit, and notice that the reader has
	// not yet been told. They are two flags rather than one because the notice is
	// emitted ONCE (a log fenced with the same warning every 8 MiB tells the reader
	// nothing the first one did not) while the fact stays true forever.
	elided bool
	notice bool

	wclosed bool  // the run finished
	werr    error // and this is what it finished with (nil ⇒ io.EOF)
	rerr    error // the reader was closed with this; every Write now fails with it
}

func newJobStream() *jobStream {
	s := &jobStream{}
	s.wake = sync.NewCond(&s.mu)
	return s
}

// Write is the RUN side, and it never blocks. It is called from the provisioner's
// goroutine — in practice from os/exec's stdout copier, which cmd.Wait() joins, so
// blocking here is what would make a run untearable-down.
func (s *jobStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// A reaped job's writes fail rather than accumulate: that error is the whole
	// mechanism by which a run nobody is watching any more stops running.
	if s.rerr != nil {
		return 0, s.rerr
	}
	if s.wclosed {
		return 0, io.ErrClosedPipe
	}

	s.buf = append(s.buf, p...)
	if over := len(s.buf) - maxJobBuffer; over > 0 {
		// Drop from the FRONT. What a user opens a failed build's log for is the
		// error at the end of it, so the tail is the half worth keeping.
		n := copy(s.buf, s.buf[over:])
		s.buf = s.buf[:n]
		// The notice is NOT written into the buffer here, and that is the whole
		// reason it is a flag: a notice sitting at the front of the buffer is the
		// first thing the NEXT overflow drops, so a run that keeps overflowing would
		// quietly eat its own truncation warning. Read emits it instead.
		if !s.elided {
			s.elided, s.notice = true, true
		}
	}

	s.wake.Signal()
	return len(p), nil
}

// Read is the UI side. It hands over as much as it has, up to len(p) — so a backlog
// that built up while Update was blocked comes back in a few big chunks instead of
// hundreds of 4 KiB ones, each of which would cost its own message and repaint.
//
// It blocks while there is nothing to say, which is what parks readNextCmd's
// goroutine between chunks exactly as the io.Pipe did.
func (s *jobStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for len(s.buf) == 0 && s.rerr == nil && !s.wclosed {
		s.wake.Wait()
	}

	if s.rerr != nil {
		return 0, s.rerr
	}
	// Announce a gap at the point the reader reaches it — never silently, since a
	// truncated log that reads as complete is the one outcome worse than a short one.
	if s.notice {
		s.buf = append([]byte(jobElisionNotice), s.buf...)
		s.notice = false
	}
	if len(s.buf) > 0 {
		// Buffered output is drained even after the run has closed its end: the last
		// lines of a build are the ones that say why it failed.
		n := copy(p, s.buf)
		rest := copy(s.buf, s.buf[n:])
		s.buf = s.buf[:rest]
		return n, nil
	}
	if s.werr != nil {
		return 0, s.werr
	}
	return 0, io.EOF
}

// CloseWrite ends the run's side of the stream, mirroring
// io.PipeWriter.CloseWithError: nil means a clean finish (the reader sees io.EOF
// once it has drained), and a non-nil err is what the reader gets instead.
func (s *jobStream) CloseWrite(err error) {
	s.mu.Lock()
	if !s.wclosed {
		s.wclosed, s.werr = true, err
	}
	s.mu.Unlock()
	s.wake.Broadcast()
}

// closeRead tears the reading end down, mirroring io.PipeReader.CloseWithError. The
// buffer goes with it — nobody is going to read it — and every subsequent Write
// fails with err, which is what unblocks a run that is mid-write.
func (s *jobStream) closeRead(err error) {
	if err == nil {
		err = io.ErrClosedPipe
	}
	s.mu.Lock()
	if s.rerr == nil {
		s.rerr = err
	}
	s.buf = nil
	s.mu.Unlock()
	s.wake.Broadcast()
}
