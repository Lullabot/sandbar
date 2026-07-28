package ui

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// readWithin runs one Read on its own goroutine and fails the test if it has not
// returned by the deadline — a Read that never returns is the failure mode every
// test here is guarding against, and a hung test reports it as a timeout with no
// explanation.
func readWithin(t *testing.T, s *jobStream, n int) (int, error) {
	t.Helper()
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		buf := make([]byte, n)
		got, err := s.Read(buf)
		done <- result{got, err}
	}()
	select {
	case r := <-done:
		return r.n, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("Read never returned")
		return 0, nil
	}
}

func readAllWithin(t *testing.T, s *jobStream, n int) string {
	t.Helper()
	type result struct {
		s   string
		err error
	}
	done := make(chan result, 1)
	go func() {
		buf := make([]byte, n)
		got, err := s.Read(buf)
		done <- result{string(buf[:got]), err}
	}()
	select {
	case r := <-done:
		return r.s
	case <-time.After(5 * time.Second):
		t.Fatal("Read never returned")
		return ""
	}
}

// THE BUG THIS TYPE EXISTS FOR. A suspending shell (tea.ExecProcess, commands.go)
// blocks Bubble Tea's whole event loop, so nothing drains a running build's output
// for as long as the user is attached. Against the io.Pipe this replaced, the
// provisioner blocked on write once the buffers in front of it filled — opening a
// shell to watch a build PAUSED that build. Writes must complete with no reader in
// sight, however many of them there are.
func TestJobStreamWritesNeverBlockWithNoReader(t *testing.T) {
	s := newJobStream()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 500 {
			if _, err := s.Write([]byte(strings.Repeat("ansible task output\n", 40))); err != nil {
				t.Errorf("write failed: %v", err)
				return
			}
		}
		s.CloseWrite(nil)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a run stalled writing to a stream nobody was reading — the io.Pipe bug is back")
	}
}

// AND THE OTHER HALF OF IT. The backlog must come back in a few big chunks, not one
// message per 4 KiB: every chunk costs a message, an Update and a repaint, which is
// what made the tile's progress bar replay every Ansible task on detach. Read hands
// over everything it is holding.
func TestJobStreamReadDrainsTheWholeBacklogAtOnce(t *testing.T) {
	s := newJobStream()
	for range 8 {
		if _, err := s.Write([]byte(strings.Repeat("x", 1000))); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if n, _ := readWithin(t, s, jobReadChunk); n != 8000 {
		t.Fatalf("Read returned %d bytes, want the whole 8000-byte backlog in one go", n)
	}
}

// A live run still streams line by line: Read returns what there is rather than
// waiting for its buffer to fill, so a build that prints one line a second does not
// sit invisible until 32 KiB have accumulated.
func TestJobStreamReadReturnsWhatItHasWithoutWaitingToFill(t *testing.T) {
	s := newJobStream()
	if _, err := s.Write([]byte("TASK [base : install]\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := readAllWithin(t, s, jobReadChunk); got != "TASK [base : install]\n" {
		t.Fatalf("Read = %q, want the one line that was written", got)
	}
}

// A reader with nothing to read parks — that is what holds readNextCmd's goroutine
// between chunks — and the next write wakes it.
func TestJobStreamReadBlocksUntilThereIsOutput(t *testing.T) {
	s := newJobStream()
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, jobReadChunk)
		n, _ := s.Read(buf)
		got <- string(buf[:n])
	}()

	select {
	case out := <-got:
		t.Fatalf("Read returned %q before anything was written", out)
	case <-time.After(50 * time.Millisecond):
	}

	if _, err := s.Write([]byte("late")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case out := <-got:
		if out != "late" {
			t.Fatalf("Read = %q, want %q", out, "late")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a write did not wake the blocked reader")
	}
}

// The close semantics readNextCmd reads a run's OUTCOME from, and which this type
// owes io.Pipe: a clean finish surfaces io.EOF, a failed one surfaces its own error
// — and either way the output written before it is drained first. The last lines of
// a build are the ones that say why it failed.
func TestJobStreamCloseWriteSurfacesTheOutcomeAfterDraining(t *testing.T) {
	boom := errors.New("provision failed")
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"a clean finish reads as EOF", nil, io.EOF},
		{"a failed run reads as its error", boom, boom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newJobStream()
			if _, err := s.Write([]byte("final line\n")); err != nil {
				t.Fatalf("write: %v", err)
			}
			s.CloseWrite(tc.err)

			if got := readAllWithin(t, s, jobReadChunk); got != "final line\n" {
				t.Fatalf("buffered output after close = %q, want it drained first", got)
			}
			if _, err := readWithin(t, s, jobReadChunk); !errors.Is(err, tc.want) {
				t.Fatalf("Read after drain = %v, want %v", err, tc.want)
			}
		})
	}
}

// Reaping a job must UNBLOCK ITS RUN, not merely forget about it (stopper.stop,
// jobs.go). With the io.Pipe that meant a run parked mid-write; here nothing is
// parked, so the guarantee is the error: every later write fails, which is what
// makes a run that ignores its context stop anyway.
func TestJobStreamClosedReaderFailsEveryLaterWrite(t *testing.T) {
	s := newJobStream()
	s.closeRead(errJobReaped)

	if _, err := s.Write([]byte("still going")); !errors.Is(err, errJobReaped) {
		t.Fatalf("write after reaping = %v, want %v", err, errJobReaped)
	}
	if _, err := readWithin(t, s, jobReadChunk); !errors.Is(err, errJobReaped) {
		t.Fatalf("read after reaping = %v, want %v", err, errJobReaped)
	}
}

// A blocked reader is woken by the reaping too, or readNextCmd's goroutine is
// parked on a job that no longer exists forever.
func TestJobStreamClosingTheReaderWakesABlockedRead(t *testing.T) {
	s := newJobStream()
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, jobReadChunk)
		_, err := s.Read(buf)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond) // let it park
	s.closeRead(errJobReaped)

	select {
	case err := <-done:
		if !errors.Is(err, errJobReaped) {
			t.Fatalf("woken Read = %v, want %v", err, errJobReaped)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reaping a job left its reader parked")
	}
}

// The backstop, and what it keeps. A run that outproduces anything a reader will
// ever take must not grow the buffer without bound — but it must not truncate
// SILENTLY either: a log that reads as complete and is not is worse than a short
// one. The tail survives (the error is at the end) and the gap is announced.
func TestJobStreamDropsTheOLDESTOutputAndSaysSo(t *testing.T) {
	s := newJobStream()
	for range 9 {
		if _, err := s.Write([]byte(strings.Repeat("a", 1<<20))); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if _, err := s.Write([]byte("THE LAST LINE\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	s.CloseWrite(nil)

	var got strings.Builder
	for {
		buf := make([]byte, jobReadChunk)
		n, err := s.Read(buf)
		got.Write(buf[:n])
		if err != nil {
			break
		}
	}
	out := got.String()

	if len(out) > maxJobBuffer+len(jobElisionNotice) {
		t.Fatalf("buffer grew to %d bytes, past the %d-byte cap", len(out), maxJobBuffer)
	}
	if !strings.HasSuffix(out, "THE LAST LINE\n") {
		t.Fatal("the TAIL is the half worth keeping, and it was dropped")
	}
	if !strings.Contains(out, strings.TrimSpace(jobElisionNotice)) {
		t.Fatal("output was dropped without saying so — a truncated log that reads as complete")
	}
}
