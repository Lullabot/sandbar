package ui

// layoutMode is the one place terminal size becomes budgets. classify(w, h)
// is a pure function — no lipgloss rendering, no model, no I/O — so the whole
// responsive contract is testable without a terminal (see layout_test.go).
// Every screen derives its size from a layoutMode instead of hardcoding an
// offset against the raw width/height, which is what let the old per-screen
// terminal-size subtractions (one per pane, duplicated in places) drift out
// of sync with each other in the first place.
type layoutMode struct {
	Width, Height int // the raw size classify was given, for reference

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

	// ShowMessages/MessagesHeight: the messages strip is the first pane to go
	// as the terminal shrinks. MessagesHeight is 0 whenever ShowMessages is
	// false.
	ShowMessages   bool
	MessagesHeight int

	// GridHeight is the main scrollable pane's row budget — the tile grid on
	// the board, and (until later tasks replace them) the list table, the
	// progress viewport, the secrets textarea, and the file browser. It never
	// goes to zero: the grid and the footer are the last things to shrink.
	GridHeight int

	// FooterHeight is the help bar's row budget.
	FooterHeight int
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
// header and the grid. footerRowHeight is the help bar.
const (
	headerHeightFull    = 2
	headerHeightCompact = 1
	messagesStripHeight = 3
	footerRowHeight     = 1

	// minBudget is the floor every derived budget is clamped to, so classify
	// always returns a renderable mode — there is no terminal size at which
	// sand shows a "terminal too small" wall.
	minBudget = 1

	// fullHeaderMinHeight/messagesMinHeight are the terminal-height
	// thresholds below which the header compacts and the messages strip
	// hides, respectively. They encode the shedding order: messages strip
	// first, then a compact header; the grid and footer never go.
	fullHeaderMinHeight = 20
	messagesMinHeight   = 24
)

// classify maps a terminal size to the budgets every screen sizes itself
// from. It is called from exactly one place: the WindowSizeMsg handler in
// model.go's applySize. Nothing else may call it — every pane's size must
// trace back to that single decision.
func classify(w, h int) layoutMode {
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
	messagesHeight := 0
	if showMessages {
		messagesHeight = messagesStripHeight
	}
	footerHeight := footerRowHeight

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
		Width:  w,
		Height: h,

		ContentWidth:  contentWidth,
		ContentHeight: contentHeight,

		Columns:   columns,
		TileWidth: tileWidth,

		HeaderHeight: headerHeight,
		HeaderFull:   headerFull,

		ShowMessages:   showMessages,
		MessagesHeight: messagesHeight,

		GridHeight: grid,

		FooterHeight: footerHeight,
	}
}

// clamp floors n at min.
func clamp(n, min int) int {
	if n < min {
		return min
	}
	return n
}
