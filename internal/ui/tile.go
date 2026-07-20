package ui

// tile.go renders ONE VM as a bordered card: the board's atomic unit.
// Everything on it is derived, never a struct field rendered straight
// through that would let it lie:
//
//   - Status is ALWAYS reached through deriveStatus (jobs.go) — never
//     v.Status directly. Lima has no Building status; to Lima a VM being
//     provisioned is simply Running, so a tile that trusted v.Status would
//     show a build in flight as a healthy idle VM, and a FAILED provision as
//     a reassuring green "Running" tile.
//   - The cpu/mem gauges render only for the DERIVED running state, and only
//     when the heartbeat actually produced a reading (guestSample's Has*
//     fields) — never a zeroed bar standing in for "no reading yet".
//   - The architecture and base-image badges surface only when the fleet
//     actually disagrees about them, via a genuine equality test
//     (computeFleetUniformity) over every VM's value — not a hardcoded field
//     name.
//
// Six content lines, always, regardless of what is known: title, status, up
// to three gauge/badge rows (cpu, mem, disk — or a building VM's progress bar
// and role/task line), and a closing `up <duration>` / `last used <duration>
// ago` line. A fixed line count is what lets the board (board.go) lay tiles
// out in a grid without measuring each one (see layout.go's tileHeight).

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Tile framing. A rounded (or, focused, thick) border plus Padding(0,1) adds
// exactly this many columns beyond the content width layout.go's classify
// hands out as TileWidth, and exactly two rows beyond the six content lines
// below (tileHeight = 6 + 2 there).
const (
	tileBorderCols  = 2
	tilePaddingCols = 2
	tileContentRows = 6
)

// lowFreeThreshold is the "less than 10% free" line every low-capacity warning
// in this package draws from — host memory/disk (hostwarn.go) and a single
// VM's mem/disk gauge (below) — stated ONCE so the header's host warnings and
// a tile's own badges can never quietly disagree about what "low" means.
// Comparisons use the USED fraction against the complementary bound
// (`frac > 1-lowFreeThreshold`), never `1-frac < lowFreeThreshold`: the
// subtraction turns an exactly-at-threshold reading into "below" in binary
// floating point (1-0.9 = 0.0999…8 < 0.10), and the boundary must not warn.
const lowFreeThreshold = 0.10

// tileInput bundles a single VM's rendering material: the tile-width budget
// (layout.go), the job snapshot (jobs.go), the heartbeat sample
// (heartbeat.go), and this VM's resolved place in the exception-only-field
// rule below. Now is threaded
// through explicitly (rather than read from time.Now() inside) so the
// up/last-used duration math is deterministic in tests.
type tileInput struct {
	VM     vm.VM
	Job    jobSnapshot
	HasJob bool
	// RemoteProvisioning is set when the VM carries an in-flight provenance
	// marker but has no local build job — another controller is building it, and
	// this tile must show Building, not Running. See deriveStatus.
	RemoteProvisioning bool
	// RemoteProgress is that in-flight marker's published position, used to draw
	// the bar when the build belongs to another controller. Ignored when HasJob:
	// a local build's own parsed stream is fresher than anything it published.
	RemoteProgress ansibleProgress

	Sample    guestSample
	HasSample bool

	// Traits/Uniform are this VM's exception-only field values and the
	// fleet-wide verdict on whether each one is uniform. Gathering Traits for
	// every VM on the board (Arch off vm.VM, Base/Managed off the registry)
	// and calling computeFleetUniformity once over all of them is the board's
	// job (board.go); this file only defines the shape and the rule.
	Traits  vmTraits
	Uniform fleetUniformity

	Focused bool
	Width   int // layoutMode.TileWidth

	// ProfileLabel is the tile's provenance: the name of the profile this
	// VM runs through (its owning member's profile.Name). It folds into the
	// title row (line 0) rather than growing the tile's fixed six-line budget
	// — see tileTitleLine. Empty renders no label at all (never a bracket
	// pair around nothing).
	ProfileLabel string

	// Badge is the unlanded-work marker (badge.go) for this VM: "" when there
	// is nothing to show. It rides the footer row, RIGHT-aligned, the same way
	// ProfileLabel rides the title row — see tileFooterLine. It is passed in
	// (rather than spliced onto the rendered tile afterwards) so the footer's
	// width arithmetic happens on plain, pre-border text: splicing into the
	// already-bordered string meant diffing rows byte-wise to re-find the
	// border, which could cut an ANSI escape in half and corrupt the row's
	// measured width.
	Badge string

	// Spinner is the current animation frame for a Building tile's glyph. ""
	// falls back to a static glyph — what every test not driving a real
	// spinner gets, and a safe default if the board omits one entirely.
	Spinner string

	Now time.Time
}

// renderTile draws one VM's card.
func renderTile(in tileInput) string {
	width := tileInnerWidth(in.Width)
	status := deriveStatus(in.VM, in.Job, in.HasJob, in.RemoteProvisioning)

	lines := make([]string, tileContentRows)
	lines[0] = tileTitleLine(in.VM.Name, in.ProfileLabel, width)
	lines[1] = tileStatusLine(status, in.Spinner, in.Traits, in.Uniform)

	if status == statusBuilding {
		// A building tile replaces ALL of its gauges with an in-place progress
		// bar and the current Ansible role/task count — even if a live
		// heartbeat sample exists (the VM is genuinely booted; Ansible is just
		// a process inside it), cpu/mem must not leak through here.
		// A LOCAL build's bar comes from its own parsed output stream; a build
		// running on ANOTHER controller has no such stream here, so its position
		// comes from what that controller published to the provenance marker. The
		// two are the same shape on purpose — one renderer, either source.
		prog := in.Job.Progress
		if !in.HasJob {
			prog = in.RemoteProgress
		}
		lines[2] = tileProgressBarLine(prog, width)
		lines[3] = tileRoleLine(prog)
		// lines[4] and lines[5] stay blank: a building VM's own vm.VM record
		// can be a zero value for the first minutes of a create (the clone
		// has not landed in `limactl list` yet — see jobRegistry's "seen"
		// comment), so there is no honest up/last-used to compute yet.
	} else {
		// EVERY GAUGE HAS A FIXED ROW, and disk's is row 4 whatever else is known.
		// These rows used to be packed from the top, so a running VM whose heartbeat
		// had no reading yet dropped its cpu and mem rows and disk SLID UP into their
		// slot. The heartbeat is torn down whenever the board is not the visible
		// screen and after the idle window (heartbeat.go), so leaving the board and
		// coming back — or simply sitting still for five minutes — made two gauges
		// vanish and the third jump. The tile appeared to be losing data it had never
		// actually lost.
		//
		// A running VM therefore always shows cpu and mem. When the heartbeat has no
		// reading, they render as "no reading" (tileGaugeNoReading) — NOT as an empty
		// bar at 0%, which would be the "zero standing in for no reading yet" lie this
		// file exists to prevent. A stopped VM has no cpu or mem to report at all, so
		// its rows stay blank: absent, not zeroed.
		if status == statusRunning {
			if in.HasSample && in.Sample.HasCPU {
				lines[2] = tileGaugeLine(cpuLabel(in.VM), in.Sample.CPUPct/100,
					fmt.Sprintf("%.0f%%", in.Sample.CPUPct), width)
			} else {
				lines[2] = tileGaugeNoReading(cpuLabel(in.VM), width)
			}
			if in.HasSample && in.Sample.HasMem() {
				frac := memFraction(in.Sample)
				value := humanizeBytes(strconv.FormatUint(in.Sample.MemUsed, 10)) + "/" +
					humanizeBytes(strconv.FormatUint(in.Sample.MemTotal, 10))
				if frac > 1-lowFreeThreshold {
					lines[3] = tileWarnGaugeLine("mem", frac, value, width)
				} else {
					lines[3] = tileGaugeLine("mem", frac, value, width)
				}
			} else {
				lines[3] = tileGaugeNoReading("mem", width)
			}
		}
		// Disk is real data today and always renders, running or stopped, and always
		// on the same row.
		lines[4] = tileDiskLine(in.VM, status, in.Sample, in.HasSample, width)
		lines[5] = tileFooterLine(in.VM, in.Now, in.Badge, width)
	}

	for i, l := range lines {
		lines[i] = tilePad(l, width)
	}

	// The focused tile's border differs by more than colour (a thicker glyph set), so
	// focus survives NO_COLOR and a monochrome terminal. Both styles are built once
	// (styles.go) rather than per tile per frame: Render does not mutate them.
	style := tileFrameStyle
	if in.Focused {
		style = tileFocusedFrameStyle
	}
	return style.Render(strings.Join(lines, "\n"))
}

// tileTitleLine folds the profile-provenance label into the title row
// (line 0) instead of growing the tile's fixed six-line budget: the VM's name
// stays LEFT, unchanged (that is the identity a reader scans
// for first), and the profile label rides the same row, right-aligned,
// whenever there is room next to it. The label shrinks (truncates, then
// disappears entirely) before the name ever would — the name is the tile's
// identity, provenance is secondary — so a long VM name on a narrow tile
// degrades exactly as it always has, just without a label crowding it.
func tileTitleLine(name, profile string, width int) string {
	title := tileTitleStyle.Render(name)
	if profile == "" {
		return title
	}
	nameW := ansi.StringWidth(name)
	label := "[" + profile + "]"
	gap := width - nameW - ansi.StringWidth(label)
	if gap < 1 {
		// Not enough room for the label as given — try shrinking IT (never the
		// name) to whatever fits after one gap column and the brackets.
		avail := width - nameW - 1 - 2
		if avail < 1 {
			return title // no room at all: drop the label rather than crowd the name
		}
		label = "[" + ansi.Truncate(profile, avail, "…") + "]"
		gap = width - nameW - ansi.StringWidth(label)
		if gap < 1 {
			return title
		}
	}
	return title + strings.Repeat(" ", gap) + tileChromeStyle.Render(label)
}

// tileInnerWidth is the text budget inside the border and padding.
func tileInnerWidth(width int) int {
	w := width - tileBorderCols - tilePaddingCols
	if w < 1 {
		w = 1
	}
	return w
}

// tilePad truncates (with an ellipsis) or right-pads a line to exactly width
// visible cells, so every content line — and therefore the tile's rendered
// border — comes out a uniform rectangle regardless of what it holds.
func tilePad(s string, width int) string {
	s = ansi.Truncate(s, width, "…")
	if w := ansi.StringWidth(s); w < width {
		s += strings.Repeat(" ", width-w)
	}
	return s
}

// tileStatusLine renders the glyph + status word (the primary scanning
// channel), plus any exception-only badges this VM's Traits/Uniform decided
// should surface.
func tileStatusLine(status derivedStatus, spinner string, t vmTraits, u fleetUniformity) string {
	glyph := tileGlyph(status, spinner)
	line := tileStyleFor(status).Render(glyph + " " + status.String())
	if badges := tileBadges(t, u); len(badges) > 0 {
		line += "  " + tileChromeStyle.Render(strings.Join(badges, " · "))
	}
	return line
}

// tileGlyph picks the status glyph. A caller-supplied spinner frame animates
// the Building glyph; every other status is static (there is nothing to
// animate about a settled Running/Stopped/Failed state).
func tileGlyph(status derivedStatus, spinner string) string {
	if status == statusBuilding && spinner != "" {
		return spinner
	}
	switch status {
	case statusRunning:
		return "●"
	case statusBuilding:
		return "◐"
	case statusFailed:
		return "✖"
	default: // statusStopped
		return "○"
	}
}

// tileStyleFor is the colour half of the status channel — glyph and word
// always carry the meaning too (see tileGlyph/derivedStatus.String), so
// colour is never load-bearing on its own.
func tileStyleFor(status derivedStatus) lipgloss.Style {
	switch status {
	case statusRunning:
		return tileRunningStyle
	case statusBuilding:
		return tileBuildingStyle
	case statusFailed:
		return tileFailedStyle
	default: // statusStopped
		return tileStoppedStyle
	}
}

// tileBadges resolves this VM's exception-only fields against the fleet's
// uniformity verdict: a field hides when the whole fleet agrees on it, and
// shows THIS VM's own value the moment it does not. The managed/external
// field goes through the identical, un-special-cased rule — it is not
// deleted, it is simply never exceptional once the board filters to managed
// clones only (board.go), which makes it uniform by construction.
func tileBadges(t vmTraits, u fleetUniformity) []string {
	var badges []string
	if u.ShowArch {
		badges = append(badges, "arch "+t.Arch)
	}
	if u.ShowBase {
		base := t.Base
		if base == "" {
			base = "none"
		}
		badges = append(badges, "base "+base)
	}
	if u.ShowManaged {
		if t.Managed {
			badges = append(badges, "managed")
		} else {
			badges = append(badges, "external")
		}
	}
	return badges
}

// vmTraits are the exception-only field values for ONE VM — the raw material
// the fleet-uniformity rule (below) compares across every VM the caller
// includes. Gathering these for a whole board (Arch off vm.VM, Base/Managed
// off the registry) is the board's job (board.go); this file only defines the
// shape and the rule that consumes it.
type vmTraits struct {
	Arch    string
	Base    string
	Managed bool
}

// fleetUniformity records, per exception-only field, whether computeFleetUniformity
// found at least one VM disagreeing with the rest — the "Show" naming (and,
// deliberately, the zero value being "hidden") mirrors guestSample's Has*
// fields: the SAFE default, for a caller that forgot to populate this or a
// test that never calls computeFleetUniformity at all, is to show nothing
// invented, not to paint every tile with badges. False hides the field on
// every tile (the whole fleet agreed); true shows it — with each tile's own
// value — so a reader can see not just the odd one out but what it differs
// from.
type fleetUniformity struct {
	ShowArch    bool
	ShowBase    bool
	ShowManaged bool
}

// computeFleetUniformity is THE exception-only rule, and it is a genuine
// equality test over the values actually present — never a hardcoded field
// name — so a second base image or a lone foreign architecture makes it
// react automatically, with no code change here. An empty or single-VM fleet
// is vacuously uniform: there is nothing yet to disagree with, so every Show
// flag comes back false.
func computeFleetUniformity(fleet []vmTraits) fleetUniformity {
	return fleetUniformity{
		ShowArch:    !fleetAgrees(fleet, func(t vmTraits) string { return t.Arch }),
		ShowBase:    !fleetAgrees(fleet, func(t vmTraits) string { return t.Base }),
		ShowManaged: !fleetAgrees(fleet, func(t vmTraits) bool { return t.Managed }),
	}
}

// fleetAgrees reports whether every VM in fleet has the same value for get.
func fleetAgrees[T comparable](fleet []vmTraits, get func(vmTraits) T) bool {
	if len(fleet) == 0 {
		return true
	}
	first := get(fleet[0])
	for _, t := range fleet[1:] {
		if get(t) != first {
			return false
		}
	}
	return true
}

// tileGaugeLabelWidth is the gauge rows' shared label column. It is sized for the
// widest label any tile can produce — "cpu (16c)" — so cpu, mem, disk and build
// all start their bars in the same column and a VM with a two-digit core count
// does not shove its bar out of line with the tile above it.
const tileGaugeLabelWidth = 9

// cpuLabel is the cpu gauge's label, carrying the VM's ALLOCATED core count:
// "cpu (4c)". The count is the one fact the deleted VM screen had that the tile
// did not, and it belongs next to the utilization it is the denominator of — the
// gauge reads "how hard are these 4 cores working", which is unanswerable without
// knowing there are 4. A VM Lima has not reported yet has no count to show (it is
// mid-clone), and gets a bare "cpu" rather than an invented "(0c)".
func cpuLabel(v vm.VM) string {
	if v.CPUs <= 0 {
		return "cpu"
	}
	return fmt.Sprintf("cpu (%dc)", v.CPUs)
}

// tileGaugeLine renders one "<label> <bar> <value>" row, dimmed as chrome:
// the gauge's presence (or absence, for cpu/mem — see renderTile) is the
// signal; the row itself is secondary to the title/status above it.
func tileGaugeLine(label string, frac float64, value string, width int) string {
	return tileGaugeRow(label, value, width, func(barWidth int) string {
		return tileGaugeBar(frac, barWidth)
	})
}

// tileGaugeRow is the row arithmetic every gauge shares: the label column, the bar
// that fills what is left, and the value. Stated ONCE, because the fixed-row
// alignment the tile depends on is exactly what drifts when two copies of it
// disagree about the label width or the separator.
func tileGaugeRow(label, value string, width int, bar func(barWidth int) string) string {
	return tileGaugeRowStyled(tileChromeStyle, label, value, width, bar)
}

// tileGaugeRowStyled is tileGaugeRow parametrized by the row's style — the
// chrome grey every gauge used before, or warnStyle for a row a low-capacity
// warning (tileWarnGaugeLine, below) has flagged.
//
// barWidth is computed from ansi.StringWidth(labelCol), NOT len(labelCol):
// fmt's %-*s pads label to tileGaugeLabelWidth RUNES/columns, but a label
// carrying a multi-byte rune (the warning marker "⚠") has a byte length wider
// than its column count once padded, and len() would (wrongly) starve the bar
// by that difference — a real bug this fix closes (see
// TestTileGaugeRowUsesDisplayWidthNotByteLength), invisible until this file's
// only labels were pure ASCII, where the two measures always agreed.
func tileGaugeRowStyled(style lipgloss.Style, label, value string, width int, bar func(barWidth int) string) string {
	labelCol := fmt.Sprintf("%-*s", tileGaugeLabelWidth, label)
	barWidth := width - ansi.StringWidth(labelCol) - 1 - ansi.StringWidth(value)
	if barWidth < 3 {
		barWidth = 3
	}
	return style.Render(labelCol + bar(barWidth) + " " + value)
}

// tileWarnGaugeLine renders a gauge row exactly like tileGaugeLine, but flags
// a resource below lowFreeThreshold free (rules 3+4 of the low-capacity-
// warning feature): a "⚠ " marker prefixed to the label, and the WHOLE row in
// warnStyle (the repo's one existing warning colour — styles.go already uses
// it for the building tile's amber, so this reuses the single palette rather
// than introducing a second, competing "warning yellow") instead of the
// ordinary chrome grey — so a tile almost out of memory or disk is impossible
// to miss scanning the board.
//
// The marker is the single-cell U+26A0 WARNING SIGN ("⚠"), deliberately NOT
// its two-cell emoji-presentation variant ("⚠️", U+26A0 U+FE0F): ansi.
// StringWidth reports the plain form as 1 column and the VS16 form as 2 (spot-
// checked directly against this build's ansi package), and a caller that
// assumed 1 for the emoji variant would silently misalign this row's bar
// against its siblings — tileGaugeRowStyled's own fix for exactly that class
// of bug is what makes this row-align safe.
func tileWarnGaugeLine(label string, frac float64, value string, width int) string {
	return tileGaugeRowStyled(warnStyle, "⚠ "+label, value, width, func(barWidth int) string {
		return tileGaugeBar(frac, barWidth)
	})
}

// tileGaugeNoReading renders a gauge row for a metric that is REAL but currently
// UNREAD — a running VM whose heartbeat has not reported yet, or whose heartbeat
// the idle gate has torn down. It holds the row (so nothing below it moves) while
// refusing to state a value.
//
// The bar is drawn in its own glyph, not the empty-bar glyph: an empty ░ bar is
// how this tile says "0%", and reusing it here would turn "I don't know" into "it
// is idle" — a busy VM would read as asleep. The em dash says the same thing in
// the value column, where a number would otherwise go.
func tileGaugeNoReading(label string, width int) string {
	return tileGaugeRow(label, "—", width, func(barWidth int) string {
		return strings.Repeat("·", barWidth)
	})
}

// tileGaugeBar renders a filled/empty bar of exactly width cells for a
// fraction clamped to [0,1].
func tileGaugeBar(frac float64, width int) string {
	if width < 1 {
		width = 1
	}
	switch {
	case frac < 0:
		frac = 0
	case frac > 1:
		frac = 1
	}
	filled := int(frac*float64(width) + 0.5)
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// memFraction is the mem gauge's fill, guarding the same zero-total edge case
// ansibleProgress.Fraction() guards for the build bar.
func memFraction(s guestSample) float64 {
	if s.MemTotal == 0 {
		return 0
	}
	return float64(s.MemUsed) / float64(s.MemTotal)
}

// tileDiskLine renders the always-on disk gauge, GUEST-FIRST, HOST-FALLBACK.
//
// While the VM is RUNNING and its heartbeat sample carries a guest root-
// filesystem reading (sample.HasDisk()), this draws the GUEST's own
// used/total — the only honest source of "how full is this VM". ZFS
// compression and sparse disk images both let the backing image occupy far
// fewer host bytes than the guest has actually written, so a host-side du of
// the instance directory (v.DiskUsed) can read comfortably under capacity —
// say 18.5/20 GiB — while the guest's own filesystem is genuinely 100% full.
// Only the guest itself can answer that question.
//
// Without a guest reading — a stopped VM (no live heartbeat to ask), an old
// guest whose heartbeat predates this probe, or a running one whose first
// sample hasn't landed yet — this renders EXACTLY the host-side du it always
// has: v.Disk/v.DiskUsed, Lima's own reporting, with DiskUsed=="" meaning
// UNMEASURABLE and drawn as an explicit "?", never a fabricated zero.
func tileDiskLine(v vm.VM, status derivedStatus, sample guestSample, hasSample bool, width int) string {
	if status == statusRunning && hasSample && sample.HasDisk() {
		total := humanizeBytes(strconv.FormatUint(sample.DiskTotal, 10))
		value := humanizeBytes(strconv.FormatUint(sample.DiskUsed, 10)) + "/" + total
		frac := float64(sample.DiskUsed) / float64(sample.DiskTotal)
		if frac > 1-lowFreeThreshold {
			return tileWarnGaugeLine("disk", frac, value, width)
		}
		return tileGaugeLine("disk", frac, value, width)
	}

	total := humanizeBytes(v.Disk)
	if total == "" {
		total = "?"
	}
	if v.DiskUsed == "" {
		return tileGaugeLine("disk", 0, "?/"+total, width)
	}
	usedN, uerr := strconv.ParseInt(v.DiskUsed, 10, 64)
	totalN, terr := strconv.ParseInt(v.Disk, 10, 64)
	if uerr == nil && terr == nil && totalN > 0 {
		frac := float64(usedN) / float64(totalN)
		value := humanizeBytes(v.DiskUsed) + "/" + total
		if frac > 1-lowFreeThreshold {
			return tileWarnGaugeLine("disk", frac, value, width)
		}
		return tileGaugeLine("disk", frac, value, width)
	}
	// totalN unmeasurable (uerr/terr set, or Disk<=0): no honest free% to
	// compute, so — like DiskUsed=="" above — this never warns; it just
	// draws the same fallback disk row it always has.
	return tileGaugeLine("disk", 0, humanizeBytes(v.DiskUsed)+"/"+total, width)
}

// tileProgressBarLine is a building tile's gauge: the same bar renderer,
// filled from the job's parsed Ansible progress.
func tileProgressBarLine(p ansibleProgress, width int) string {
	frac := p.Fraction()
	return tileGaugeLine("build", frac, fmt.Sprintf("%d%%", int(frac*100+0.5)), width)
}

// tileRoleLine is a building tile's second content row: the current Ansible
// role and task count (e.g. "ansible: docker · 7/19"). Ansible has not
// necessarily started yet — the clone and the boot take most of a build's
// wall time — so this falls back through Task, then to sand's own phase
// banner (Step), which is the tile's only signal during those otherwise
// silent minutes (see ansible.go's Step doc).
func tileRoleLine(p ansibleProgress) string {
	switch {
	case cmp.Or(p.Role, p.Task) != "":
		return tileChromeStyle.Render(fmt.Sprintf("ansible: %s · %d/%d", cmp.Or(p.Role, p.Task), p.Index, p.Total))
	case p.Step != "":
		return tileChromeStyle.Render(p.Step)
	default:
		return tileChromeStyle.Render("starting…")
	}
}

// tileFooterLine is the closing line every non-building tile ends on: `up
// <duration>` while Lima reports the VM Running, `last used <duration> ago`
// (or "never used") while Lima reports it Stopped. This asks a narrower,
// independent question from the status line above ("what real state is this
// actual entity in, and since when") — a Failed tile still has some real
// underlying Lima state, which is exactly what this answers.
//
// badge is the unlanded-work marker (badge.go), RIGHT-aligned on this same row
// — the uptime clause is what a reader scans for on the left, so the git note
// hangs off the opposite margin rather than trailing two spaces behind a
// variable-width duration, where it never landed in the same column twice.
// It follows tileTitleLine's rule for a secondary right-aligned label: the
// badge yields (truncating, then disappearing) before the uptime clause ever
// does, so a narrow tile degrades exactly as it did before the badge existed.
func tileFooterLine(v vm.VM, now time.Time, badge string, width int) string {
	return tileFooterAlign(tileUptimeClause(v, now), badge, width)
}

// tileFooterAlign lays the uptime clause left and the badge right on one row
// of exactly width cells, or returns the clause alone when the badge cannot
// fit beside it. Both inputs arrive already styled, so every measurement here
// goes through ansi.StringWidth rather than len: the styles carry escape
// bytes that occupy no cells.
func tileFooterAlign(clause, badge string, width int) string {
	if badge == "" {
		return clause
	}
	clauseW := ansi.StringWidth(clause)
	gap := width - clauseW - ansi.StringWidth(badge)
	if gap < 1 {
		// No room for the badge as given — shrink IT (never the clause), and
		// drop it entirely rather than crowd the uptime it sits beside.
		avail := width - clauseW - 1
		if avail < 1 {
			return clause
		}
		badge = ansi.Truncate(badge, avail, "…")
		gap = width - clauseW - ansi.StringWidth(badge)
		if gap < 1 {
			return clause
		}
	}
	return clause + strings.Repeat(" ", gap) + badge
}

// tileUptimeClause is the footer's left half: the `up <duration>` / `last used
// <duration> ago` / `never used` text, with no badge and no padding.
func tileUptimeClause(v vm.VM, now time.Time) string {
	// These times are SAMPLED IN listCmd (commands.go), off the Bubble Tea goroutine,
	// and only read here. They used to be stat'd right in this function — up to three
	// os.Stat calls per tile, on every frame, and a building board redraws ~10x a
	// second for its spinner. That is a blocking filesystem call on the render path,
	// which listCmd's own doc already forbids for exactly the reason it forbids it:
	// one stale mount and the whole UI stalls.
	if v.Status == limaRunning {
		if !v.UpSince.IsZero() {
			return tileChromeStyle.Render("up " + formatUptime(now.Sub(v.UpSince)))
		}
		return tileChromeStyle.Render("up")
	}
	if !v.LastUsed.IsZero() {
		return tileChromeStyle.Render("last used " + formatAgo(now.Sub(v.LastUsed)))
	}
	return tileChromeStyle.Render("never used")
}

// lastUsed reports the mtime that best answers "when was this stopped VM
// last used": ~/.lima/<name>/ha.stderr.log's mtime — the hostagent's last
// write. Verified against a real Lima 2.1.3 instance: `limactl stop` leaves
// this file's mtime at the moment QEMU exits, seconds after the command
// returns.
//
// A VM that has NEVER been started has no ha.stderr.log at all, and that
// absence — not a fallback — is what must read as "never used": falling back
// to the disk file there would report its CREATION time as a bogus "last
// used" value. The disk file's mtime is instead a fallback for the narrower
// case of ha.stderr.log existing but being unreadable for some other reason
// (a permissions problem, say) — evidence the VM error is real for
// diagnostic without misreporting a never-started VM as used.
func lastUsed(hf lima.HostFiles, dir string) (time.Time, bool) {
	if dir == "" {
		return time.Time{}, false
	}
	fi, err := hf.Stat(filepath.Join(dir, "ha.stderr.log"))
	if err == nil {
		return fi.ModTime(), true
	}
	if !errors.Is(err, fs.ErrNotExist) {
		if fi2, err2 := hf.Stat(filepath.Join(dir, "disk")); err2 == nil {
			return fi2.ModTime(), true
		}
	}
	return time.Time{}, false
}

// upSince reports when the CURRENT boot of a running VM began, approximated
// by the mtime of ~/.lima/<name>/ha.pid — the hostagent's pidfile. Verified
// against a real Lima 2.1.3 instance: ha.pid is written fresh at every
// `limactl start` and removed at every stop, and its mtime stays FIXED at
// start for as long as the VM runs — unlike ha.stderr.log, which keeps
// advancing as the hostagent logs, and so cannot stand in for a boot marker
// the way it can for a stopped VM's last-used time. qemu.pid, written at the
// same moment, is the fallback if ha.pid is missing but the VM is running.
func upSince(hf lima.HostFiles, dir string) (time.Time, bool) {
	if dir == "" {
		return time.Time{}, false
	}
	for _, name := range []string{"ha.pid", "qemu.pid"} {
		if fi, err := hf.Stat(filepath.Join(dir, name)); err == nil {
			return fi.ModTime(), true
		}
	}
	return time.Time{}, false
}

// formatUptime renders a running VM's closing line: hours+minutes below a
// day, days+hours at or above one — the "up 2h14m" style.
func formatUptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Minute)
	totalMin := int(d.Minutes())
	days := totalMin / (24 * 60)
	hours := (totalMin / 60) % 24
	mins := totalMin % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh%dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// formatAgo renders a stopped VM's closing line in the coarsest unit that
// keeps it readable — the "last used 3d ago" / "6 weeks ago" style.
func formatAgo(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	default:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/(24*30)))
	}
}
