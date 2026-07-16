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

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// message is one timestamped line in the session's activity log.
type message struct {
	at   time.Time
	text string

	// warn marks a message as a warning — a host or guest crossing below
	// lowFreeThreshold (hostwarn.go, tile.go's low-capacity-warning feature),
	// or any other line logged through logWarn rather than logMsg. Every
	// render site keys its "⚠ " + warnStyle treatment on this flag, so it must
	// travel with the entry rather than be inferred from its text.
	warn bool
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

// logWarn is logMsg's warning twin: the entry it appends renders with a "⚠ "
// marker and the repo's one warnStyle amber, everywhere messages render (the
// docked strip and the single-line activity view — see messagesStripView and
// activityLineView below). text == "" is the same deliberate no-op logMsg's
// own doc comment explains — nothing to report is simply not appending.
func (m *model) logWarn(text string) {
	if text == "" {
		return
	}
	m.messages = append(m.messages, message{at: time.Now(), text: text, warn: true})
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

// lastMessageWarn reports whether the latest logged line is a warning (see
// logWarn) — false, never a guess, when nothing has been logged yet.
// activityLineView's plain-message case (below) uses it to decide whether
// that one line gets the ⚠ + warnStyle treatment.
func (m model) lastMessageWarn() bool {
	if len(m.messages) == 0 {
		return false
	}
	return m.messages[len(m.messages)-1].warn
}

// warnPrefix prepends the single-cell "⚠ " marker for a warn entry — the same
// U+26A0 glyph tileWarnGaugeLine uses on a tile's gauge rows, deliberately
// NOT its two-cell emoji-presentation variant (see that function's own doc
// comment on why the distinction matters for width math). Applied BEFORE any
// truncation, so a warning clipped by width still shows the marker rather
// than losing it to the tail that gets cut.
func warnPrefix(text string, warn bool) string {
	if warn {
		return "⚠ " + text
	}
	return text
}

// messageStyleFor is the colour half of a message's warn treatment: warnStyle
// (the repo's one amber, styles.go) for a warning, the ordinary statusStyle
// grey otherwise — mirroring tileStyleFor's own status->style mapping.
func messageStyleFor(warn bool) lipgloss.Style {
	if warn {
		return warnStyle
	}
	return statusStyle
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
		// printed the newest message TWICE on the same screen — once in the box and
		// once again on this line, a few rows below it — which reads as a rendering
		// bug rather than as two panes doing different jobs.
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
			warn := m.lastMessageWarn()
			return m.clipLine(messageStyleFor(warn).Render(warnPrefix(text, warn)))
		}
		return ""
	}
}

// messagesStripView renders the DOCKED activity log: a titled "Messages" frame
// holding the session's most recently logged lines, newest at the bottom. It
// renders EXACTLY MessagesHeight rows — frame included — padded with blank lines
// where nothing has been logged yet, so the grid below it never shifts as the log
// fills in (the same reasoning tile.go's fixed tileHeight uses).
//
// The frame is drawn here rather than by a lipgloss border style because the
// title has to sit IN the top edge ("╭─ Messages ───╮") and lipgloss v2.0.5 has
// no titled border. Its two rows are budgeted in layout.go (messagesStripChrome),
// not taken from the message lines.
//
// This is the FIRST pane the layout classifier sheds as the terminal contracts
// (see layout.go): MessagesHeight is 0 below messagesMinHeight, and this renders
// "" in that case — the board must, and does, render correctly without it. The
// pending confirm / acting spinner are NOT duplicated here: activityLineView
// (above) already renders them, unconditionally, in the footer band — so a
// confirmation the user must answer is never lost just because the terminal is
// too short to show the box.
func (m model) messagesStripView() string {
	height := m.layout.MessagesHeight
	if height < 1 {
		return ""
	}
	// The box lines up with the TILES, not with the terminal: TileWidth is an
	// integer division of the space the columns share, so a box drawn to
	// ContentWidth overhangs the grid it sits under by the remainder (up to two
	// cells at a common width). GridWidth is what a full row of tiles measures.
	width := m.layout.GridWidth()
	rows := height - messagesStripChrome // the message lines inside the frame

	// A frame needs room for its two vertical edges and a space either side of the
	// text. On a terminal too narrow to afford that (or too short to afford the
	// chrome), fall back to the bare lines: the messages matter, the box does not.
	inner := width - 4
	if rows < 1 || inner < 1 {
		return strings.Join(m.messageLines(height, width), "\n")
	}

	out := make([]string, 0, height)
	out = append(out, messagesFrameTop(width))
	for _, text := range m.messageLines(rows, inner) {
		pad := inner - ansi.StringWidth(ansi.Strip(text))
		if pad < 0 {
			pad = 0
		}
		out = append(out, frameStyle.Render("│")+" "+text+strings.Repeat(" ", pad)+" "+frameStyle.Render("│"))
	}
	out = append(out, frameStyle.Render("╰"+strings.Repeat("─", width-2)+"╯"))
	return strings.Join(out, "\n")
}

// messagesFrameTop is the frame's top edge with the title spliced into it —
// "╭─ Messages ─────╮" — falling back to a plain edge when the terminal is too
// narrow to carry the label.
func messagesFrameTop(width int) string {
	const label = " Messages "
	fill := width - 2 - 1 - len(label) // corners, the leading ─, the label
	if fill < 0 {
		return frameStyle.Render("╭" + strings.Repeat("─", width-2) + "╮")
	}
	return frameStyle.Render("╭─") + frameTitleStyle.Render(label) +
		frameStyle.Render(strings.Repeat("─", fill)+"╮")
}

// messageLines is the last n logged messages, oldest first, each clipped to
// width — padded at the FRONT with blanks when fewer than n have been logged, so
// the newest always sits on the bottom row and the pane never changes height.
func (m model) messageLines(n, width int) []string {
	recent := m.recentMessages(n)
	lines := make([]string, n)
	for i := 0; i < n; i++ {
		idx := len(recent) - n + i
		if idx < 0 {
			continue // blank: nothing logged for this slot yet
		}
		text := ansi.Truncate(warnPrefix(recent[idx].text, recent[idx].warn), width, "…")
		lines[i] = messageStyleFor(recent[idx].warn).Render(text)
	}
	return lines
}
