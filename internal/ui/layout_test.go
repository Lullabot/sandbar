package ui

import "testing"

// TestClassifySizeSweep is the regression net for every hardcoded per-screen
// sizing offset this task deletes (the old viewport/table/secrets-editor
// terminal-size subtractions and contentWidth's floor-20). classify is a pure
// function of two ints, so the whole responsive contract — "there is no
// terminal size at which sand shows a too-small wall" — is checkable without
// a terminal at all.
func TestClassifySizeSweep(t *testing.T) {
	sizes := []struct{ w, h int }{
		{80, 24},
		{100, 30},
		{120, 40},
		{200, 60},
		{40, 10}, // pathological small
		{20, 5},  // pathological tiny
	}

	for _, sz := range sizes {
		lm := classify(sz.w, sz.h)

		if lm.Columns <= 0 {
			t.Errorf("classify(%d,%d).Columns = %d, want > 0", sz.w, sz.h, lm.Columns)
		}
		if lm.TileWidth <= 0 {
			t.Errorf("classify(%d,%d).TileWidth = %d, want > 0", sz.w, sz.h, lm.TileWidth)
		}
		if lm.ContentWidth <= 0 {
			t.Errorf("classify(%d,%d).ContentWidth = %d, want > 0", sz.w, sz.h, lm.ContentWidth)
		}
		if lm.HeaderHeight <= 0 {
			t.Errorf("classify(%d,%d).HeaderHeight = %d, want > 0", sz.w, sz.h, lm.HeaderHeight)
		}
		if lm.GridHeight <= 0 {
			t.Errorf("classify(%d,%d).GridHeight = %d, want > 0", sz.w, sz.h, lm.GridHeight)
		}
		if lm.FooterHeight <= 0 {
			t.Errorf("classify(%d,%d).FooterHeight = %d, want > 0", sz.w, sz.h, lm.FooterHeight)
		}
		if lm.MessagesHeight < 0 {
			t.Errorf("classify(%d,%d).MessagesHeight = %d, want >= 0 (0 = the strip is shed)", sz.w, sz.h, lm.MessagesHeight)
		}

		// Every pane's height budget must sum within the terminal height — no
		// pane may claim more room than exists.
		sum := lm.HeaderHeight + lm.MessagesHeight + lm.GridHeight + lm.FooterHeight
		if sum > sz.h {
			t.Errorf("classify(%d,%d) pane heights sum to %d, exceeding terminal height %d", sz.w, sz.h, sum, sz.h)
		}

		// Tile width must fit within the terminal — never wider than what's there.
		if lm.TileWidth > sz.w {
			t.Errorf("classify(%d,%d).TileWidth = %d, exceeds terminal width %d", sz.w, sz.h, lm.TileWidth, sz.w)
		}
	}
}

// At 80x24 — the classic terminal default — the board must still be a board:
// a single tile column (not deleted, not a fallback list), navigable.
func TestClassify80x24YieldsSingleColumn(t *testing.T) {
	lm := classify(80, 24)
	if lm.Columns != 1 {
		t.Fatalf("classify(80,24).Columns = %d, want 1 (a still-navigable single-column board)", lm.Columns)
	}
	if lm.TileWidth <= 0 || lm.GridHeight <= 0 {
		t.Fatalf("classify(80,24) must be renderable: TileWidth=%d GridHeight=%d", lm.TileWidth, lm.GridHeight)
	}
}

// As width grows, more tile columns fit — the shedding order runs in
// reverse: single column -> multi-column grid.
func TestClassifyWidthGrowsColumns(t *testing.T) {
	if classify(200, 60).Columns <= classify(80, 24).Columns {
		t.Fatalf("a much wider terminal should yield at least as many tile columns")
	}
}

// classify never returns an error or a "too small" sentinel — the contract is
// that every size is renderable, however cramped.
func TestClassifyNoSizeIsUnrenderable(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{0, 0}, {1, 1}, {5, 3}, {10000, 10000}} {
		lm := classify(sz.w, sz.h)
		if lm.Columns <= 0 || lm.TileWidth <= 0 || lm.GridHeight <= 0 || lm.FooterHeight <= 0 || lm.HeaderHeight <= 0 {
			t.Errorf("classify(%d,%d) is not renderable: %+v", sz.w, sz.h, lm)
		}
	}
}

// classify's own zero-bands behaviour must be untouched by the per-profile
// header bands: classify(w,h) and classifyWithFooter thread a headerBands of 0
// through classifyWithHeaderBands, so every caller that does not ask for bands
// keeps getting exactly the base header height it always did.
func TestClassifyRequestsNoHeaderBands(t *testing.T) {
	lm := classify(120, 40)
	if lm.HeaderBandLines != 0 {
		t.Fatalf("classify(120,40).HeaderBandLines = %d, want 0 (classify never asks for bands)", lm.HeaderBandLines)
	}
}

// A tall, wide terminal has room to grant every requested band in full.
func TestClassifyWithHeaderBandsGrantsWhatFits(t *testing.T) {
	lm := classifyWithHeaderBands(120, 40, 1, 3)
	if lm.HeaderBandLines != 3 {
		t.Fatalf("HeaderBandLines = %d, want 3 granted in full on a roomy terminal", lm.HeaderBandLines)
	}
	if lm.GridHeight <= 0 || lm.FooterHeight <= 0 {
		t.Fatalf("classifyWithHeaderBands(120,40,1,3) must stay renderable: %+v", lm)
	}
	sum := lm.HeaderHeight + lm.MessagesHeight + lm.GridHeight + lm.FooterHeight
	if sum > 40-appPaddingV {
		t.Fatalf("pane heights sum to %d, exceeding the content height", sum)
	}
}

// 80x24 — the narrowest supported terminal — must never break: however many
// bands a large fleet wants, the grid and footer stay renderable, and
// HeaderBandLines never claims more than the layout actually paid for.
func TestClassifyWithHeaderBandsNeverBreaks80x24(t *testing.T) {
	for _, want := range []int{0, 1, 2, 5, 20} {
		lm := classifyWithHeaderBands(80, 24, 1, want)
		if lm.GridHeight < minBudget {
			t.Fatalf("headerBands=%d: GridHeight = %d, want >= %d (the grid must never break)", want, lm.GridHeight, minBudget)
		}
		if lm.FooterHeight <= 0 {
			t.Fatalf("headerBands=%d: FooterHeight = %d, want > 0", want, lm.FooterHeight)
		}
		if lm.HeaderBandLines > want {
			t.Fatalf("headerBands=%d: HeaderBandLines = %d, granted more than requested", want, lm.HeaderBandLines)
		}
		sum := lm.HeaderHeight + lm.MessagesHeight + lm.GridHeight + lm.FooterHeight
		if sum > 24-appPaddingV {
			t.Fatalf("headerBands=%d: pane heights sum to %d, exceeding the content height", want, sum)
		}
	}
}

// Bands are the FIRST thing shed when a short terminal cannot afford
// everything — before the messages strip, before the full header — because a
// summarized "+K more" row can say something useful in one line where losing
// the messages strip or the title row cannot.
func TestClassifyWithHeaderBandsShedBeforeMessagesAndHeader(t *testing.T) {
	// A terminal too short to show every requested band, but tall enough for
	// the messages strip and the full header (a few rows of slack above the
	// bare minimum), must shed bands FIRST — the messages strip and header
	// survive.
	const h = messagesMinHeight + 5
	lm := classifyWithHeaderBands(120, h, 1, 100)
	if lm.HeaderBandLines >= 100 {
		t.Fatalf("100 requested bands at height %d should not all be granted", h)
	}
	if lm.MessagesHeight == 0 {
		t.Fatalf("bands must be shed before the messages strip, but the strip was shed at height %d", h)
	}
	if !lm.HeaderFull {
		t.Fatalf("bands must be shed before the full header, but the header compacted at height %d", h)
	}
}
