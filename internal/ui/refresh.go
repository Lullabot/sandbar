package ui

// refresh.go keeps the board LIVE: an interval timer that re-runs `limactl
// list` and re-renders, on the exact same idle-gating discipline as the guest
// heartbeats (heartbeat.go) and the spinner. Without it the board is a
// snapshot from whenever the screen was last touched, and every claim about
// it being "live" is false; with it, an idle `sand` left open in a
// backgrounded terminal, over ssh, on battery, still does not poll.
//
// shouldTick (heartbeat.go) is deliberately named for the general question,
// not its first caller — this file is the second, and reuses it rather than
// writing a second copy of the rule. Three independent copies of "is anyone
// watching" is exactly how the defaultKeys/viewHelp drift happened.

import (
	"time"

	"github.com/lullabot/sandbar/internal/registry"

	tea "charm.land/bubbletea/v2"
)

// refreshInterval is how often the board re-lists the fleet while it is live.
// Long enough that a `limactl list` call every tick is not worth thinking
// about; short enough that a VM started or stopped from outside sand (another
// terminal, another tool) shows up without the user having to touch a key. It
// is also backoffSteps[0] (fleet.go): a healthy member and a member on its
// first errored retry poll at the same rate; only a persistent failure slows.
const refreshInterval = 5 * time.Second

// refreshTickMsg drives ONE fleet member's periodic re-list. It carries the
// member's scope so the handler re-arms and re-lists the RIGHT member — a fleet
// polls each member on its own timer, at its own (possibly backed-off) cadence,
// so one wedged profile's slow retries never gate a healthy profile's 5s
// refresh. A zero-value scope routes to the active member (tests).
type refreshTickMsg struct{ scope registry.Scope }

// refreshTickCmd waits d and then fires a tick for the member owning scope.
func refreshTickCmd(scope registry.Scope, d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return refreshTickMsg{scope: scope} })
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
		cmds = append(cmds, refreshTickCmd(mem.scope, mem.refreshDelay()))
	}
	return tea.Batch(cmds...)
}
