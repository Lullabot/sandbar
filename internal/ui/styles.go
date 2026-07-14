package ui

import "charm.land/lipgloss/v2"

// Shared lipgloss styles for the TUI. Colours are ANSI 256 indices so the UI
// degrades gracefully on limited terminals.
var (
	appStyle = lipgloss.NewStyle().Padding(1, 2)

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("231")).
			Background(lipgloss.Color("63")).
			Padding(0, 1)

	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

	// The messages strip's frame (messages.go). Deliberately DIMMER (238) than the
	// message text it holds (241): the box is furniture, and it must not out-shout
	// the log inside it or compete with the tile borders below, which are what the
	// eye should land on. The title is a shade brighter than the frame so it reads
	// as a label rather than as part of the line.
	frameStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	frameTitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	errStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	warnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	okStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))

	labelStyle        = lipgloss.NewStyle().Width(18).Foreground(lipgloss.Color("245"))
	focusedLabelStyle = lipgloss.NewStyle().Width(18).Bold(true).Foreground(lipgloss.Color("63"))

	// hintStyle is dim guidance text that must reflow to the full content width.
	// Unlike labelStyle it carries no fixed Width, so callers set one (via
	// .Width) to wrap it to the terminal rather than a form label's column.
	hintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63")).
			Padding(0, 1)

	// fieldInfoStyle renders the focused field's help under the create form: a
	// dim, left-bordered block so multi-line help (the GitHub token guidance)
	// reads as an aside rather than part of the form.
	fieldInfoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			BorderStyle(lipgloss.NormalBorder()).
			BorderLeft(true).
			BorderForeground(lipgloss.Color("63")).
			PaddingLeft(1)

	// Tile status colours (task 07, tile.go): the SAME ANSI-256 indices used
	// above, not a second palette — 42 (okStyle's green) for running, 241
	// (statusStyle's dim grey) for stopped, 214 (warnStyle's amber) for
	// building, 203 (errStyle's red) for failed. Colour is never the only
	// carrier of meaning: every tile also prints a glyph and the status word,
	// which is what a status test can still tell apart after ansi.Strip.
	tileTitleStyle    = lipgloss.NewStyle().Bold(true)
	tileRunningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	tileStoppedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	tileBuildingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	tileFailedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))

	// tileChromeStyle is the dim (never absent) treatment for a tile's
	// secondary rows: gauges, badges, the closing up/last-used line. Same 245
	// index as labelStyle/hintStyle/fieldInfoStyle above.
	tileChromeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	// tileFocusedBorderColor/tileUnfocusedBorderColor colour a tile's border;
	// the focused tile additionally switches to lipgloss.ThickBorder() (see
	// tile.go) so focus survives NO_COLOR and a monochrome terminal too — the
	// border glyphs themselves change, not just their colour.
	tileFocusedBorderColor   = lipgloss.Color("63")
	tileUnfocusedBorderColor = lipgloss.Color("245")

	// tileGhostBorderColor outlines the board's empty-slot ghost tile (board.go).
	// It is dimmer than an unfocused VM's border on purpose: the invitation to
	// create a VM must never compete with a VM that actually exists.
	tileGhostBorderColor = lipgloss.Color("240")
)

// The three shapes a tile frame can take, built ONCE rather than per tile per
// frame: renderTile and renderGhostTile used to construct a fresh lipgloss.Style
// (and a fresh border set) on every render, ~11 of them a frame at 10fps while a
// build spinner runs. Render does not mutate a style, so they are safe to share.
//
// The focused frame differs by GLYPH SET, not just colour, so the focus ring
// survives NO_COLOR and a monochrome terminal.
var (
	tileFrameStyle        = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(tileUnfocusedBorderColor).Padding(0, 1)
	tileFocusedFrameStyle = lipgloss.NewStyle().Border(lipgloss.ThickBorder()).BorderForeground(tileFocusedBorderColor).Padding(0, 1)
	tileGhostFrameStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(tileGhostBorderColor).Padding(0, 1)
)
