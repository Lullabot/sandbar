package ui

// scrollbar.go is the tile grid's scroll affordance: a one-cell track down the
// right of the grid, with a thumb whose SIZE says how much of the board fits on
// screen and whose POSITION says where in the board you are.
//
// It exists because a small window could look like an empty board. The grid
// scrolls to follow focus (ensureFocusVisible, board.go) and shows
// GridHeight/tileHeight rows — which is TWO at the minimum supported 80x24 and
// ONE below about 23 rows tall. A user with four sandboxes on a short terminal
// saw one tile, or, having arrowed onto the ghost cell, saw nothing but "press
// enter to add a VM" — a board that reads as "you have no VMs" when in fact it
// has four, with no signal anywhere that the other three were one keypress away.
// Nothing on the board disclosed the scroll position: not the header, not the
// grid, not the footer.
//
// It is spent in COLUMNS, not rows. Rows on this board are contested — the
// header bands, the messages strip and the help bar already negotiate for them
// in classifyWithHeaderBands, and the shedding order guarantees the losers go
// first on short terminals. A scroll hint that cost a row would therefore be
// shed on exactly the terminals where the grid scrolls hardest and the hint
// matters most. Two columns, by contrast, cost a tile column at only two widths
// in every 42 (see gutterFor) and nothing at all elsewhere.

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// The gutter's glyphs. Different SHAPES, not just different colours, so the
// thumb is still distinguishable from the track under NO_COLOR, on a monochrome
// terminal, and in the ansi.Strip'd goldens — the same rule the focus ring
// follows by switching border sets rather than only its colour.
const (
	scrollThumbRune = "█"
	scrollTrackRune = "░"
)

// scrollThumb places the thumb in a track of trackLines cells, for a viewport
// showing `visible` of `total` tile rows starting at row `first`. It returns the
// thumb's first cell and its length; a size of 0 means DO NOT DRAW — there is
// nothing off screen, so there is nothing to say.
//
// Two properties matter more than the exact arithmetic, and the tests pin both:
//
//   - The thumb touches the top of the track when and only when first==0, and
//     the bottom when and only when the grid is scrolled as far as it goes.
//     A bar that stopped a cell short of the end would say "there is more below"
//     at the bottom of the board, which is the exact lie this file exists to fix.
//   - The thumb is never the whole track while the grid scrolls. It is capped at
//     trackLines-1 so at least one cell of track always shows behind it: a full
//     track and an absent bar look identical, and the first is supposed to mean
//     "there is more" while the second means "there is not".
func scrollThumb(trackLines, total, visible, first int) (start, size int) {
	if trackLines < 1 || visible < 1 || total <= visible {
		return 0, 0
	}
	// Proportion first: how much of the board fits on screen, rounded to cells.
	size = roundDiv(trackLines*visible, total)
	if size < 1 {
		size = 1
	}
	if size > trackLines-1 {
		size = trackLines - 1
	}
	// Then position, as a fraction of the SCROLLABLE RANGE (total-visible) mapped
	// onto the range the thumb can travel (trackLines-size) — not as a fraction of
	// `total`, which would leave the thumb short of the bottom at full scroll.
	span := total - visible
	start = roundDiv((trackLines-size)*first, span)
	if start < 0 {
		start = 0
	}
	if start > trackLines-size {
		start = trackLines - size
	}
	return start, size
}

// roundDiv is a/b rounded to nearest, for non-negative a and positive b. The
// scrollbar rounds rather than truncating so a thumb that is nearly a full cell
// does not read as half of one.
func roundDiv(a, b int) int {
	if b <= 0 {
		return 0
	}
	return (a + b/2) / b
}

// scrollGutterLines renders the gutter as exactly `lines` rows of `width` cells:
// one blank separator column, then the track cell. When there is nothing off
// screen it renders BLANK rather than an empty track — the gutter's columns are
// reserved unconditionally (layoutMode.GutterWidth), but a track drawn on a board
// that fits is chrome claiming a scroll that does not exist.
func scrollGutterLines(lines, width, total, visible, first int) []string {
	if lines < 1 || width < 1 {
		return nil
	}
	blank := strings.Repeat(" ", width)
	out := make([]string, lines)
	start, size := scrollThumb(lines, total, visible, first)
	if size == 0 {
		for i := range out {
			out[i] = blank
		}
		return out
	}
	pad := strings.Repeat(" ", width-1)
	for i := range out {
		if i >= start && i < start+size {
			out[i] = pad + scrollThumbStyle.Render(scrollThumbRune)
		} else {
			out[i] = pad + scrollTrackStyle.Render(scrollTrackRune)
		}
	}
	return out
}

// attachScrollGutter joins the gutter to the right of an already-rendered,
// already-height-clipped grid block.
//
// Every line is padded to `tilesWidth` first, because the last tile row may be
// SHORT (a board whose VM count is not a multiple of the column count): without
// the pad, the track would step left on that row and read as a ragged tile
// border rather than as a straight bar. lipgloss is not asked to do the join —
// it would measure the block itself, and the block's own lines already carry
// styling whose width only ansi.StringWidth reads correctly.
func attachScrollGutter(block string, tilesWidth, gutterWidth, total, visible, first int) string {
	if gutterWidth < 1 || block == "" {
		return block
	}
	lines := strings.Split(block, "\n")
	gutter := scrollGutterLines(len(lines), gutterWidth, total, visible, first)
	if gutter == nil {
		return block
	}
	for i, ln := range lines {
		pad := tilesWidth - ansi.StringWidth(ln)
		if pad < 0 {
			pad = 0
		}
		lines[i] = ln + strings.Repeat(" ", pad) + gutter[i]
	}
	return strings.Join(lines, "\n")
}
