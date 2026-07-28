package ui

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"

	"github.com/charmbracelet/x/ansi"
)

// A board that FITS says nothing. The gutter's columns are reserved
// unconditionally (so tiles never re-flow when a create pushes the board past
// the viewport), but drawing a track on a board with nothing off screen is
// chrome claiming a scroll that does not exist — and the whole point of the
// affordance is that its presence means something.
func TestScrollThumbSaysNothingWhenEverythingFits(t *testing.T) {
	for _, c := range []struct{ track, total, visible, first int }{
		{16, 2, 2, 0},
		{16, 1, 2, 0},
		{16, 0, 2, 0},
		{0, 9, 2, 0},
		{16, 9, 0, 0},
	} {
		if _, size := scrollThumb(c.track, c.total, c.visible, c.first); size != 0 {
			t.Errorf("scrollThumb(%d,%d,%d,%d) drew a thumb of %d, want none",
				c.track, c.total, c.visible, c.first, size)
		}
	}
}

// THE TWO ENDS ARE THE WHOLE CONTRACT. A bar that stopped a cell short of the
// bottom would say "there is more below" while the user is looking at the last
// tile on the board — the same lie, one step further down, that the affordance
// exists to remove. Likewise a bar not flush with the top at scrollRow 0 would
// claim there is something above the first tile.
func TestScrollThumbReachesBothEnds(t *testing.T) {
	for _, c := range []struct{ track, total, visible int }{
		{16, 3, 2}, {16, 9, 2}, {8, 7, 1}, {24, 40, 3}, {5, 100, 1},
	} {
		start, size := scrollThumb(c.track, c.total, c.visible, 0)
		if size == 0 {
			t.Fatalf("precondition: %+v must be scrollable", c)
		}
		if start != 0 {
			t.Errorf("%+v at the top: thumb starts at %d, want 0", c, start)
		}
		// The bottom is the last scroll position ensureFocusVisible can produce.
		bottom := c.total - c.visible
		start, size = scrollThumb(c.track, c.total, c.visible, bottom)
		if start+size != c.track {
			t.Errorf("%+v at the bottom (first=%d): thumb ends at %d, want the track's end %d",
				c, bottom, start+size, c.track)
		}
	}
}

// The thumb never fills the track while the grid scrolls: a full track and an
// absent bar look identical, and they are supposed to mean opposite things.
// It is also never empty — a zero-length thumb on a scrollable board is a track
// with no position in it.
func TestScrollThumbNeverFillsOrVanishes(t *testing.T) {
	for track := 2; track <= 40; track++ {
		for total := 2; total <= 60; total++ {
			for visible := 1; visible < total; visible++ {
				for _, first := range []int{0, (total - visible) / 2, total - visible} {
					start, size := scrollThumb(track, total, visible, first)
					if size < 1 || size > track-1 {
						t.Fatalf("scrollThumb(%d,%d,%d,%d) size = %d, want 1..%d",
							track, total, visible, first, size, track-1)
					}
					if start < 0 || start+size > track {
						t.Fatalf("scrollThumb(%d,%d,%d,%d) = (%d,%d) falls outside the track",
							track, total, visible, first, start, size)
					}
				}
			}
		}
	}
}

// Scrolling down never moves the thumb up. Monotonicity is what makes the bar
// readable as a position rather than as decoration.
func TestScrollThumbIsMonotonic(t *testing.T) {
	const track, total, visible = 16, 12, 2
	prev := -1
	for first := 0; first <= total-visible; first++ {
		start, size := scrollThumb(track, total, visible, first)
		if size == 0 {
			t.Fatalf("precondition: the board must be scrollable")
		}
		if start < prev {
			t.Fatalf("first=%d: thumb moved UP to %d from %d", first, start, prev)
		}
		prev = start
	}
}

// The gutter is exactly as tall as the block it is joined to and exactly as
// wide as the columns classify reserved — no more, or the grid overhangs the
// terminal; no fewer, or the track steps out of its column.
func TestScrollGutterLinesAreExactlySized(t *testing.T) {
	for _, lines := range []int{1, 8, 16, 17} {
		got := scrollGutterLines(lines, scrollGutterWidth, 9, 2, 0)
		if len(got) != lines {
			t.Fatalf("scrollGutterLines(%d,…) returned %d lines", lines, len(got))
		}
		for i, ln := range got {
			if w := ansi.StringWidth(ansi.Strip(ln)); w != scrollGutterWidth {
				t.Errorf("line %d is %d cells wide, want %d", i, w, scrollGutterWidth)
			}
		}
	}
}

// THE GLYPHS CARRY THE MEANING, NOT THE COLOUR. Under NO_COLOR, on a monochrome
// terminal, and in the ansi.Strip'd goldens, the thumb must still be
// distinguishable from the track behind it — the same rule the focus ring
// follows by switching border SETS rather than only its colour.
func TestScrollGutterSurvivesColourStripping(t *testing.T) {
	stripped := ansi.Strip(strings.Join(scrollGutterLines(16, scrollGutterWidth, 9, 2, 0), "\n"))
	if !strings.Contains(stripped, scrollThumbRune) || !strings.Contains(stripped, scrollTrackRune) {
		t.Fatalf("the thumb and the track must both survive ansi.Strip, got:\n%q", stripped)
	}
}

// The board-level claim, through the REAL renderer: a grid with more tiles than
// fit shows the bar, and the same grid with room to spare does not.
//
// The size is the one the affordance was built for — a short terminal where
// visibleTileRows is 1 and the board reads as "you have no VMs" without it.
func TestGridViewShowsTheScrollbarOnlyWhenThereIsMore(t *testing.T) {
	pinHostCapacity(t, 16<<30, 100<<30)

	small := newTestModel(t)
	small = resized(small, 80, 22)
	small = loadManaged(t, small,
		vm.VM{Name: "api", Status: "Running"}, vm.VM{Name: "db", Status: "Running"},
		vm.VM{Name: "web", Status: "Running"}, vm.VM{Name: "x1", Status: "Running"},
	)
	if small.visibleTileRows() != 1 {
		t.Fatalf("precondition: 80x22 should show one tile row, got %d", small.visibleTileRows())
	}
	grid := ansi.Strip(small.gridView())
	if !strings.Contains(grid, scrollThumbRune) || !strings.Contains(grid, scrollTrackRune) {
		t.Fatalf("a board with four VMs in a one-row viewport must show the scroll bar, got:\n%s", grid)
	}
	// Only the tile it can actually see — which is precisely why the bar has to
	// be there to say the other three exist.
	if strings.Contains(grid, "db") {
		t.Fatalf("precondition: only the first tile should be on screen, got:\n%s", grid)
	}

	// A tall terminal with the same fleet has nothing off screen: no track.
	big := newTestModel(t)
	big = resized(big, 80, 60)
	big = loadManaged(t, big,
		vm.VM{Name: "api", Status: "Running"}, vm.VM{Name: "db", Status: "Running"},
	)
	grid = ansi.Strip(big.gridView())
	if strings.Contains(grid, scrollThumbRune) {
		t.Fatalf("a board that fits must not draw a scroll bar, got:\n%s", grid)
	}
}

// The bar tracks the SCROLL, not just the fleet size: arrowing to the bottom of
// the board must move the thumb to the bottom of the track. This is the
// end-to-end form of TestScrollThumbReachesBothEnds — through moveFocus and
// ensureFocusVisible, the only things that move scrollRow.
func TestGridViewScrollbarFollowsTheFocusRing(t *testing.T) {
	pinHostCapacity(t, 16<<30, 100<<30)
	m := newTestModel(t)
	m = resized(m, 80, 22)
	m = loadManaged(t, m,
		vm.VM{Name: "api", Status: "Running"}, vm.VM{Name: "db", Status: "Running"},
		vm.VM{Name: "web", Status: "Running"}, vm.VM{Name: "x1", Status: "Running"},
	)

	thumbRows := func(s string) (first, last int) {
		first, last = -1, -1
		for i, ln := range strings.Split(ansi.Strip(s), "\n") {
			if strings.Contains(ln, scrollThumbRune) {
				if first < 0 {
					first = i
				}
				last = i
			}
		}
		return first, last
	}

	top, _ := thumbRows(m.gridView())
	if top != 0 {
		t.Fatalf("at the top of the board the thumb should start on the first row, got row %d", top)
	}

	// Walk the ring to the last cell — the ghost, one past the last VM.
	for i := 0; i < m.gridCells(); i++ {
		m.moveFocus(0, 1)
	}
	lines := len(strings.Split(ansi.Strip(m.gridView()), "\n"))
	_, bottom := thumbRows(m.gridView())
	if bottom != lines-1 {
		t.Fatalf("at the bottom of the board the thumb should end on the last row (%d), got %d", lines-1, bottom)
	}
}
