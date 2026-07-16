package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/browse"
	"github.com/lullabot/sandbar/internal/registry"
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
	if err := base.reg.Add(vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	loaded, _ := base.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "web", Status: "Running"},
		{Name: "sandbar-base", Status: "Stopped"}, // hidden: a base image
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

// EVERY footer is clipped to the terminal, on every screen. applySize calls
// help.SetWidth(0) on purpose — bubbles' own truncation renders one item PAST the
// budget rather than stopping — so a screen that calls help.ShortHelpView directly
// is rendering an entirely UNCLIPPED help bar. Four of the five did.
//
// footerView is the one honest clip. This drives each screen at a width far too
// narrow for its verbs and asserts nothing overflows.
func TestEveryScreensFooterIsClippedToTheTerminal(t *testing.T) {
	const w, h = 40, 24 // far too narrow for any of these help bars

	m := newTestModel(t)
	m = resized(m, w, h)
	m = putOnBoard(t, m, vm.VM{Name: "web", Status: "Running", CPUs: 2})

	screens := map[string]func(model) string{
		"board":   func(m model) string { return m.boardView() },
		"form":    func(m model) string { return m.formView() },
		"secrets": func(m model) string { return m.secretsView() },
		"dest":    func(m model) string { return m.destView() },
	}
	models := map[string]model{"board": m}

	form := m
	form.openForm()
	models["form"] = form

	sec := openSecretsViaKey(t, m, "web", "Running")
	models["secrets"] = sec

	dest := m
	dest.view = viewDest
	dest.transferVM = "web"
	dest.transferScope = registry.LocalScope
	dest.transferSrc = "/home/u/file.txt"
	dest.dest, _ = browse.NewDestInput("Destination dir: ", "/tmp/host-dst", nil)
	models["dest"] = dest

	// Only the HELP BAR is under test. help.ShortHelpView separates items with "•",
	// which nothing else on these screens renders, so that identifies the footer
	// without depending on which verbs happen to be eligible.
	for name, render := range screens {
		saw := false
		for _, line := range strings.Split(ansi.Strip(render(models[name])), "\n") {
			if !strings.Contains(line, "•") {
				continue
			}
			saw = true
			// Trailing spaces are lipgloss padding every line in the block out to the
			// widest one; they are not the footer's own text. Measure the text.
			line = strings.TrimRight(line, " ")
			if got := ansi.StringWidth(line); got > w {
				t.Errorf("%s screen: the help bar overflows the %d-column terminal (%d cells): %q", name, w, got, line)
			}
		}
		if !saw {
			t.Errorf("%s screen rendered no help bar — the test proves nothing about it", name)
		}
	}
}

// THE FOOTER WRAPS RATHER THAN TRUNCATING. It used to be one clipped line, so a
// board offering eight verbs simply ended in "…" and the rest were unfindable —
// which defeats the point of deriving the footer from the command registry at all.
// The rows come out of the grid, which has them to spare at 1-3 VMs.
func TestFooterWrapsInsteadOfTruncating(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 80, 24) // the plan's must-work size, and too narrow for one line
	m = putOnBoard(t, m, vm.VM{Name: "web", Status: "Running", CPUs: 2})

	lines := m.footerLines(m.boardHelp())
	if len(lines) < 2 {
		t.Fatalf("at 80 columns the board's verbs need more than one line, got %d", len(lines))
	}
	joined := ansi.Strip(strings.Join(lines, " "))
	// The LAST verb survives. It was the first casualty of truncation.
	for _, want := range []string{"q quit", "? keys", "e secrets", "g download"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the wrapped footer dropped %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "…") {
		t.Errorf("nothing should be elided at 80 columns:\n%s", joined)
	}
	// And every line still fits.
	for _, l := range lines {
		if got := ansi.StringWidth(ansi.Strip(l)); got > m.layout.ContentWidth {
			t.Errorf("a wrapped line overflows the content width (%d > %d): %q", got, m.layout.ContentWidth, l)
		}
	}
}

// The build is on the header's title row, right-aligned. It is the one question a
// bug report always needs.
func TestHeaderShowsTheBuildRightAligned(t *testing.T) {
	m := newTestModel(t)
	pinVersion(t, "v9.9.9-dirty")
	m = resized(m, 100, 30)
	m = putOnBoard(t, m, vm.VM{Name: "web", Status: "Running"})

	title := ansi.Strip(strings.Split(m.headerView(), "\n")[0])
	if !strings.Contains(title, "sand") || !strings.Contains(title, "v9.9.9-dirty") {
		t.Fatalf("the title row should carry the app name and the build, got %q", title)
	}
	if !strings.HasSuffix(strings.TrimRight(title, " "), "v9.9.9-dirty") {
		t.Fatalf("the build should be right-aligned, got %q", title)
	}
	// A terminal too narrow for both drops the version rather than truncating the
	// hash — half a commit hash is worse than none.
	narrow := resized(m, 12, 30)
	if got := ansi.Strip(strings.Split(narrow.headerView(), "\n")[0]); strings.Contains(got, "v9.9") {
		t.Fatalf("a narrow header should drop the build, not squeeze it: %q", got)
	}
}
