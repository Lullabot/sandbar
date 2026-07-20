package ui

// badge_test.go covers task 04's unlanded-work badge two ways, mirroring the
// split badge.go itself keeps: computeCheckoutBadge is a pure mapping and
// gets ordinary table-driven unit tests (no rendering involved at all); the
// text renderCheckoutBadge produces is pinned with golden.RequireEqual,
// exactly the mechanism header_bands_golden_test.go uses (regenerate with
// `go test ./internal/ui/ -run TestCheckoutBadgeRender -update`). How that
// text reaches a tile is tile.go's business, so the footer-layout tests below
// drive it through real renderTile output rather than a hand-built fixture.

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/checkouts"
	"github.com/lullabot/sandbar/internal/vm"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
)

func TestComputeCheckoutBadge(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-1 * time.Minute)

	cases := []struct {
		name    string
		vc      checkouts.VMCheckouts
		known   bool
		running bool
		want    checkoutBadge
	}{
		{
			name:    "actionable: a pushed branch",
			vc:      checkouts.VMCheckouts{SweptAt: fresh, Checkouts: []checkouts.Checkout{{PushState: checkouts.PushStatePushed}}},
			known:   true,
			running: true,
			want:    checkoutBadge{Actionable: true},
		},
		{
			name:    "at-risk: unpushed commits",
			vc:      checkouts.VMCheckouts{SweptAt: fresh, Checkouts: []checkouts.Checkout{{PushState: checkouts.PushStateUnpushed, Ahead: 3}}},
			known:   true,
			running: true,
			want:    checkoutBadge{AtRisk: true, Ahead: 3},
		},
		{
			name:    "at-risk: never pushed, no honest ahead count",
			vc:      checkouts.VMCheckouts{SweptAt: fresh, Checkouts: []checkouts.Checkout{{PushState: checkouts.PushStateNever}}},
			known:   true,
			running: true,
			want:    checkoutBadge{AtRisk: true},
		},
		{
			name:    "at-risk: dirty working tree on an otherwise-pushed checkout",
			vc:      checkouts.VMCheckouts{SweptAt: fresh, Checkouts: []checkouts.Checkout{{PushState: checkouts.PushStatePushed, Dirty: 2}}},
			known:   true,
			running: true,
			want:    checkoutBadge{Actionable: true, AtRisk: true, Dirty: true},
		},
		{
			name: "both at once: one pushed checkout, one at-risk checkout",
			vc: checkouts.VMCheckouts{SweptAt: fresh, Checkouts: []checkouts.Checkout{
				{PushState: checkouts.PushStatePushed},
				{PushState: checkouts.PushStateUnpushed, Ahead: 1, Dirty: 1},
			}},
			known:   true,
			running: true,
			want:    checkoutBadge{Actionable: true, AtRisk: true, Ahead: 1, Dirty: true},
		},
		{
			name:    "empty: swept, nothing found — nothing to show, not stale",
			vc:      checkouts.VMCheckouts{SweptAt: fresh},
			known:   true,
			running: true,
			want:    checkoutBadge{},
		},
		{
			name:    "empty: no registry entry at all (never swept)",
			vc:      checkouts.VMCheckouts{},
			known:   false,
			running: true,
			want:    checkoutBadge{Stale: true},
		},
		{
			name:    "stale: sweep older than the freshness window",
			vc:      checkouts.VMCheckouts{SweptAt: now.Add(-2 * time.Hour), Checkouts: []checkouts.Checkout{{PushState: checkouts.PushStatePushed}}},
			known:   true,
			running: true,
			want:    checkoutBadge{Stale: true},
		},
		{
			name:    "stale: VM is stopped, however fresh SweptAt looks",
			vc:      checkouts.VMCheckouts{SweptAt: fresh, Checkouts: []checkouts.Checkout{{PushState: checkouts.PushStatePushed}}},
			known:   true,
			running: false,
			want:    checkoutBadge{Stale: true},
		},
		{
			name:    "stale: SweptAt zero value, even if known is somehow true",
			vc:      checkouts.VMCheckouts{Checkouts: []checkouts.Checkout{{PushState: checkouts.PushStatePushed}}},
			known:   true,
			running: true,
			want:    checkoutBadge{Stale: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeCheckoutBadge(tc.vc, tc.known, tc.running, now)
			if got != tc.want {
				t.Errorf("computeCheckoutBadge() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestCheckoutBadgeRender pins renderCheckoutBadge's exact text (colour
// escapes included) for the five states the task calls out: actionable,
// at-risk, both-at-once, empty (nothing), and stale.
func TestCheckoutBadgeRender(t *testing.T) {
	cases := map[string]checkoutBadge{
		"Actionable":  {Actionable: true},
		"AtRisk":      {AtRisk: true, Ahead: 4},
		"AtRiskDirty": {AtRisk: true, Ahead: 0, Dirty: true},
		"BothAtOnce":  {Actionable: true, AtRisk: true, Ahead: 2, Dirty: true},
		"Empty":       {},
		"Stale":       {Stale: true},
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			golden.RequireEqual(t, []byte(renderCheckoutBadge(b)))
		})
	}
}

// TestCheckoutBadgeRenderEmptyAndStaleAreBothNothing locks in that "empty"
// and "stale" are indistinguishable in the RENDERED output — both suppress
// the badge entirely, per the acceptance criterion that a stale/empty
// registry never fabricates state. The pure computeCheckoutBadge tests above
// are what actually distinguish the two conditions.
func TestCheckoutBadgeRenderEmptyAndStaleAreBothNothing(t *testing.T) {
	if got := renderCheckoutBadge(checkoutBadge{}); got != "" {
		t.Errorf("empty verdict rendered %q, want \"\"", got)
	}
	if got := renderCheckoutBadge(checkoutBadge{Stale: true, Actionable: true, AtRisk: true, Ahead: 9, Dirty: true}); got != "" {
		t.Errorf("stale verdict rendered %q, want \"\" (stale must suppress everything else)", got)
	}
}

// TestTileFooterBadgeRendersRightAligned exercises the badge on its real
// path — through renderTile, against the actual renderer rather than a
// hand-built fixture — and pins the two properties that matter: the tile stays
// a perfect rectangle, and the badge sits on the RIGHT margin of the footer.
//
// It deliberately uses the STYLED badge renderCheckoutBadge actually emits,
// not a bare string. The predecessor of this test spliced a plain "⚠
// actionable" into the rendered tile and so never exercised an ANSI escape at
// all; the splice it covered measured width byte-wise and would cut an escape
// sequence in half, counting the escape's own bytes as visible cells and
// pulling the footer's right border ~9 cells inside the tile. A styled badge
// is the regression.
func TestTileFooterBadgeRendersRightAligned(t *testing.T) {
	for _, width := range []int{32, 40, 56} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			in := tileInput{
				VM:    vm.VM{Name: "web", CPUs: 2, Status: "Running"},
				Width: width,
				Now:   time.Now(),
			}
			plain := renderTile(in)
			in.Badge = renderCheckoutBadge(checkoutBadge{Actionable: true})
			badged := renderTile(in)

			if badged == plain {
				t.Fatal("the badge did not reach the rendered tile at all")
			}
			plainLines := strings.Split(plain, "\n")
			lines := strings.Split(badged, "\n")
			if len(lines) != len(plainLines) {
				t.Fatalf("the badge changed the tile's row count: got %d, want %d", len(lines), len(plainLines))
			}
			// EVERY row keeps the tile's exact rendered width — the border
			// stays a straight vertical line, footer included.
			for i, l := range lines {
				if got, want := ansi.StringWidth(l), ansi.StringWidth(plainLines[i]); got != want {
					t.Errorf("row %d width changed: got %d, want %d\n%s", i, got, want, ansi.Strip(badged))
				}
			}

			footer := ansi.Strip(lines[len(lines)-2])
			if !strings.Contains(footer, "actionable") {
				t.Fatalf("footer row does not carry the badge: %q", footer)
			}
			// Right-aligned: the badge ends flush against the trailing padding
			// + border, with no gap of its own.
			if trimmed := strings.TrimRight(footer, " │╯╰┃"); !strings.HasSuffix(trimmed, "actionable") {
				t.Errorf("badge is not right-aligned on the footer row: %q", footer)
			}
			// And the uptime clause still holds the left margin.
			if !strings.Contains(footer, "up") {
				t.Errorf("footer row lost its uptime clause: %q", footer)
			}
		})
	}
}

// TestTileFooterBadgeDegradesOnNarrowTiles pins tileFooterAlign's yield rule:
// the badge shrinks and then disappears before the uptime clause it sits
// beside ever does, and the row stays exactly width cells throughout.
func TestTileFooterBadgeDegradesOnNarrowTiles(t *testing.T) {
	clause := tileChromeStyle.Render("up 3d")
	badge := renderCheckoutBadge(checkoutBadge{Actionable: true, AtRisk: true, Ahead: 2, Dirty: true})
	identity := func(s string) string { return s }
	for width := 1; width <= 60; width++ {
		got := tileRowSplit(clause, ansi.StringWidth(clause), badge, width, 0, identity)
		if w := ansi.StringWidth(got); w > width && w > ansi.StringWidth(clause) {
			t.Fatalf("width %d: footer overflowed to %d cells: %q", width, w, ansi.Strip(got))
		}
		if !strings.Contains(ansi.Strip(got), "up 3d") {
			t.Fatalf("width %d: the uptime clause was dropped or truncated: %q", width, ansi.Strip(got))
		}
	}
	// An empty badge is a no-op, byte for byte.
	if got := tileRowSplit(clause, ansi.StringWidth(clause), "", 40, 0, identity); got != clause {
		t.Errorf("an empty badge must return the clause unchanged, got %q", got)
	}
}

// TestCheckoutBadgeIgnoresPristineClones is the regression for the badge
// firing on a VM where nobody had done any work: a fresh clone sits on the
// default branch, level with its tracking ref, so the sweep classifies it
// "pushed" — which the badge read as "actionable" and painted amber. Only a
// checkout with something of its OWN to land may light it.
func TestCheckoutBadgeIgnoresPristineClones(t *testing.T) {
	now := time.Now()
	pristine := checkouts.VMCheckouts{
		SweptAt: now,
		Checkouts: []checkouts.Checkout{
			{Path: "/home/u/a", Branch: "main", DefaultBranch: "main", PushState: checkouts.PushStatePushed},
			{Path: "/home/u/b", Branch: "main", DefaultBranch: "main", PushState: checkouts.PushStatePushed},
		},
	}
	got := computeCheckoutBadge(pristine, true, true, now)
	if got.Actionable {
		t.Error("a VM holding only pristine clones must not be actionable")
	}
	if got.AtRisk {
		t.Error("a VM holding only pristine clones has nothing at risk")
	}
	if rendered := renderCheckoutBadge(got); rendered != "" {
		t.Errorf("badge rendered %q, want no badge at all", rendered)
	}

	// One real feature branch alongside the clones is still actionable — the
	// fix must not suppress the case the badge exists for.
	withWork := pristine
	withWork.Checkouts = append(append([]checkouts.Checkout{}, pristine.Checkouts...),
		checkouts.Checkout{Path: "/home/u/c", Branch: "feature", DefaultBranch: "main", PushState: checkouts.PushStatePushed})
	if !computeCheckoutBadge(withWork, true, true, now).Actionable {
		t.Error("a fully-pushed feature branch must still read as actionable")
	}

	// Uncommitted work ON the default branch is still at risk: NothingToLand
	// answers only the actionable half, never the delete guard's half.
	dirtyMain := checkouts.VMCheckouts{
		SweptAt: now,
		Checkouts: []checkouts.Checkout{
			{Path: "/home/u/a", Branch: "main", DefaultBranch: "main", PushState: checkouts.PushStatePushed, Dirty: 3},
		},
	}
	if !computeCheckoutBadge(dirtyMain, true, true, now).AtRisk {
		t.Error("uncommitted changes on the default branch must still be at risk")
	}
}
