package ui

// tile.go renders ONE VM as a bordered card: the board's (task 08) atomic
// unit. Everything on it is derived, never a struct field rendered straight
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
// ago` line. A fixed line count is what lets the board (task 08) lay tiles
// out in a grid without measuring each one (see layout.go's tileHeight).

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

// tileInput bundles a single VM's rendering material: task 03's tile-width
// budget, task 04's job snapshot, task 05's heartbeat sample, and this VM's
// resolved place in the exception-only-field rule below. Now is threaded
// through explicitly (rather than read from time.Now() inside) so the
// up/last-used duration math is deterministic in tests.
type tileInput struct {
	VM     vm.VM
	Job    jobSnapshot
	HasJob bool

	Sample    guestSample
	HasSample bool

	// Traits/Uniform are this VM's exception-only field values and the
	// fleet-wide verdict on whether each one is uniform. Gathering Traits for
	// every VM on the board (Arch off vm.VM, Base/Managed off the registry)
	// and calling computeFleetUniformity once over all of them is the board's
	// job (task 08); this file only defines the shape and the rule.
	Traits  vmTraits
	Uniform fleetUniformity

	Focused bool
	Width   int // layoutMode.TileWidth

	// Spinner is the current animation frame for a Building tile's glyph. ""
	// falls back to a static glyph — what every test not driving a real
	// spinner gets, and a safe default if the board omits one entirely.
	Spinner string

	Now time.Time
}

// renderTile draws one VM's card.
func renderTile(in tileInput) string {
	width := tileInnerWidth(in.Width)
	status := deriveStatus(in.VM, in.Job, in.HasJob)

	lines := make([]string, tileContentRows)
	lines[0] = tileTitleStyle.Render(in.VM.Name)
	lines[1] = tileStatusLine(status, in.Spinner, in.Traits, in.Uniform)

	if status == statusBuilding {
		// A building tile replaces ALL of its gauges with an in-place progress
		// bar and the current Ansible role/task count — even if a live
		// heartbeat sample exists (the VM is genuinely booted; Ansible is just
		// a process inside it), cpu/mem must not leak through here.
		lines[2] = tileProgressBarLine(in.Job.Progress, width)
		lines[3] = tileRoleLine(in.Job.Progress)
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
				lines[2] = tileGaugeLine("cpu", in.Sample.CPUPct/100,
					fmt.Sprintf("%.0f%%", in.Sample.CPUPct), width)
			} else {
				lines[2] = tileGaugeNoReading("cpu", width)
			}
			if in.HasSample && in.Sample.HasMem() {
				lines[3] = tileGaugeLine("mem", memFraction(in.Sample),
					humanizeBytes(strconv.FormatUint(in.Sample.MemUsed, 10))+"/"+
						humanizeBytes(strconv.FormatUint(in.Sample.MemTotal, 10)), width)
			} else {
				lines[3] = tileGaugeNoReading("mem", width)
			}
		}
		// Disk is real data today and always renders, running or stopped, and always
		// on the same row.
		lines[4] = tileDiskLine(in.VM, width)
		lines[5] = tileFooterLine(in.VM, in.Now)
	}

	for i, l := range lines {
		lines[i] = tilePad(l, width)
	}

	border := lipgloss.RoundedBorder()
	borderColor := tileUnfocusedBorderColor
	if in.Focused {
		// The focused tile's border differs by more than colour (a thicker
		// glyph set), so focus survives NO_COLOR and a monochrome terminal.
		border = lipgloss.ThickBorder()
		borderColor = tileFocusedBorderColor
	}
	style := lipgloss.NewStyle().Border(border).BorderForeground(borderColor).Padding(0, 1)
	return style.Render(strings.Join(lines, "\n"))
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
// clones only (task 08), which makes it uniform by construction.
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
// off the registry) is the board's job (task 08); this file only defines the
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

// tileGaugeLine renders one "<label> <bar> <value>" row, dimmed as chrome:
// the gauge's presence (or absence, for cpu/mem — see renderTile) is the
// signal; the row itself is secondary to the title/status above it.
func tileGaugeLine(label string, frac float64, value string, width int) string {
	labelCol := fmt.Sprintf("%-6s", label) // fits "build" (5 chars) plus a separating space
	barWidth := width - len(labelCol) - 1 - ansi.StringWidth(value)
	if barWidth < 3 {
		barWidth = 3
	}
	return tileChromeStyle.Render(labelCol + tileGaugeBar(frac, barWidth) + " " + value)
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
	labelCol := fmt.Sprintf("%-6s", label)
	const value = "—"
	barWidth := width - len(labelCol) - 1 - ansi.StringWidth(value)
	if barWidth < 3 {
		barWidth = 3
	}
	return tileChromeStyle.Render(labelCol + strings.Repeat("·", barWidth) + " " + value)
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

// tileDiskLine renders the always-on disk gauge. v.Disk/v.DiskUsed are raw
// byte-count strings (Lima's own reporting, with DiskUsed populated from
// diskUsedBytes at list time — see commands.go's listCmd); DiskUsed=="" means
// UNMEASURABLE, and the gauge renders that as an explicit "?", never a
// fabricated zero.
func tileDiskLine(v vm.VM, width int) string {
	total := humanizeBytes(v.Disk)
	if total == "" {
		total = "?"
	}
	if v.DiskUsed == "" {
		return tileGaugeLine("disk", 0, "?/"+total, width)
	}
	usedN, uerr := strconv.ParseInt(v.DiskUsed, 10, 64)
	totalN, terr := strconv.ParseInt(v.Disk, 10, 64)
	frac := 0.0
	if uerr == nil && terr == nil && totalN > 0 {
		frac = float64(usedN) / float64(totalN)
	}
	return tileGaugeLine("disk", frac, humanizeBytes(v.DiskUsed)+"/"+total, width)
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
	case p.Role != "":
		return tileChromeStyle.Render(fmt.Sprintf("ansible: %s · %d/%d", p.Role, p.Index, p.Total))
	case p.Task != "":
		return tileChromeStyle.Render(fmt.Sprintf("ansible: %s · %d/%d", p.Task, p.Index, p.Total))
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
func tileFooterLine(v vm.VM, now time.Time) string {
	if v.Status == limaRunning {
		if t, ok := upSince(v.Dir); ok {
			return tileChromeStyle.Render("up " + formatUptime(now.Sub(t)))
		}
		return tileChromeStyle.Render("up")
	}
	if t, ok := lastUsed(v.Dir); ok {
		return tileChromeStyle.Render("last used " + formatAgo(now.Sub(t)))
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
func lastUsed(dir string) (time.Time, bool) {
	if dir == "" {
		return time.Time{}, false
	}
	fi, err := os.Stat(filepath.Join(dir, "ha.stderr.log"))
	if err == nil {
		return fi.ModTime(), true
	}
	if !os.IsNotExist(err) {
		if fi2, err2 := os.Stat(filepath.Join(dir, "disk")); err2 == nil {
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
func upSince(dir string) (time.Time, bool) {
	if dir == "" {
		return time.Time{}, false
	}
	for _, name := range []string{"ha.pid", "qemu.pid"} {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return fi.ModTime(), true
		}
	}
	return time.Time{}, false
}

// formatUptime renders a running VM's closing line: hours+minutes below a
// day, days+hours at or above one — matching the "up 2h14m" style set out in
// the plan mockup.
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
// keeps it readable, matching the "last used 3d ago" / "6 weeks ago" style
// from the plan.
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
