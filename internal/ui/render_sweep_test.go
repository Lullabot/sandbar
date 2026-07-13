package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/vm"

	"github.com/charmbracelet/x/ansi"
)

// GOLDENS PIN THE RESPONSIVE RANGE; THIS SWEEP IS ITS COMPLEMENT: it proves
// there is no terminal size — however cramped, however vast — at which the
// board fails to render at all, AND that what it renders FITS. layout_test.go
// already pins the first half at the pure classify(w, h) level (task 03:
// TestClassifyNoSizeIsUnrenderable); this asserts it end to end, through the
// REAL board renderer, over a fleet that actually has tiles to draw and a hidden
// VM to disclose — the surfaces this task added on top of classify's budgets.
//
// THE FIT ASSERTION IS THE ONE THAT MATTERS, and its absence is what let the
// board ship two rows taller than classify budgeted for: at 80x24 the rendered
// view came to 26 lines, so bubbletea's alt-screen clipped rows 25–26 and the
// ENTIRE help bar was invisible at the one size the plan calls out as must-work.
// "It rendered something, and didn't say 'too small'" stayed true the whole time.
// Assert the extent, or the footer walks off the bottom again.
func TestBoardRendersAtEverySizeInTheSweep(t *testing.T) {
	pinHostCapacity(t, 16<<30, 100<<30)
	base := newTestModel(t)
	if err := base.reg.Add(vm.CreateConfig{Name: "web", BaseName: "claude-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	loaded, _ := base.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "web", Status: "Running"},
		{Name: "claude-base", Status: "Stopped"}, // hidden: a base image
	}})
	base = loaded.(model)

	// The board's chrome is not constant, and every optional line of it is a row
	// the budget must already have paid for. Sweep all of them: quiet, with the
	// activity log filled in (long lines, to catch a footer pushed off the bottom
	// AND a status line running off the right edge), with a confirm overlay up —
	// the one line that may never be clipped away, since the user has to answer
	// it — and with the search indicator showing.
	states := []struct {
		name string
		with func(model) model
	}{
		{"quiet", func(m model) model { return m }},
		{"messages", func(m model) model {
			for i := 0; i < 5; i++ {
				m.logMsg(fmt.Sprintf("stopping web… (%d) — with a long tail of explanation that runs well past the right edge", i))
			}
			return m
		}},
		{"confirm", func(m model) model {
			m.logMsg("stop all: three sand VMs are running")
			m.confirm = &confirmState{prompt: "Stop 3 running sand VMs (web, api, db and 1 more)?"}
			return m
		}},
		{"searching", func(m model) model {
			m.logMsg("stopping web…")
			m.searching = true
			m.searchQuery = "we"
			return m
		}},
	}

	widths := []int{1, 5, 10, 20, 40, 60, 80, 100, 120, 160, 200, 400}
	heights := []int{1, 3, 5, 7, 8, 10, 15, 20, 24, 26, 27, 30, 40, 60, 100}

	for _, st := range states {
		for _, w := range widths {
			for _, h := range heights {
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("boardView panicked at %dx%d (%s): %v", w, h, st.name, r)
						}
					}()
					mm := st.with(resized(base, w, h))
					view := ansi.Strip(mm.boardView())
					if view == "" {
						t.Fatalf("boardView() at %dx%d (%s) rendered nothing", w, h, st.name)
					}
					if strings.Contains(strings.ToLower(view), "too small") {
						t.Fatalf("boardView() at %dx%d (%s) hit a \"terminal too small\" wall:\n%s", w, h, st.name, view)
					}

					lines, cols := viewExtent(view)
					if h >= boardMinFitHeight && lines > h {
						t.Errorf("boardView() at %dx%d (%s) rendered %d lines: the terminal holds %d, so its last %d row(s) — the help bar — are clipped away:\n%s",
							w, h, st.name, lines, h, lines-h, view)
					}
					if w >= boardMinFitWidth && cols > w {
						t.Errorf("boardView() at %dx%d (%s) rendered a %d-cell line into a %d-cell terminal:\n%s",
							w, h, st.name, cols, w, view)
					}
				}()
			}
		}
	}
}

// boardMinFitHeight/boardMinFitWidth are the sizes below which the board cannot
// fit BY ARITHMETIC rather than by bug, which is why the assertions above start
// there — and why they say so out loud instead of quietly skipping.
//
// Height: appStyle's two padding rows, plus the chrome the board may never
// shed — a compact header line, one (clipped) grid row, and the footer band,
// which carries the pending-confirmation line the user has to be able to answer.
// Seven rows, and no budget arithmetic fits seven rows into five. classify
// already concedes exactly this: below that floor it stops subtracting and
// floors every band at minBudget rather than raising a "terminal too small"
// wall, and the terminal clips what will not fit. At and above it the fit is
// guaranteed by construction — classify's bands sum to exactly ContentHeight,
// and boardView spends no more rows than each band was budgeted.
//
// Width: appStyle's four padding columns plus classify's one-column minimum. A
// terminal one cell wide cannot hold four cells of padding.
const (
	boardMinFitHeight = appPaddingV + headerHeightCompact + minBudget + footerBandHeight
	boardMinFitWidth  = appPaddingH + minBudget
)

// viewExtent measures a rendered (ANSI-stripped) screen: how many rows it spends,
// and how wide its widest one is in DISPLAY CELLS — not bytes, since the tiles'
// box-drawing borders, the header's "·" and the gauges' blocks are all multi-byte.
func viewExtent(view string) (lines, width int) {
	rows := strings.Split(view, "\n")
	// A view ending in a newline is not a row taller for it.
	if n := len(rows); n > 0 && rows[n-1] == "" {
		rows = rows[:n-1]
	}
	for _, r := range rows {
		if w := ansi.StringWidth(r); w > width {
			width = w
		}
	}
	return len(rows), width
}

// The VM (detail) screen gets the same sweep: its footer is the one that
// used to overflow the terminal (gap (a) task 08 handed task 09), so it is
// worth its own pass rather than trusting the board's sweep to cover it.
func TestDetailViewRendersAtEverySizeInTheSweep(t *testing.T) {
	m := newTestModel(t)
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{{Name: "claude", Status: "Running", CPUs: 2}}})
	m = loaded.(model)
	m.view = viewDetail
	m.detail, _ = m.lookupVM("claude")

	for _, w := range []int{1, 10, 20, 40, 80, 100, 160, 400} {
		for _, h := range []int{1, 5, 10, 24, 30, 60} {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("detailView panicked at %dx%d: %v", w, h, r)
					}
				}()
				mm := resized(m, w, h)
				view := ansi.Strip(mm.detailView())
				if view == "" {
					t.Fatalf("detailView() at %dx%d rendered nothing", w, h)
				}
				if strings.Contains(strings.ToLower(view), "too small") {
					t.Fatalf("detailView() at %dx%d hit a \"terminal too small\" wall:\n%s", w, h, view)
				}
			}()
		}
	}
}
