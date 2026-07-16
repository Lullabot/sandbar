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

	// HeaderBandLines is how many EXTRA header rows (task 10: one per-profile
	// stats band or disabled/errored banner, beyond the base title+counts) the
	// caller may render this frame — already folded into HeaderHeight above.
	// It is granted, not requested: classifyWithHeaderBands negotiates it
	// against the same budget the messages strip and the help bar compete
	// for, and sheds it FIRST (before the messages strip, before the full
	// header) when a short terminal cannot afford everything, so a large
	// fleet's status lines can never push the grid or the footer off the
	// bottom. header.go's headerBandLines is what turns a granted count
	// smaller than the fleet's actual line count into a summarized "+K more"
	// row rather than silently truncating the list.
	HeaderBandLines int

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

// GridWidth is how wide a FULL row of tiles actually renders: the columns plus
// the gaps between them. It is NOT ContentWidth — TileWidth is an integer
// division of the space the columns share, so the remainder (up to Columns-1
// cells) is left over, and a pane drawn to ContentWidth overhangs the tiles it
// sits with by exactly that much. Anything that must line up with the grid — the
// messages box — measures itself against this, not against the terminal.
func (l layoutMode) GridWidth() int {
	cols := l.Columns
	if cols < 1 {
		cols = 1
	}
	w := cols*l.TileWidth + tileGap*(cols-1)
	if w > l.ContentWidth {
		w = l.ContentWidth // a single tile forced narrower than tileMinWidth
	}
	return clamp(w, minBudget)
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

// Row budgets for the header/messages/footer bands. headerHeightFull is the
// title plus a VM-count line; headerHeightCompact folds the counts into the
// title line. The messages box (between the grid and the footer band) is the one
// pane whose height is NOT fixed — see below. footerBandHeight is the closing
// band under it.
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
//
// It requests zero header bands (see classifyWithHeaderBands): the plain
// two-argument shape every pre-task-10 caller and test still uses, for a
// fleet whose header never grows past the base title+counts.
func classifyWithFooter(w, h, helpLines int) layoutMode {
	return classifyWithHeaderBands(w, h, helpLines, 0)
}

// classifyWithHeaderBands is classifyWithFooter plus task 10's per-profile
// header rows: headerBands is how many EXTRA lines the header wants (one per
// connected/disabled/errored fleet member beyond the first — see
// model.desiredHeaderBands), and HeaderBandLines on the returned mode is how
// many it was actually GRANTED, which may be fewer.
//
// Bands are budgeted, not just requested, for the same reason the help bar's
// line count is: a header that renders more rows than the layout counted is
// exactly the kind of drift that broke 80x24 for the footer once already, and
// a large fleet can want an unbounded number of them. They are also the FIRST
// thing shed when a short terminal cannot afford everything (before the
// messages strip, before the full header) — least essential, because a
// summarized "+K more" row (header.go's headerBandLines) says something
// useful with a single line where the messages strip or the title row cannot.
func classifyWithHeaderBands(w, h, helpLines, headerBands int) layoutMode {
	if helpLines < 1 {
		helpLines = 1
	}
	if headerBands < 0 {
		headerBands = 0
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

	headerBase := headerHeightCompact
	if headerFull {
		headerBase = headerHeightFull
	}

	// Grant bands what is left once the SMALLEST box we would draw, the footer's
	// fixed chrome, and a minimal grid row are reserved — capped BEFORE
	// messagesHeight is sized below, so a fleet asking for more bands than fit
	// sheds them here rather than the messages strip reading as already gone by
	// the time the shedding loop (below) gets a turn. Bands are the least
	// essential row this header renders (a summarized "+K more" says something
	// useful in one line; the messages strip and the title row cannot shrink
	// that gracefully), which is why they are capped first, ahead of helpLines.
	minMessages := 0
	if showMessages {
		minMessages = messagesStripChrome + messagesStripMinLines
	}
	maxBands := contentHeight - headerBase - minMessages - footerBandChrome - minBudget
	if showMessages {
		// The box's OWN sizing (below) reserves two full rows of tiles ahead of
		// itself — it may never cost a tile row — so bands must respect that same
		// reservation, or a large fleet's bands would silently eat the room the
		// box needs and read as "shed" before the shedding loop even runs.
		maxBands -= 2 * tileHeight
	}
	if maxBands < 0 {
		maxBands = 0
	}
	grantedBands := headerBands
	if grantedBands > maxBands {
		grantedBands = maxBands
	}
	headerHeight := headerBase + grantedBands

	// Grant the help bar what is left once the header (base + granted bands), the
	// SMALLEST box we would draw, and a minimal grid are paid for. It may ask for
	// more than the terminal has. The smallest box is the right reservation here: the
	// box is sized below, out of what survives THIS decision, so assuming a big one
	// would starve the help bar — and the help bar is the row that tells the user
	// which keys exist.
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
	// Shed the least-essential pane first as the budget goes negative: the header
	// bands, then the messages strip, then the header's full band. The grid and
	// footer never shrink away — they are floored to minBudget as a last resort so
	// classify always returns a renderable mode.
	for grid < minBudget && grantedBands > 0 {
		grantedBands--
		headerHeight = headerBase + grantedBands
		grid = contentHeight - headerHeight - messagesHeight - footerHeight
	}
	if grid < minBudget && showMessages {
		showMessages = false
		messagesHeight = 0
		grid = contentHeight - headerHeight - messagesHeight - footerHeight
	}
	if grid < minBudget && headerFull {
		headerFull = false
		headerBase = headerHeightCompact
		headerHeight = headerBase + grantedBands
		grid = contentHeight - headerHeight - messagesHeight - footerHeight
	}
	grid = clamp(grid, minBudget)

	return layoutMode{
		ContentWidth:  contentWidth,
		ContentHeight: contentHeight,

		Columns:   columns,
		TileWidth: tileWidth,

		HeaderHeight:    headerHeight,
		HeaderFull:      headerFull,
		HeaderBandLines: grantedBands,

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
