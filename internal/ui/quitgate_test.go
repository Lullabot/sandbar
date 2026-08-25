package ui

import (
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// quitGateModel is heartbeatModel with the checkout sweep wired to a SECOND fake
// shell, so the two probes' connections can be counted apart — they are separate
// SSH connections in the real thing too (sweepshell.go), and a burst of "three
// prompts" only reads straight if the heartbeat's and the sweep's are told apart.
func quitGateModel(t *testing.T, hb, sw *fakeShell, managed ...string) model {
	t.Helper()
	m := heartbeatModel(t, hb, managed...)
	m.sweeps = newSweeps(sw)
	return m
}

// runningList renders names as a vmsLoadedMsg payload with every VM up.
func runningList(names []string) []vm.VM {
	pairs := make([]string, 0, len(names)*2)
	for _, n := range names {
		pairs = append(pairs, n, limaRunning)
	}
	return vms(pairs...)
}

// startsSoFar is how many guest connections the two registries have EVER opened,
// read under their own mutexes.
//
// It counts the DECISION, not its side effect, and that is what makes these tests
// both exact and instant. start() bumps nextEpoch and records the entry under the
// lock BEFORE it launches the goroutine that execs the shell (heartbeat.go,
// sweepshell.go), and l.send runs Update inline on the test's own goroutine — so
// the moment send returns, this is already a complete count of every connection
// that keypress committed to. Watching the fake shell's open events instead would
// mean sleeping for the spawned goroutines to catch up, which is a wait for
// information the test already has: ~1.7s across this file, for no assertion
// strength at all.
func startsSoFar(hb *heartbeatRegistry, sw *sweepRegistry) int {
	hb.mu.Lock()
	beats := hb.nextEpoch
	hb.mu.Unlock()
	sw.mu.Lock()
	sweeps := sw.nextEpoch
	sw.mu.Unlock()
	return int(beats + sweeps)
}

// liveConnections is how many of them are open RIGHT NOW, across both probes.
// stop/stopAll delete from the maps under the lock before cancelling, so this is
// as synchronous as startsSoFar.
func liveConnections(m model) int {
	return len(m.heartbeats.names(registry.LocalScope)) + len(m.sweeps.names(registry.LocalScope))
}

// THE KEY THAT QUITS MUST NOT OPEN CONNECTIONS. This is the regression this file
// exists for, reported from the field as several 1Password prompts arriving AFTER
// sand had already exited: the quit key refreshes lastInput like any other, which
// reopens the idle gate, and the reconcile at the bottom of that same Update
// starts a shell per running VM — unmultiplexed, one agent prompt each, and all of
// them abandoned mid-handshake when the process exits a moment later.
//
// See model.quitting for the full account of the mechanism; this file pins the
// behaviour rather than re-deriving the reasoning.
func TestQuittingOpensNoConnections(t *testing.T) {
	// Two VMs throughout: the gate under test is one model-level bool with no
	// per-VM branch, so a third VM would exercise nothing a second does not. What
	// DOES differ is how the quit is reached, which is what these cases vary.
	names := []string{"web", "api"}

	for _, tc := range []struct {
		what string
		idle bool
		busy string // a VM with a build in flight, which makes 'q' confirm first
	}{
		// The idle window (heartbeatIdleAfter) is the classic way in: sand sits
		// untouched, the gate shuts and drops everything, and the keypress that
		// wakes it is the same one that ends it.
		{"gone idle", true, ""},
		// Work in flight routes through the confirm overlay, so the quit spans TWO
		// keypresses ('q' then 'y') and neither may open anything. It is also where
		// an ODD prompt count comes from: the busy VM keeps its heartbeat but gets
		// no sweep (syncSweeps), so two VMs come to three connections.
		{"gone idle, one building", true, "api"},
		// And the case that was always fine, kept so a future change cannot make it
		// worse: connections already open are left alone, not reopened.
		{"never idle", false, ""},
	} {
		t.Run(tc.what, func(t *testing.T) {
			hb, sw := newFakeShell(), newFakeShell()
			l := newTeaLoop(t, quitGateModel(t, hb, sw, names...))

			l.send(vmsLoadedMsg{vms: runningList(names)})
			for _, n := range names {
				hb.await(t, "open:"+n)
				sw.await(t, "open:"+n)
			}

			if tc.idle {
				l.m.lastInput = time.Now().Add(-heartbeatIdleAfter - time.Second)
				l.send(vmsLoadedMsg{vms: runningList(names)})
				for _, n := range names {
					hb.await(t, "close:"+n)
					sw.await(t, "close:"+n)
				}
			}
			if tc.busy != "" {
				seedJob(t, &l.m, tc.busy, vm.CreateConfig{Name: tc.busy, BaseName: "sandbar-base"})
				// seedJob leaves the model on the progress screen, which does not
				// offer 'q' at all — put it back on the board, or this case would
				// pass by never pressing quit.
				l.m.view = viewBoard
			}

			before := startsSoFar(l.m.heartbeats, l.m.sweeps)

			l.send(runeKey('q'))
			if l.m.confirm != nil {
				if !l.m.confirm.quits {
					t.Fatalf("q raised a confirmation that is not marked as quitting: %q", l.m.confirm.prompt)
				}
				l.send(runeKey('y'))
			}

			if opened := startsSoFar(l.m.heartbeats, l.m.sweeps) - before; opened != 0 {
				t.Fatalf("quitting opened %d fresh SSH connection(s) — each is an unmultiplexed handshake and an agent prompt, and every one is orphaned when the process exits", opened)
			}
			if live := liveConnections(l.m); live != 0 {
				t.Fatalf("quitting left %d connection(s) open to be orphaned on exit — they must be cancelled in sand's own last Update", live)
			}
		})
	}
}

// ctrl+c IS A QUIT TOO, and it is the one most likely to be forgotten: it lives in
// dispatch (model.go) rather than beside the board's 'q', and it is what a user
// reaches for on a wedged remote — the worst possible moment to open connections
// nobody will ever answer for.
func TestCtrlCQuitOpensNoConnections(t *testing.T) {
	names := []string{"web", "api"}
	hb, sw := newFakeShell(), newFakeShell()
	l := newTeaLoop(t, quitGateModel(t, hb, sw, names...))

	l.send(vmsLoadedMsg{vms: runningList(names)})
	for _, n := range names {
		hb.await(t, "open:"+n)
		sw.await(t, "open:"+n)
	}
	l.m.lastInput = time.Now().Add(-heartbeatIdleAfter - time.Second)
	l.send(vmsLoadedMsg{vms: runningList(names)})
	for _, n := range names {
		hb.await(t, "close:"+n)
		sw.await(t, "close:"+n)
	}

	before := startsSoFar(l.m.heartbeats, l.m.sweeps)
	l.send(ctrlKey('c'))

	if opened := startsSoFar(l.m.heartbeats, l.m.sweeps) - before; opened != 0 {
		t.Fatalf("ctrl+c opened %d fresh SSH connection(s) on the way out", opened)
	}
	if live := liveConnections(l.m); live != 0 {
		t.Fatalf("ctrl+c left %d connection(s) open to be orphaned on exit", live)
	}
}

// QUITTING MUST CLOSE WHAT IT FINDS OPEN, which is the other half of the same bug
// and the half that made the prompts arrive after sand was gone.
//
// shouldTick gates on the active view as well as the idle window, so ANY trip to
// another screen tears every connection down and returning rebuilds them —
// legitimately: the user is on the board again and the gauges are theirs to watch.
// No five-minute wait is needed anywhere in this. Quit a keystroke later and sand
// used to simply exit, leaving those ssh clients parked on the agent socket.
func TestQuitClosesTheConnectionsItFindsOpen(t *testing.T) {
	names := []string{"web", "api"}
	hb, sw := newFakeShell(), newFakeShell()
	l := newTeaLoop(t, quitGateModel(t, hb, sw, names...))

	l.send(vmsLoadedMsg{vms: runningList(names)})
	for _, n := range names {
		hb.await(t, "open:"+n)
		sw.await(t, "open:"+n)
	}

	// '?' opens the help sheet: the gate shuts on the view alone.
	l.send(runeKey('?'))
	for _, n := range names {
		hb.await(t, "close:"+n)
		sw.await(t, "close:"+n)
	}
	// '?' again returns to the board and rebuilds them, which is correct.
	l.send(runeKey('?'))
	for _, n := range names {
		hb.await(t, "open:"+n)
		sw.await(t, "open:"+n)
	}
	if got := liveConnections(l.m); got != 2*len(names) {
		t.Fatalf("precondition: returning to the board should hold %d connections, got %d", 2*len(names), got)
	}

	// And now 'q'. Every live connection must be cancelled here, in sand's own
	// last Update — not left to a process exit that will not touch them.
	l.send(runeKey('q'))
	for _, n := range names {
		hb.await(t, "close:"+n)
		sw.await(t, "close:"+n)
	}
	if got := liveConnections(l.m); got != 0 {
		t.Fatalf("quit left %d connection(s) to be orphaned on exit", got)
	}
}

// AN UNANSWERED QUIT PROMPT IS A HOLD, NOT A TEARDOWN — see awaitingQuitAnswer.
// The obvious implementation makes the pending confirmation a third term in
// shouldTick, and that is wrong in a way this test pins: shouldTick drives stop and
// start together, so it would blank the gauges under a prompt the user is still
// reading, and answering 'n' would rebuild every connection — turning DECLINING a
// quit into its own burst of agent prompts.
func TestPendingQuitConfirmNeitherOpensNorCloses(t *testing.T) {
	names := []string{"web", "api"}
	hb, sw := newFakeShell(), newFakeShell()
	l := newTeaLoop(t, quitGateModel(t, hb, sw, names...))

	l.send(vmsLoadedMsg{vms: runningList(names)})
	for _, n := range names {
		hb.await(t, "open:"+n)
		sw.await(t, "open:"+n)
	}
	seedJob(t, &l.m, "api", vm.CreateConfig{Name: "api", BaseName: "sandbar-base"})
	l.m.view = viewBoard // seedJob leaves the model on the progress screen

	before := startsSoFar(l.m.heartbeats, l.m.sweeps)
	liveBefore := liveConnections(l.m)

	// 'q' with work in flight raises the overlay rather than quitting.
	l.send(runeKey('q'))
	if l.m.confirm == nil || !l.m.confirm.quits {
		t.Fatal("q with a build in flight should raise a quit confirmation")
	}
	if opened := startsSoFar(l.m.heartbeats, l.m.sweeps) - before; opened != 0 {
		t.Fatalf("a pending quit prompt opened %d connection(s); it must open nothing it may have to orphan", opened)
	}
	if live := liveConnections(l.m); live != liveBefore {
		t.Fatalf("a pending quit prompt took the live connections from %d to %d; the gauges must not blank under a prompt the user is still reading", liveBefore, live)
	}

	// Declining resumes with no handshake at all: nothing was closed, so nothing
	// has to be reopened.
	l.send(runeKey('n'))
	if l.m.confirm != nil {
		t.Fatal("declining should clear the overlay")
	}
	if opened := startsSoFar(l.m.heartbeats, l.m.sweeps) - before; opened != 0 {
		t.Fatalf("declining a quit cost %d fresh connection(s) — and so, on a per-connection agent, that many prompts", opened)
	}
	if !l.m.shouldTick() {
		t.Fatal("declining should leave the gate open — the user is still here")
	}
}
