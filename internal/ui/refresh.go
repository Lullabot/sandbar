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

	tea "charm.land/bubbletea/v2"
)

// refreshInterval is how often the board re-lists the fleet while it is live.
// Long enough that a `limactl list` call every tick is not worth thinking
// about; short enough that a VM started or stopped from outside sand (another
// terminal, another tool) shows up without the user having to touch a key.
const refreshInterval = 5 * time.Second

// refreshTickMsg drives the board's periodic re-list. It carries nothing —
// the tick itself is the signal, and listCmd (commands.go) does the actual
// work.
type refreshTickMsg struct{}

// refreshTickCmd waits one refreshInterval and then fires.
func refreshTickCmd() tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return refreshTickMsg{} })
}

// tickRefresh starts the periodic re-list loop when shouldTick allows it and
// none is already running, and stops it (by simply declining to re-arm)
// otherwise. Called after EVERY message (see Update, mirroring
// syncHeartbeats), so the board resumes polling within one message of the
// idle gate reopening — the same discipline that lets the heartbeats' gate
// stay correct without being rechecked at a few remembered call sites.
//
// m.refreshing guards against a SECOND loop: without it, every message while
// the gate is open would spawn its own tea.Tick, and N messages would poll N
// times as often — the same bug tickSpinner's m.spinning guards against for
// the spinner.
func (m *model) tickRefresh() tea.Cmd {
	if !m.shouldTick() {
		m.refreshing = false
		return nil
	}
	if m.refreshing {
		return nil
	}
	m.refreshing = true
	return refreshTickCmd()
}
