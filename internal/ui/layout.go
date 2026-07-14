package ui

// layoutMode is the one place terminal size becomes budgets. classify(w, h)
// is a pure function — no lipgloss rendering, no model, no I/O — so the whole
// responsive contract is testable without a terminal (see layout_test.go).
// Every screen derives its size from a layoutMode instead of hardcoding an
// offset against the raw width/height, which is what let the old per-screen
// terminal-size subtractions (one per pane, duplicated in places) drift out
// of sync with each other in the first place.
type layoutMode struct {
	// ContentWidth/ContentHeight are the usable area inside appStyle's
	// Padding(1, 2): every screen's text and panes live inside this box, not
	// the raw terminal size.
	ContentWidth  int
	ContentHeight int

	// Columns/TileWidth are the tile grid's budget: how many tile columns fit
	// side by side, and how wide each one gets. Task 07 renders tiles at
	// TileWidth x tileHeight (below); this task only decides the numbers.
	Columns   int
	TileWidth int

	// HeaderHeight/HeaderFull: the header band sheds from full (title + VM
	// counts) to compact (counts folded into one line) before anything else
	// but the messages strip.
	HeaderHeight int
	HeaderFull   bool

	// MessagesHeight is the messages strip's row budget; the strip is the first pane
	// to go as the terminal shrinks, and 0 means it is gone. There is deliberately no
	// companion ShowMessages bool — it was one, and two fields that must agree are
	// two fields that can disagree. `MessagesHeight > 0` IS the predicate, and it is
	// the one messages.go already gated on.
	MessagesHeight int

	// GridHeight is the main scrollable pane's row budget — the tile grid on
	// the board, and (until later tasks replace them) the list table, the
	// progress viewport, the secrets textarea, and the file browser. It never
	// goes to zero: the grid and the footer are the last things to shrink.
	GridHeight int

	// FooterHeight is the CLOSING BAND's row budget, not the help bar's alone:
	// the status/activity line, the name-filter indicator and the help bar
	// together (footerBandView, board.go), padded with blanks to exactly this
	// many rows. Budgeting only the help bar is what broke 80x24 — the board
	// spent rows on chrome nobody had counted, and the terminal clipped the
	// help bar right off the bottom.
	FooterHeight int

	// HelpLines is how many rows of the footer band the help bar is ALLOWED, which
	// is not always how many it asked for. The bar wraps rather than truncating, and
	// on a pathologically narrow terminal every item wraps onto its own line — at
	// one column wide, eight verbs want twenty-eight rows and there is no terminal
	// that can pay for them. classifyWithFooter grants what is affordable after the
	// header, the messages strip and a minimal grid are paid for, and the band cuts
	// the bar to fit. Wrapping is a courtesy; the grid is not.
	HelpLines int
}

// Tile size budget, exported for task 07 (the board/tile renderer) and task
// 08 to build against. A tile's content is at most six lines (title, status,
// cpu, mem, disk, up/last-used) plus a rounded border top and bottom, hence
// tileHeight = 6 + 2. tileMinWidth is the narrowest a tile can render before
// its content gets cramped; classify uses it to decide how many columns fit,
// but a tile is never dropped below it by choice — only forced narrower when
// the terminal itself is smaller than tileMinWidth (see TileWidth).
const (
	tileHeight   = 8
	tileMinWidth = 40
	tileGap      = 2 // blank columns rendered between adjacent tiles
)

// appStyle's Padding(1, 2): 1 row top+bottom, 2 columns left+right.
const (
	appPaddingV = 2
	appPaddingH = 4
)

// Fixed row budgets for the header/messages/footer bands. headerHeightFull is
// the title plus a VM-count line; headerHeightCompact folds the counts into
// the title line. messagesStripHeight is the messages pane shown between the
// grid and the footer band. footerBandHeight is the closing band below it.
const (
	headerHeightFull    = 2
	headerHeightCompact = 1
	// The messages box asks for messagesStripMaxLines of history and settles for
	// as few as messagesStripMinLines when the terminal cannot afford ten — below
	// that it is shed entirely rather than shrink to a sliver. Its height is
	// therefore NOT a constant: classify grants it whatever is spare once the
	// header, the footer and TWO ROWS OF TILES are paid for (see there), because
	// the box may never cost a tile row. The frame's two rows are budgeted on top
	// of the lines, never taken out of them.
	messagesStripChrome   = 2  // the frame's top (with the "Messages" title) and bottom
	messagesStripMinLines = 3  // fewer than this and the box is not worth its frame
	messagesStripMaxLines = 10 // all the history the box will ever show at once

	// footerBandHeight is the closing band: the activity line (which carries a
	// pending confirmation, so it may never be shed), the name-filter
	// indicator, and the help bar. Three rows, ALWAYS — blanks take up the
	// slack when the optional two are absent, so the band's height is a
	// constant the grid's budget can be derived against and the help bar
	// cannot be pushed off the bottom of the terminal by a status line
	// appearing. See footerBandView.
	footerBandHeight = 3

	// footerBandChrome is the band's two non-help rows: the activity line (which
	// carries a pending confirmation, so it may never be shed) and the name-filter
	// indicator. The help bar's own rows are added on top — see classifyWithFooter.
	footerBandChrome = 2

	// minBudget is the floor every derived budget is clamped to, so classify
	// always returns a renderable mode — there is no terminal size at which
	// sand shows a "terminal too small" wall.
	minBudget = 1

	// fullHeaderMinHeight/messagesMinHeight are the terminal-height
	// thresholds below which the header compacts and the messages strip
	// hides, respectively. They encode the shedding order: messages strip
	// first, then a compact header; the grid and footer never go.
	//
	// messagesMinHeight exists because THE BOX MAY NOT COST A TILE ROW: the tiles
	// are the board's reason to exist, and the box's oldest line is the least of
	// what a short terminal can lose. So the threshold is the shortest terminal
	// that affords two rows of tiles AND the smallest box worth drawing:
	//
	//	2 padding + 2 header + (2 frame + 3 lines) + 16 tiles (2 rows of tileHeight 8) + 3 footer = 28
	//
	// It is DERIVED, not a magic number, so raising messagesStripMaxLines cannot
	// silently push the threshold out from under it: a taller box simply shows
	// fewer lines on a shorter terminal (classify grants it the spare rows), and
	// this stays the floor at which it is shed instead.
	fullHeaderMinHeight = 20
	messagesMinHeight   = appPaddingV + headerHeightFull + messagesStripChrome +
		messagesStripMinLines + 2*tileHeight + footerBandHeight
)

// classify maps a terminal size to the budgets every screen sizes itself
// from. It is called from exactly one place: the WindowSizeMsg handler in
// model.go's applySize. Nothing else may call it — every pane's size must
// trace back to that single decision.
func classify(w, h int) layoutMode { return classifyWithFooter(w, h, 1) }

// classifyWithFooter is classify with the help bar's ACTUAL line count, which is no
// longer always one: the footer wraps rather than truncating (footerLines,
// model.go), so a board offering eight verbs on a narrow terminal spends two or
// three rows on them. Those rows have to be BUDGETED, not taken silently — a band
// that renders more rows than the layout counted is exactly what pushed the help bar
// off the bottom of an 80x24 terminal once already.
func classifyWithFooter(w, h, helpLines int) layoutMode {
	if helpLines < 1 {
		helpLines = 1
	}
	contentWidth := clamp(w-appPaddingH, minBudget)
	contentHeight := clamp(h-appPaddingV, minBudget)

	columns := (contentWidth + tileGap) / (tileMinWidth + tileGap)
	if columns < 1 {
		columns = 1
	}
	tileWidth := clamp((contentWidth-tileGap*(columns-1))/columns, minBudget)

	headerFull := h >= fullHeaderMinHeight
	showMessages := h >= messagesMinHeight

	headerHeight := headerHeightCompact
	if headerFull {
		headerHeight = headerHeightFull
	}
	// Grant the help bar what is left once the header, the SMALLEST box we would
	// draw, and a minimal grid are paid for. It may ask for more than the terminal
	// has. The smallest box is the right reservation here: the box is sized below,
	// out of what survives THIS decision, so assuming a big one would starve the
	// help bar — and the help bar is the row that tells the user which keys exist.
	minMessages := 0
	if showMessages {
		minMessages = messagesStripChrome + messagesStripMinLines
	}
	affordable := contentHeight - headerHeight - minMessages - footerBandChrome - minBudget
	if affordable < 1 {
		affordable = 1
	}
	if helpLines > affordable {
		helpLines = affordable
	}

	// The band is the activity line + the filter indicator + the help bar's rows,
	// never fewer than the three it has always been.
	footerHeight := footerBandChrome + helpLines
	if footerHeight < footerBandHeight {
		footerHeight = footerBandHeight
	}

	// The box takes what is SPARE once the header, the footer and two rows of tiles
	// are paid for — capped at messagesStripMaxLines, and shed outright below
	// messagesStripMinLines rather than shrunk to a sliver. This is what lets it ask
	// for ten lines without that becoming a tax on every terminal too short to seat
	// them: a shorter terminal simply shows fewer messages, and only a terminal that
	// cannot seat three loses the box. It may never take the second tile row, which
	// is why 2*tileHeight is subtracted BEFORE the box is sized rather than after.
	messagesHeight := 0
	if showMessages {
		lines := contentHeight - headerHeight - footerHeight - 2*tileHeight - messagesStripChrome
		if lines > messagesStripMaxLines {
			lines = messagesStripMaxLines
		}
		if lines >= messagesStripMinLines {
			messagesHeight = messagesStripChrome + lines
		}
	}

	grid := contentHeight - headerHeight - messagesHeight - footerHeight
	// Shed the least-essential pane first as the budget goes negative: the
	// messages strip, then the header's full band. The grid and footer never
	// shrink away — they are floored to minBudget as a last resort so classify
	// always returns a renderable mode.
	if grid < minBudget && showMessages {
		showMessages = false
		messagesHeight = 0
		grid = contentHeight - headerHeight - messagesHeight - footerHeight
	}
	if grid < minBudget && headerFull {
		headerFull = false
		headerHeight = headerHeightCompact
		grid = contentHeight - headerHeight - messagesHeight - footerHeight
	}
	grid = clamp(grid, minBudget)

	return layoutMode{
		ContentWidth:  contentWidth,
		ContentHeight: contentHeight,

		Columns:   columns,
		TileWidth: tileWidth,

		HeaderHeight: headerHeight,
		HeaderFull:   headerFull,

		MessagesHeight: messagesHeight,

		GridHeight: grid,

		FooterHeight: footerHeight,
		HelpLines:    helpLines,
	}
}

// clamp floors n at min.
func clamp(n, min int) int {
	if n < min {
		return min
	}
	return n
}
