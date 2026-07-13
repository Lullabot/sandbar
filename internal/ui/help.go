package ui

// help.go is the `?` screen: every key sand has, with a sentence saying what it
// does.
//
// It exists because the footer cannot say it. The footer is a reminder — "u upload"
// tells you which key, not what it copies, in which direction, or that it will ask
// you where to put it. And the footer only shows the verbs that apply to the tile
// under the ring right now, which is the correct thing for a footer to do and the
// wrong thing for a reference: a user who wants to know whether sand can do a thing
// at all should not have to first arrange for a VM to be in the state where it can.
//
// So this screen lists EVERY verb, whether or not it currently applies, and says
// when it does. The per-VM half is generated from the same registry (commandreg.go)
// the footer and the dispatcher derive from, so a verb cannot exist without
// appearing here — which is the whole point of there being one registry.

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// boardKeys are the keys that act on the BOARD rather than on a VM. They are not in
// the command registry — they take no VM and have no enabledFor — so they are listed
// here by hand. Keep this in sync with updateBoard; it is short, and it is the only
// hand-maintained list left.
var boardKeys = []struct {
	keys  string
	about string
}{
	{"↑ ↓ ← →", "Move the focus ring between tiles. Every verb below acts on the tile it is on."},
	{"enter", "On the empty slot, create a VM. On a VM's tile it does nothing — the tile already shows everything sand knows."},
	{"n", "Create a VM. Opens the form from anywhere on the board."},
	{"/", "Filter the tiles by name as you type. esc clears it. It narrows what you SEE and nothing else — X still stops every managed VM."},
	{"X", "Stop every running sand VM, after a confirmation. Base images and VMs sand did not create are never touched."},
	{"q", "Quit. If a build or a file transfer is still running, it confirms first rather than orphaning it."},
	{"?", "This screen."},
}

// openHelp shows the `?` screen.
func (m *model) openHelp() {
	m.helpScroll = 0
	m.view = viewHelp
}

// updateHelp handles keys on the `?` screen: it scrolls, and anything that means
// "done" returns to the board. `?` toggles, so the key that opened it closes it.
func (m model) updateHelp(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Back), key.Matches(msg, m.keys.Help):
		m.view = viewBoard
		m.helpScroll = 0
		return m, nil
	case msg.Code == tea.KeyDown:
		m.helpScroll++
	case msg.Code == tea.KeyUp:
		m.helpScroll--
	case msg.Code == tea.KeyPgDown:
		m.helpScroll += m.layout.GridHeight
	case msg.Code == tea.KeyPgUp:
		m.helpScroll -= m.layout.GridHeight
	}
	m.clampHelpScroll()
	return m, nil
}

// helpHelp is the `?` screen's own footer.
func (m model) helpHelp() []key.Binding {
	return []key.Binding{boardMove, m.keys.Back}
}

// helpLines renders the whole reference, one entry per paragraph: the keys, then a
// wrapped sentence indented under them.
func (m model) helpLines() []string {
	width := m.layout.ContentWidth
	// The key column is sized to the widest key label so every sentence starts in
	// the same column; the arrows are the widest by a distance.
	const keyCol = 9

	var lines []string
	section := func(title string) {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, labelStyle.Render(title))
	}
	entry := func(keys, about string) {
		// DISPLAY WIDTH, not byte length, and MEASURED, not assumed. "↑ ↓ ← →" is 7
		// cells and 19 bytes, and titleStyle carries its own padding — so the head's
		// width is the only honest input to the wrap. Computing it with len(), or
		// hardcoding it, wraps the sentences one cell too wide and clips every one of
		// them that had room to spare.
		pad := keyCol - ansi.StringWidth(keys)
		if pad < 1 {
			pad = 1
		}
		head := "  " + titleStyle.Render(keys) + strings.Repeat(" ", pad)
		headWidth := ansi.StringWidth(ansi.Strip(head))
		for i, l := range wrapText(about, width-headWidth) {
			if i == 0 {
				lines = append(lines, head+statusStyle.Render(l))
				continue
			}
			lines = append(lines, strings.Repeat(" ", headWidth)+statusStyle.Render(l))
		}
	}

	section("The board")
	for _, k := range boardKeys {
		entry(k.keys, k.about)
	}

	section("On the focused VM")
	for _, c := range vmCommands {
		entry(strings.Join(c.binding.Keys(), " "), c.about)
	}
	lines = append(lines, "")
	lines = append(lines, statusStyle.Render(wrapText(
		"A verb only appears in the footer when it applies to the focused VM — a stopped VM offers no shell, "+
			"and a VM that is still building offers nothing that would interrupt its build. The key does nothing when it is not offered.",
		width)[0]))

	return lines
}

// wrapText breaks a sentence at word boundaries to fit width.
func wrapText(s string, width int) []string {
	if width < 8 {
		width = 8
	}
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case ansi.StringWidth(line)+1+ansi.StringWidth(word) <= width:
			line += " " + word
		default:
			out = append(out, line)
			line = word
		}
	}
	if line != "" {
		out = append(out, line)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// helpBodyHeight is how many rows of the reference fit on screen.
func (m model) helpBodyHeight() int {
	h := m.layout.ContentHeight - 2 - m.layout.FooterHeight // title + blank + footer band
	if h < 1 {
		h = 1
	}
	return h
}

// clampHelpScroll keeps the scroll inside the content.
func (m *model) clampHelpScroll() {
	max := len(m.helpLines()) - m.helpBodyHeight()
	if max < 0 {
		max = 0
	}
	if m.helpScroll > max {
		m.helpScroll = max
	}
	if m.helpScroll < 0 {
		m.helpScroll = 0
	}
}

// helpView renders the `?` screen.
func (m model) helpView() string {
	all := m.helpLines()
	body := m.helpBodyHeight()

	start := m.helpScroll
	if start > len(all)-body {
		start = len(all) - body
	}
	if start < 0 {
		start = 0
	}
	end := start + body
	if end > len(all) {
		end = len(all)
	}

	var b strings.Builder
	title := "Keys"
	if len(all) > body {
		title += statusStyle.Render("   ↑↓ to scroll")
	}
	b.WriteString(m.clipLine(titleStyle.Render(title)))
	b.WriteString("\n\n")
	for _, l := range all[start:end] {
		b.WriteString(m.clipLine(l) + "\n")
	}
	b.WriteString("\n" + m.footerView(m.helpHelp()))
	return appStyle.Render(b.String())
}
