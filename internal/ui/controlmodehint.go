package ui

import "os/exec"

// controlmodehint.go is the board's one-shot nudge toward tmux control mode.
//
// The `S` verb has two branches (see shellCmd): inside a host tmux it opens a
// new window and the board stays live; outside one it SUSPENDS the board for as
// long as the user is attached. The suspend branch is the one users notice, and
// there are two ways out of it that nothing in the UI otherwise mentions:
//
//   - Start sand inside `tmux -CC new-session`. $TMUX is then set, so `S` takes
//     the fast path — and because iTerm2 renders a control-mode client's windows
//     as native tabs, each shell lands in a real tab beside a still-live board.
//   - Run `sand shell --cc NAME` from a separate window, which attaches to the
//     GUEST's tmux in control mode, so the guest's own windows become native tabs.
//
// Why a log line and not a permanent footer item or a `?`-screen entry: this is
// advice about how to LAUNCH sand, which is useless at the moment the user is
// reading the footer and actionable only next time they start it. It is worth
// saying once, when they have just felt the cost, and never again.
//
// Once per PROCESS, deliberately not persisted. A dotfile remembering that a tip
// was shown is state to find, migrate and eventually delete, for a single line of
// text; a user who quits and relaunches has just had the chance to act on it and
// is exactly who should see it again.

// hostHasTmux reports whether a tmux binary is on the host's PATH. It gates the
// hint because both halves of the advice require tmux on the host, and telling a
// user without it to run `tmux -CC` is a detour into installing a package, not a
// tip.
//
// A package-level var — the same seam runHostTmuxNewWindow and header.go's
// hostMemBytesFn use — so no unit test depends on whether the machine running it
// happens to have tmux.
var hostHasTmux = func() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// controlModeHint logs the control-mode tip if this run has not already, naming
// the VM the user just shelled into so the suggested command is copy-pasteable.
//
// It stays silent when $TMUX is already set: that user is on the fast path
// ALREADY, so the first half of the advice is redundant, and the second half is
// something `sand shell --cc` would refuse from inside tmux anyway (a tmux pane
// strips the control-mode handshake — see lima.AttachControl).
//
// The caller logs its own "attaching…" line first, so the two arrive in reading
// order in the strip the board restores on detach.
func (m *model) controlModeHint(name string) {
	if m.ccHintShown || hostInTmux() || !hostHasTmux() {
		return
	}
	m.ccHintShown = true
	m.logMsg("tip (iTerm2): `sand shell --cc " + name + "` opens native tabs; " +
		"or start sand inside `tmux -CC new-session` so S never suspends this board")
}
