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
