package ui

import (
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// quitGateModel wires a model with BOTH long-lived probes on their OWN fake
// shells, so the connections each one opens can be counted apart — the sweep's
// and the heartbeat's are separate SSH connections in the real thing too
// (sweepshell.go), and the arithmetic behind a reported prompt count only reads
// straight if they are counted separately here.
func quitGateModel(t *testing.T, hb, sw *fakeShell, managed ...string) model {
	t.Helper()
	m := newTestModel(t)
	for _, name := range managed {
		if err := m.reg.Add(vm.CreateConfig{Name: name, BaseName: "sandbar-base"}); err != nil {
			t.Fatalf("seed %s as managed: %v", name, err)
		}
	}
	m.heartbeats = newHeartbeats(hb)
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

// openCount totals the shells a fake has EVER opened across names.
func openCount(sh *fakeShell, names []string) int {
	n := 0
	for _, name := range names {
		n += sh.opened(name)
	}
	return n
}

// closeCount is openCount's mirror, for the shells a fake has ever closed.
func closeCount(sh *fakeShell, names []string) int {
	n := 0
	for _, name := range names {
		n += sh.closed(name)
	}
	return n
}

// THE KEY THAT QUITS MUST NOT OPEN CONNECTIONS. This is the regression this file
// exists for, reported from the field as several 1Password prompts arriving
// AFTER sand had already exited.
//
// Update reconciles the heartbeats, the sweeps and the refresh loop after every
// message (model.go), and the quit key is just another message: it refreshes
// lastInput, which reopens the idle gate, and the reconcile at the bottom of
// that same Update then starts a shell per running VM — synchronously, inside
// the Update that returns tea.Quit, because start() execs before it returns.
//
// Every one of those is a full handshake: both probes are lima.WithoutMux, so
// none of them can ride the control master, and an agent that asks per
// connection (1Password, a hardware key) asks for each. Then the process exits
// while they are still authenticating. Nothing cancels them — their contexts are
// rooted at context.Background() — and a Go process does not take its children
// with it, so they are orphaned mid-handshake and the prompts outlive sand.
func TestQuittingOpensNoConnections(t *testing.T) {
	for _, tc := range []struct {
		what  string
		names []string
		idle  bool
		busy  string
	}{
		// The idle window (heartbeatIdleAfter) is the classic way in: sand sits
		// untouched, the gate shuts and drops everything, and the keypress that
		// wakes it is the same one that ends it.
		{"one VM, gone idle", []string{"web"}, true, ""},
		{"two VMs, gone idle", []string{"web", "api"}, true, ""},
		{"three VMs, gone idle", []string{"web", "api", "db"}, true, ""},
		// An ODD count, which is what a report of three prompts looks like: a VM
		// with a build in flight keeps its heartbeat but gets no sweep
		// (syncSweeps), so two VMs come to two heartbeats and one sweep.
		{"two VMs, one building, gone idle", []string{"web", "api"}, true, "api"},
		// And the case that was always fine, kept so a future change cannot make
		// it worse: connections already open are simply left alone.
		{"two VMs, never idle", []string{"web", "api"}, false, ""},
	} {
		t.Run(tc.what, func(t *testing.T) {
			hb, sw := newFakeShell(), newFakeShell()
			l := newTeaLoop(t, quitGateModel(t, hb, sw, tc.names...))

			l.send(vmsLoadedMsg{vms: runningList(tc.names)})
			for _, n := range tc.names {
				hb.await(t, "open:"+n)
				sw.await(t, "open:"+n)
			}

			if tc.idle {
				l.m.lastInput = time.Now().Add(-heartbeatIdleAfter - time.Second)
				l.send(vmsLoadedMsg{vms: runningList(tc.names)})
				for _, n := range tc.names {
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

			hbBefore, swBefore := openCount(hb, tc.names), openCount(sw, tc.names)

			// The user comes back and presses 'q'. With work in flight that raises
			// the confirm overlay instead, so answer it — both keypresses are on
			// the path out and neither may open anything.
			l.send(runeKey('q'))
			if l.m.confirm != nil {
				if !l.m.confirm.quits {
					t.Fatalf("q raised a confirmation that is not marked as quitting: %q", l.m.confirm.prompt)
				}
				l.send(runeKey('y'))
			}

			// start() registers its entry synchronously, before spawning the
			// goroutine that execs the shell, so the registry counts are a
			// race-free read of what this keypress committed to. The execs
			// themselves would land a moment later, hence the grace period.
			beats := len(l.m.heartbeats.names(registry.LocalScope))
			sweeps := len(l.m.sweeps.names(registry.LocalScope))
			time.Sleep(250 * time.Millisecond)
			newHB := openCount(hb, tc.names) - hbBefore
			newSW := openCount(sw, tc.names) - swBefore

			if newHB+newSW != 0 {
				t.Fatalf("quitting opened %d fresh SSH connection(s) (%d heartbeat, %d sweep) — each is an unmultiplexed handshake and an agent prompt, and every one is orphaned when the process exits",
					newHB+newSW, newHB, newSW)
			}
			if beats != 0 || sweeps != 0 {
				t.Fatalf("quitting left %d heartbeat(s) and %d sweep connection(s) registered — they must be cancelled here, not orphaned on exit", beats, sweeps)
			}
		})
	}
}

// QUITTING MUST CLOSE WHAT IT FINDS OPEN, which is the other half of the same
// bug and the half that made the prompts arrive after sand was gone.
//
// shouldTick gates on the active view as well as the idle window, so ANY trip to
// another screen tears every connection down and returning rebuilds them —
// legitimately: the user is on the board again and the gauges are theirs to
// watch. No five-minute wait is needed anywhere in this. Quit a keystroke later
// and sand used to simply exit, leaving those ssh clients parked on the agent
// socket. Now the last reconcile runs stopAll.
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
	if got := len(l.m.heartbeats.names(registry.LocalScope)); got != len(names) {
		t.Fatalf("precondition: returning to the board should hold %d heartbeats, got %d", len(names), got)
	}

	// And now 'q'. Every live connection must be cancelled in sand's own last
	// Update — not left to a process exit that will not touch them.
	l.send(runeKey('q'))
	for _, n := range names {
		hb.await(t, "close:"+n)
		sw.await(t, "close:"+n)
	}
	if got := len(l.m.heartbeats.names(registry.LocalScope)); got != 0 {
		t.Fatalf("quit left %d heartbeat(s) to be orphaned on exit", got)
	}
	if got := len(l.m.sweeps.names(registry.LocalScope)); got != 0 {
		t.Fatalf("quit left %d sweep connection(s) to be orphaned on exit", got)
	}
}

// AN UNANSWERED QUIT PROMPT IS A HOLD, NOT A TEARDOWN — see awaitingQuitAnswer.
// The obvious implementation makes the pending confirmation a third term in
// shouldTick, and that is wrong in a way this test pins: shouldTick drives stop
// and start together, so it would blank the gauges under a prompt the user is
// still reading, and answering 'n' would rebuild every connection — turning
// DECLINING a quit into its own burst of agent prompts.
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

	opensBefore := openCount(hb, names) + openCount(sw, names)
	closesBefore := closeCount(hb, names) + closeCount(sw, names)

	// 'q' with work in flight raises the overlay rather than quitting.
	l.send(runeKey('q'))
	if l.m.confirm == nil || !l.m.confirm.quits {
		t.Fatal("q with a build in flight should raise a quit confirmation")
	}

	time.Sleep(250 * time.Millisecond)
	if got := openCount(hb, names) + openCount(sw, names) - opensBefore; got != 0 {
		t.Fatalf("a pending quit prompt opened %d connection(s); it must open nothing it may have to orphan", got)
	}
	if got := closeCount(hb, names) + closeCount(sw, names) - closesBefore; got != 0 {
		t.Fatalf("a pending quit prompt closed %d connection(s); the gauges must not blank under a prompt the user is still reading", got)
	}

	// Declining resumes with no handshake at all: nothing was closed, so nothing
	// has to be reopened.
	l.send(runeKey('n'))
	if l.m.confirm != nil {
		t.Fatal("declining should clear the overlay")
	}
	time.Sleep(250 * time.Millisecond)
	if got := openCount(hb, names) + openCount(sw, names) - opensBefore; got != 0 {
		t.Fatalf("declining a quit cost %d fresh connection(s) — and so, on a per-connection agent, that many prompts", got)
	}
	if !l.m.shouldTick() {
		t.Fatal("declining should leave the gate open — the user is still here")
	}
}
