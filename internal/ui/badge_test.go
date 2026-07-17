package ui

// badge_test.go covers task 04's unlanded-work badge two ways, mirroring the
// split badge.go itself keeps: computeCheckoutBadge is a pure mapping and
// gets ordinary table-driven unit tests (no rendering involved at all); the
// text renderCheckoutBadge produces is pinned with golden.RequireEqual,
// exactly the mechanism header_bands_golden_test.go uses (regenerate with
// `go test ./internal/ui/ -run TestCheckoutBadgeRender -update`).
// spliceCheckoutBadge gets its own tests against real renderTile output,
// since that is the one piece reaching into a rendered tile's actual bytes.

import (
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

// TestSpliceCheckoutBadge exercises the one part of badge.go that reaches
// into an actual renderTile string, against the real renderer rather than a
// hand-built fixture — the whole point being that the splice must survive
// whatever tile.go's border/padding wrapper actually looks like.
func TestSpliceCheckoutBadge(t *testing.T) {
	width := 32
	in := tileInput{
		VM:    vm.VM{Name: "web", CPUs: 2, Status: "Stopped"},
		Width: width,
		Now:   time.Now(),
	}
	tile := renderTile(in)
	innerWidth := tileInnerWidth(width)

	spliced := spliceCheckoutBadge(tile, "⚠ actionable", innerWidth)
	if spliced == tile {
		t.Fatal("spliceCheckoutBadge did not change the tile at all")
	}
	lines := strings.Split(spliced, "\n")
	origLines := strings.Split(tile, "\n")
	if len(lines) != len(origLines) {
		t.Fatalf("splice changed the tile's row count: got %d, want %d", len(lines), len(origLines))
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != ansi.StringWidth(origLines[i]) {
			t.Errorf("row %d width changed: got %d, want %d", i, got, ansi.StringWidth(origLines[i]))
		}
	}
	if !strings.Contains(ansi.Strip(spliced), "actionable") {
		t.Errorf("spliced tile does not contain the badge text:\n%s", ansi.Strip(spliced))
	}

	// Empty badge text is a no-op, byte for byte.
	if got := spliceCheckoutBadge(tile, "", innerWidth); got != tile {
		t.Error("spliceCheckoutBadge(tile, \"\", ...) must return the tile unchanged")
	}

	// A shape this function does not recognize degrades to "unchanged", not a
	// panic: an arbitrary string with the wrong row count.
	weird := "one\ntwo\nthree"
	if got := spliceCheckoutBadge(weird, "⚠ actionable", innerWidth); got != weird {
		t.Errorf("unrecognized tile shape was modified: got %q, want %q", got, weird)
	}
}

// TestSpliceCheckoutBadgeGhostTileNoPanic is the never-swept/degrade-cleanly
// acceptance criterion at the splice boundary: renderCell never actually
// calls checkoutBadgeText for a ghost tile (see renderCell's `i >= len(vms)`
// early return), so this is defense in depth rather than a real code path —
// but the splice makes no ghost-tile-specific assumption, so it must not
// panic if it is ever handed one anyway.
func TestSpliceCheckoutBadgeGhostTileNoPanic(t *testing.T) {
	ghost := renderGhostTile(32, false)
	got := spliceCheckoutBadge(ghost, "⚠ actionable", tileInnerWidth(32))
	if lines := strings.Split(got, "\n"); len(lines) != len(strings.Split(ghost, "\n")) {
		t.Errorf("splice changed the ghost tile's row count: got %d lines, want %d", len(lines), len(strings.Split(ghost, "\n")))
	}
}
