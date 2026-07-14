package ui

// messages.go is the board's activity log: a bounded, SESSION-ONLY ring
// buffer of timestamped lines, and the single replacement for the model's
// old overwritten `status` field. Persisting this across restarts is the
// deferred run-history feature — not this one; the ring exists only so a long
// session cannot grow it without bound.
//
// Before it, the board and the VM screen each rendered an ad-hoc
// confirm/acting/status switch, duplicated verbatim between listView (now
// board.go) and detailView (detail.go). activityLineView below is the one copy
// both now call; messagesStripView is new: the board's docked, multi-line
// history, which the single-line status switch never had room to be.

import (
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// message is one timestamped line in the session's activity log.
type message struct {
	at   time.Time
	text string
}

// maxMessages bounds the ring. sand can run for hours in one session (or
// days, over ssh); a log that grew forever would be the memory-leak twin of
// the invisibility this whole plan exists to remove.
const maxMessages = 50

// logMsg appends one line to the session's message log, timestamped now, and
// drops the oldest entries once the ring is full. text == "" is a deliberate
// no-op: several call sites (a shell returning, a browse opening cleanly)
// have nothing to report — a log has no "current value" to clear the way the
// old status field did, so saying nothing is simply not appending.
func (m *model) logMsg(text string) {
	if text == "" {
		return
	}
	m.messages = append(m.messages, message{at: time.Now(), text: text})
	if over := len(m.messages) - maxMessages; over > 0 {
		m.messages = m.messages[over:]
	}
}

// lastMessage is the latest logged line, or "" if the session has logged
// nothing yet. It is what activityLineView (below) renders as the board/VM
// screen's single "what just happened" line, and what the acting spinner
// sits beside.
func (m model) lastMessage() string {
	if len(m.messages) == 0 {
		return ""
	}
	return m.messages[len(m.messages)-1].text
}

// recentMessages returns up to n of the most recently logged lines, OLDEST
// FIRST (chronological) — the order messagesStripView renders newest-last,
// mirroring a scrolling log.
func (m model) recentMessages(n int) []message {
	if n <= 0 || len(m.messages) == 0 {
		return nil
	}
	if n > len(m.messages) {
		n = len(m.messages)
	}
	return m.messages[len(m.messages)-n:]
}

// activityLineView renders the board/VM screen's single "what's happening
// right now" line — ONE row, always, clipped to ContentWidth like every other
// line the screens spend, because the footer band budgets exactly one row for
// it (layout.go) and a status message long enough to wrap would take the help
// bar's row with it: the pending confirm prompt when one is open (it must
// interrupt, not queue behind history), the acting spinner beside the latest
// logged message while a lifecycle action is in flight, or just the latest
// logged message otherwise. "" means there is nothing to show, and callers
// must render no line at all rather than a blank one.
func (m model) activityLineView() string {
	switch {
	case m.confirm != nil:
		return m.confirmView()
	case m.acting:
		text := m.lastMessage()
		if text == "" {
			text = "working…"
		}
		return m.clipLine(statusStyle.Render(m.spinner.View() + " " + text))
	default:
		// A plain logged message is the STRIP's job, and only the strip's. When it
		// is on screen (MessagesHeight >= 1) it already shows this exact line —
		// it renders the most recent messages, newest last — so repeating it here
		// printed the newest message TWICE on the same screen, once above the grid
		// and once below it, which reads as a rendering bug rather than as two
		// panes doing different jobs.
		//
		// The two cases above still render unconditionally, and that is the whole
		// reason this line exists: a confirm the user must answer, and the spinner
		// for an action in flight, must survive the strip being shed on a short
		// terminal (layout.go drops it first). History can be lost to a small
		// terminal; a question cannot.
		if m.layout.MessagesHeight >= 1 {
			return ""
		}
		if text := m.lastMessage(); text != "" {
			return m.clipLine(statusStyle.Render(text))
		}
		return ""
	}
}

// messagesStripView renders the DOCKED activity log: up to MessagesHeight of
// the session's most recently logged lines, newest at the bottom. It is
// padded to EXACTLY MessagesHeight lines (blank where there is no message
// yet) so the grid below it never shifts as the log fills in — the same
// reasoning tile.go's fixed tileHeight uses.
//
// This is the FIRST pane the layout classifier sheds as the terminal
// contracts (see layout.go): MessagesHeight is 0 below messagesMinHeight, and
// this renders "" in that case — the board must, and does, render correctly
// without it. The pending confirm / acting spinner are NOT duplicated here:
// activityLineView (above) already renders them, unconditionally, below the
// grid — so a confirmation the user must answer is never lost just because
// the terminal is too short to show the strip.
func (m model) messagesStripView() string {
	height := m.layout.MessagesHeight
	if height < 1 {
		return ""
	}
	recent := m.recentMessages(height)
	lines := make([]string, height)
	for i := 0; i < height; i++ {
		idx := len(recent) - height + i
		if idx < 0 {
			continue // pad with a blank line; nothing logged for this slot yet
		}
		lines[i] = statusStyle.Render(ansi.Truncate(recent[idx].text, m.layout.ContentWidth, "…"))
	}
	return strings.Join(lines, "\n")
}
