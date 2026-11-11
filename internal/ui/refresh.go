package ui

// refresh.go re-lists the fleet while the board is open. Inactivity reduces
// polling to once per minute while preserving authenticated monitoring streams.
// shouldTick is shared with both guest probes to stop work off the board or on quit.
//
// Two invariants bound what this loop can cost a backend, and both were learned
// the hard way — a Proxmox node was taken down by a board whose request rate
// kept climbing while every response was still a 200:
//
//   - ONE loop per member. A tick that is not the member's current one is
//     dropped (tickGen), so a stale tick left over from a trip off the board
//     cannot start a second loop beside the fresh one.
//   - ONE refresh in flight per member (requestRefresh). A tick that finds the
//     previous refresh still running skips its turn rather than overlapping
//     it; a slow backend is then polled more slowly, never more densely.

import (
	"sync/atomic"
	"time"

	"github.com/lullabot/sandbar/internal/registry"

	tea "charm.land/bubbletea/v2"
)

// refreshInterval is how often the board re-lists the fleet while it is live.
// Long enough that a `limactl list` call every tick is not worth thinking
// about; short enough that a VM started or stopped from outside sandbar (another
// terminal, another tool) shows up without the user having to touch a key. It
// is also backoffSteps[0] (fleet.go): a healthy member and a member on its
// first errored retry poll at the same rate; only a persistent failure slows.
const refreshInterval = 5 * time.Second

// refreshStuckAfter is how long an in-flight refresh may go unanswered before
// the next request is allowed to supersede it. Every backend bounds its own
// calls well inside this (Proxmox's apiTimeout is 30s), so it only fires for a
// refresh that is genuinely wedged — and without it, one such refresh would
// stop the member's board from ever updating again.
const refreshStuckAfter = 2 * time.Minute

// refreshIDs issues tick generations and refresh sequence numbers. One
// process-wide counter, so no two ticks or refreshes — across members, and
// across a member rebuilt in place — ever share an id. Zero is never issued:
// it marks a hand-built message (tests), which is accepted as before.
var refreshIDs atomic.Uint64

// refreshTickMsg drives ONE fleet member's periodic re-list. It carries the
// member's scope so the handler re-arms and re-lists the RIGHT member — a fleet
// polls each member on its own timer, at its own (possibly backed-off) cadence,
// so one wedged profile's slow retries never gate a healthy profile's 5s
// refresh. A zero-value scope routes to the active member (tests). gen is the
// member's tickGen when the tick was armed; see fleetMember.tickGen.
type refreshTickMsg struct {
	scope registry.Scope
	gen   uint64
}

// refreshTickCmd waits d and then fires a tick for the member owning scope.
func refreshTickCmd(scope registry.Scope, d time.Duration, gen uint64) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return refreshTickMsg{scope: scope, gen: gen} })
}

// tickRefresh arms a per-member re-list loop for every member that needs one and
// isn't already running one, and stops them all (by declining to re-arm) once
// the idle gate closes. Called after EVERY message (see Update, mirroring
// syncHeartbeats), so the board resumes polling within one message of the gate
// reopening — the same discipline that keeps the heartbeat gate correct without
// being rechecked at a few remembered call sites.
//
// The per-member `arming` flag guards against a SECOND loop for that member:
// without it, every message while the gate is open would spawn its own tea.Tick
// for every member, and N messages would poll N times as often — the same bug
// tickSpinner's m.spinning guards against for the spinner, now per member so a
// healthy member keeps its steady cadence while a wedged one backs off. An error
// binding (nil provider) is never armed — there is nothing to list.
//
// Clearing `arming` when the gate closes does NOT cancel the tea.Tick already
// scheduled. Each arm therefore stamps a fresh tickGen, and the handler drops
// any tick that is not the member's current one — otherwise returning to the
// board before that old tick fired would leave two loops running for good.
func (m *model) tickRefresh() tea.Cmd {
	if !m.shouldTick() {
		for i := range m.members {
			m.members[i].arming = false
		}
		return nil
	}
	var cmds []tea.Cmd
	for i := range m.members {
		mem := &m.members[i]
		if mem.prov == nil || mem.arming {
			continue
		}
		mem.arming = true
		mem.tickGen = refreshIDs.Add(1)
		delay := mem.refreshDelay()
		if time.Since(m.lastInput) >= heartbeatIdleAfter && delay < idleProbeInterval {
			delay = idleProbeInterval
		}
		cmds = append(cmds, refreshTickCmd(mem.scope, delay, mem.tickGen))
	}
	return tea.Batch(cmds...)
}

// requestRefresh is the ONLY way a member's refreshCmd is started. With one
// already in flight it starts nothing: a periodic tick (queue=false) simply
// skips its turn, while a refresh someone asked for — the follow-up to a start,
// a stop, a finished build (queue=true) — is remembered and run as soon as the
// in-flight one lands, so the board never misses the change the user just made.
func (m *model) requestRefresh(i int, preflight, queue bool) tea.Cmd {
	mem := &m.members[i]
	if mem.prov == nil {
		return nil
	}
	if mem.refreshing && time.Since(mem.refreshSince) < refreshStuckAfter {
		if queue {
			mem.refreshQueued = true
			mem.refreshQueuedPreflight = mem.refreshQueuedPreflight || preflight
		}
		return nil
	}
	seq := refreshIDs.Add(1)
	mem.refreshing, mem.refreshSeq, mem.refreshSince = true, seq, time.Now()
	return stampRefresh(refreshCmd(mem.scope, mem.prov, mem.hostFiles, preflight), seq)
}

// refreshLanded closes the in-flight refresh a vmsLoadedMsg reports, and starts
// the queued one if there is one. A result whose seq is not the in-flight one
// (superseded after refreshStuckAfter, or from a member since rebuilt in place)
// changes nothing; a seq of zero is a hand-built message.
func (m *model) refreshLanded(scope registry.Scope, seq uint64) tea.Cmd {
	if seq == 0 {
		return nil
	}
	i, ok := m.memberIndex(scope)
	if !ok {
		return nil
	}
	mem := &m.members[i]
	if !mem.refreshing || mem.refreshSeq != seq {
		return nil
	}
	mem.refreshing = false
	if !mem.refreshQueued || mem.state == connDisabled {
		mem.refreshQueued, mem.refreshQueuedPreflight = false, false
		return nil
	}
	preflight := mem.refreshQueuedPreflight || mem.state != connConnected
	mem.refreshQueued, mem.refreshQueuedPreflight = false, false
	return m.requestRefresh(i, preflight, false)
}

// stampRefresh tags the vmsLoadedMsg a refresh produces with its sequence
// number, so refreshLanded can match the result to the request.
func stampRefresh(cmd tea.Cmd, seq uint64) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		if loaded, ok := msg.(vmsLoadedMsg); ok {
			loaded.seq = seq
			return loaded
		}
		return msg
	}
}
