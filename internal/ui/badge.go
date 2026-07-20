package ui

// badge.go computes and renders plan 17 Component 2's unlanded-work tile
// badge: a small, PURELY registry-derived signal for which VMs hold git work
// that has not yet become a PR (actionable) and which hold work that lives
// nowhere but the VM (at-risk). It follows the heartbeat/gauge philosophy
// tile.go already established: it shows only what was actually observed
// (checkouts.Registry.Get, the sweep-populated, host-persisted store from
// Component 1), and a VM with no fresh entry shows NOTHING — never a
// fabricated state. It issues no guest or network call of its own; every bit
// it reads already lives in memory by the time a render happens.
//
// The computation (computeCheckoutBadge) and the text it produces
// (renderCheckoutBadge) live here; HOW that text reaches the tile is
// tile.go's business — renderCell hands the rendered marker to renderTile as
// tileInput.Badge and tileFooterLine lays it out right-aligned on the footer
// row. This file therefore stays pure and trivially unit-testable, with no
// knowledge of borders, padding, or ANSI at all.

import (
	"strconv"
	"strings"
	"time"

	"github.com/lullabot/sandbar/internal/checkouts"
	"github.com/lullabot/sandbar/internal/registry"
)

// badgeFreshnessWindow is how long a RUNNING VM's last sweep may age before
// its registry entry is treated as stale rather than current — generous
// headroom over the sweep's ~60s cadence (Component 1) so a couple of missed
// cycles do not flap the badge on and off.
//
// A STOPPED VM is unconditionally stale for this badge's purposes: the sweep
// only ever updates a running VM's entry (Component 1), so a stopped VM's
// registry row can only get older, never fresher, for however long it stays
// down. That is a deliberate difference from the delete guard (Component 3),
// which deliberately still SHOWS that same last-seen data, labeled rather
// than hidden, because a stopped VM is exactly when the guard matters most.
// This badge's job is different — an at-a-glance CURRENT-state cue, not a
// historical record — so it suppresses rather than labels.
const badgeFreshnessWindow = 10 * time.Minute

// checkoutBadge is the pure verdict computeCheckoutBadge derives from one
// VM's registry entry. Every field traces back to a Checkout row the sweep
// actually recorded; nothing here is guessed.
type checkoutBadge struct {
	// Actionable is true when at least one checkout's branch has reached the
	// forge (PushState pushed). PR-existence (Component 4, task 7) is not
	// wired in yet: until it is, "pushed" alone drives this, per the plan's
	// own call that the badge "reflects push/dirty state alone" until PR
	// state resolves. This field therefore claims only "pushed", never "no
	// open PR" — the stronger claim a later task will layer on top.
	Actionable bool

	// AtRisk is true when at least one checkout has commits that have not
	// reached any remote-tracking ref, or uncommitted changes — work that
	// would be lost if the VM were deleted right now. This is what the
	// delete guard (Component 3) keys on.
	AtRisk bool

	// Ahead is the total unpushed-commit count across every checkout whose
	// PushState is PushStateUnpushed — the number the "↑N" marker names. A
	// "never pushed" checkout (PushStateNever) contributes to AtRisk but not
	// to this count: Checkout.Ahead is defined to be 0 for PushStateNever
	// (there is no tracking ref to diff against), so there is no honest
	// count to add.
	Ahead int

	// Dirty is true when at least one checkout has uncommitted changes.
	Dirty bool

	// Stale is true when this verdict should not be shown at all: there is
	// no registry entry for the VM, no sweep has ever completed, the VM is
	// not currently running, or the last sweep is older than
	// badgeFreshnessWindow. A stale verdict never renders Actionable/AtRisk/
	// Ahead/Dirty, whatever they happened to compute to.
	Stale bool
}

// computeCheckoutBadge maps one VM's registry entry to a badge verdict. It is
// a pure function of its inputs — no guest call, no clock read other than
// the now the caller hands it — so every state (actionable, at-risk, both,
// empty, stale) is deterministic and unit-testable without a running VM.
//
// known is registry.Registry.Get's second return value: whether any entry
// exists for this VM at all (a never-swept VM has none). running is whether
// the VM is CURRENTLY running (deriveStatus == statusRunning) — see
// badgeFreshnessWindow's doc for why a stopped VM is always stale here.
func computeCheckoutBadge(vc checkouts.VMCheckouts, known, running bool, now time.Time) checkoutBadge {
	if !known || vc.SweptAt.IsZero() || !running || now.Sub(vc.SweptAt) > badgeFreshnessWindow {
		return checkoutBadge{Stale: true}
	}

	var b checkoutBadge
	for _, c := range vc.Checkouts {
		switch c.PushState {
		case checkouts.PushStatePushed:
			// A checkout parked on the repo's default branch is "pushed" in the
			// literal sense but holds nothing to turn into a PR — the state
			// every pristine clone is in the moment it lands in the VM. Without
			// this, a freshly provisioned VM that had only ever cloned a repo
			// showed the amber "actionable" badge with no work behind it. See
			// checkouts.Checkout.NothingToLand.
			if !c.NothingToLand() {
				b.Actionable = true
			}
		case checkouts.PushStateUnpushed:
			b.AtRisk = true
			b.Ahead += c.Ahead
		case checkouts.PushStateNever:
			b.AtRisk = true
		}
		if c.Dirty > 0 {
			b.AtRisk = true
			b.Dirty = true
		}
	}
	return b
}

// renderCheckoutBadge turns a verdict into the short marker text
// spliceCheckoutBadge slots into the tile's footer row, styled — or "" for
// nothing to show (stale, or a genuinely clean VM with no checkouts at
// all). Actionable reuses the EXACT amber "worth your attention" vocabulary
// header.go's capacityClause already established (warnStyle plus a leading
// "⚠ "), not a new colour or glyph: this badge and the header's low-capacity
// warning must read as the same visual language. At-risk gets its own marker
// (an "↑N" ahead count and/or "dirty") in the tile's ordinary dim chrome
// (tileChromeStyle) rather than the amber vocabulary — it is information the
// delete guard will act on, not a warning that competes with Actionable's
// "you should look at this" cue.
func renderCheckoutBadge(b checkoutBadge) string {
	if b.Stale {
		return ""
	}
	var parts []string
	if b.Actionable {
		parts = append(parts, warnStyle.Render("⚠ actionable"))
	}
	var risk []string
	switch {
	case b.Ahead > 0:
		risk = append(risk, "↑"+strconv.Itoa(b.Ahead))
	case b.AtRisk && !b.Dirty:
		// At-risk with no countable ahead and no dirty files: a "never
		// pushed" checkout, which has no honest ahead count to show (see
		// checkoutBadge.Ahead's doc) but is still work that exists nowhere
		// but this VM.
		risk = append(risk, "unpushed")
	}
	if b.Dirty {
		risk = append(risk, "dirty")
	}
	if len(risk) > 0 {
		parts = append(parts, tileChromeStyle.Render(strings.Join(risk, " ")))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "  ")
}

// checkoutBadgeText is renderCell's one call into this file: look up scope's
// vm's registry entry, compute the verdict, and render it — "" for a VM with
// nothing to show. Kept as a single entry point so board.go's renderCell
// only ever needs one line, and every other detail (the registry read, the
// freshness rule, the marker vocabulary) stays in this file.
func checkoutBadgeText(reg *checkouts.Registry, scope registry.Scope, name string, running bool, now time.Time) string {
	vc, known := reg.Get(scope, name)
	return renderCheckoutBadge(computeCheckoutBadge(vc, known, running, now))
}
