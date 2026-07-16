package ui

// header.go is the board's pinned header band: the fleet count and a LIVE host
// readout — what the sandboxes are actually using, right now, against what this
// machine has.
//
// It reports USE, not ALLOCATION. The allocations (vm.VM's CPUs and Memory) are
// what each VM was handed at create time: they never move, they cannot answer
// "what is my machine doing", and summed across an idle fleet they read as a
// crisis that is not happening. The live numbers come from the guest heartbeat,
// the same and only source the tiles' gauges use — so the header and the tiles
// can never disagree, and neither invents a number it does not have.

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// buildVersion is what the header's title row shows on the right. main sets it from
// the release tag GoReleaser stamps, falling back to the embedded git revision (see
// internal/version). It is a package var rather than a constructor argument because
// every test that renders the header would otherwise have to carry a version it does
// not care about — and the goldens pin it (pinVersion) so a build from a different
// commit cannot break them.
var buildVersion = "dev"

// SetVersion tells the TUI which build it is. Called once from main.
func SetVersion(v string) { buildVersion = v }

// compactPrefix folds the title into the counts line when classify sheds the
// title row. It is charged against the counts' width budget, not rendered on top
// of it — a prefix outside the budget is a line seven cells wider than the band
// it lives in, and lipgloss will happily paint it past the right edge of the
// terminal.
const compactPrefix = "sand · "

// headerView renders the pinned header band, HeaderHeight lines exactly:
// full (a title line plus the counts) when the terminal is tall enough, or
// compact (folded onto one line) once classify sheds the title (layout.go),
// PLUS task 10's per-profile band/banner rows — however many
// m.layout.HeaderBandLines was actually granted (see headerBandLines). Every
// line is clipped to ContentWidth, the same honest clip the footer and the
// activity line take.
func (m model) headerView() string {
	var lines []string
	if m.layout.HeaderFull {
		lines = append(lines, m.clipLine(m.titleRow()),
			m.clipLine(statusStyle.Render(m.headerCounts(m.layout.ContentWidth))))
	} else {
		// Compact: the title row is gone, and so is the version with it. The counts
		// and the live host readout are worth more than knowing which build you are
		// on, and at this height there is no room to argue about it.
		budget := m.layout.ContentWidth - ansi.StringWidth(compactPrefix)
		lines = append(lines, m.clipLine(statusStyle.Render(compactPrefix+m.headerCounts(budget))))
	}
	lines = append(lines, m.headerBandLines()...)
	return strings.Join(lines, "\n")
}

// titleRow is "sand" on the left and the build on the right, which is the one
// question a bug report always needs and a user can never answer. The version is
// dropped entirely rather than squeezed when the terminal cannot hold both — a
// truncated commit hash is worse than no commit hash.
func (m model) titleRow() string {
	title := titleStyle.Render("sand")
	ver := statusStyle.Render(buildVersion)
	gap := m.layout.ContentWidth - ansi.StringWidth(title) - ansi.StringWidth(ver)
	if gap < 1 {
		return title
	}
	return title + strings.Repeat(" ", gap) + ver
}

// headerCounts assembles the header's one line: the fleet counts, then the live
// host readout beside them if it fits. The whole line is then clipped, honestly,
// to ContentWidth as a last-resort safety net for a pathologically narrow
// terminal; in the realistic range (80 columns and up) that clip never fires,
// because the capacity clause is dropped first.
func (m model) headerCounts(width int) string {
	if width < 1 {
		width = 1
	}
	core := m.fleetCountsText()
	// When per-profile status bands are actually RENDERED (layout granted them
	// lines), each member's host readout lives in its own band — repeating the
	// active member's stats up here would print the local line twice. Gate on
	// the GRANT, not desiredHeaderBands: a short terminal that sheds the bands
	// falls back to this combined line, so the stats appear exactly once either
	// way. A single-member fleet desires no bands and keeps today's one-liner.
	if m.layout.HeaderBandLines == 0 {
		if capacity := m.hostCapacityText(); capacity != "" {
			sep := " · "
			room := width - ansi.StringWidth(core) - ansi.StringWidth(sep)
			if room >= ansi.StringWidth(capacity) {
				core += sep + capacity
			}
		}
	}
	return ansi.Truncate(core, width, "…")
}

// fleetCountsText is "N sandboxes (M running)" — the board's roster (task 08:
// managed clones, plus a VM whose provision job hasn't landed in `limactl
// list` yet), and how many of them Lima (or a build) reports up right now.
// Building counts as running: the guest is up even while Ansible still owns
// the tile's status word.
func (m model) fleetCountsText() string {
	roster := m.boardVMs()
	running := 0
	for _, v := range roster {
		switch m.statusOf(v.scope, v.VM) {
		case statusRunning, statusBuilding:
			running++
		}
	}
	word := "sandboxes"
	if len(roster) == 1 {
		word = "sandbox"
	}
	return fmt.Sprintf("%d %s (%d running)", len(roster), word, running)
}

// hostCapacityText is the header's host readout: what the fleet is ACTUALLY
// USING right now, against what the host has — plus free disk on the volume
// backing Lima's instance store (freeDiskBytes, form.go — the same probe the
// create form already uses, not a second copy of its path resolution).
//
// It reports USE, not ALLOCATION, and the distinction is the whole point. It
// used to sum vm.VM's CPUs and Memory, which are what each VM was GIVEN — a
// number that never moves, cannot answer "what is my machine doing", and reads
// alarmingly (three idle VMs "using" 24GiB of a 15GiB host) precisely when
// nothing is happening. The live numbers come from the guest heartbeat
// (heartbeat.go), the same and only source the tiles' gauges use.
//
// The heartbeat only runs for running VMs, and only while the board is the
// visible screen (the idle gate). So a reading can be genuinely absent, and when
// it is this says so with an em dash rather than printing a 0 that would claim an
// idle fleet — see tileGaugeNoReading, which makes the same refusal on the tile.
func (m model) hostCapacityText() string { return m.hostCapacityTextFor(m.activeMember()) }

// hostCapacityTextFor is hostCapacityText's per-member form (task 10): the
// SAME use-not-allocation arithmetic below, just addressed at whichever
// member the caller names instead of always the active one. hostCapacityText
// (the single-band header's own text) is now a one-line call to this with
// m.activeMember() — bit-identical to the pre-task-10 behaviour for the
// zero-config single-member fleet, which is exactly the parity this task
// promises. headerBandLines is the other caller, one per connected member.
func (m model) hostCapacityTextFor(am fleetMember) string {
	roster := m.boardVMs()

	// Sum only the VMs actually reporting. A VM's CPUPct is a percentage of ITS OWN
	// vCPUs, so scaling by that VM's CPUs converts each guest's private percentage
	// into host vCPUs busy — the only form in which they can be added together, or
	// compared against the host's core count.
	var cpusUsed float64
	var memUsed int64
	active := 0
	haveCPU, haveMem := false, false
	for _, v := range roster {
		if v.scope != am.scope {
			continue
		}
		active++
		s, ok := m.sampleOf(v.scope, v.Name)
		if !ok {
			continue
		}
		if s.HasCPU {
			cpusUsed += s.CPUPct / 100 * float64(v.CPUs)
			haveCPU = true
		}
		if s.HasMem() {
			memUsed += int64(s.MemUsed)
			haveMem = true
		}
	}
	// An EMPTY board is not an absent reading, it is a known zero: no sandboxes are
	// using anything, because there are none. The em dash is reserved for a VM that
	// is up and simply has not reported. The readout stays on an empty board because
	// free disk is precisely what a user wants to see in the moment BEFORE they
	// create their first VM.
	if active == 0 {
		haveCPU, haveMem = true, true
	}

	// NO PROBING HERE. These are sampled in New (once, at startup) and re-sampled by
	// refreshCmd, which runs off the Bubble Tea goroutine. hostMemBytes reads
	// /proc/meminfo and freeDiskBytes walks parent dirs and statfs's the Lima volume;
	// either one on the render path is a blocking syscall ~10 times a second, and on
	// a host where the probe returns 0 a "fall back and probe" guard would never stop
	// firing. Zero simply means unknown, and the clause is dropped.
	hostMem, hostDisk := am.host.mem, am.host.diskFree

	// A remote provider samples the REMOTE host's core count; the local provider
	// leaves it 0 and we read this machine's count.
	hostCPUs := am.host.cpus
	if hostCPUs == 0 {
		hostCPUs = hostCPUsFn()
	}
	var parts []string
	if hostCPUs > 0 {
		if haveCPU {
			// A share of the WHOLE HOST: 100% is every core of this machine pinned. The
			// busy-vCPU total is divided by the host's core count rather than shown raw,
			// so the number answers "how much of my machine are the sandboxes eating"
			// without the reader having to know how many cores they have.
			//
			// It is deliberately NOT the same scale as the tile's cpu gauge, which is a
			// share of that ONE VM's own vCPUs. Both are right, and they differ on
			// purpose: a 2-vCPU VM pinning both cores reads 100% on its tile and 13% up
			// here, on a 16-core host. The tile answers "is this sandbox busy"; the
			// header answers "is my laptop in trouble".
			parts = append(parts, fmt.Sprintf("cpu %.0f%%", cpusUsed/float64(hostCPUs)*100))
		} else {
			parts = append(parts, "cpu —")
		}
	}
	if hostMem > 0 {
		var clause string
		if haveMem {
			clause = fmt.Sprintf("mem %s/%s", humanizeInt(memUsed), humanizeInt(hostMem))
		} else {
			clause = fmt.Sprintf("mem —/%s", humanizeInt(hostMem))
		}
		parts = append(parts, capacityClause(clause, hostMemLow(am.host)))
	}
	if hostDisk > 0 {
		clause := fmt.Sprintf("%s disk free", humanizeInt(hostDisk))
		parts = append(parts, capacityClause(clause, hostDiskLow(am.host)))
	}
	return strings.Join(parts, ", ")
}

// capacityClause is hostCapacityTextFor's per-clause twin of tileWarnGaugeLine
// (tile.go): a starved clause gets the single-cell "⚠ " marker prefixed and
// the WHOLE clause rendered in warnStyle — the repo's one warning colour —
// instead of the header's ordinary dim grey, so a starved host is a STEADY
// VISUAL STATE on the band rather than a Messages-log line that scrolls away.
// warn is computed by the caller from hostMemLow/hostDiskLow (hostwarn.go),
// the SAME condition that latches the Messages warning, never re-derived
// here — so the band and the log can never quietly disagree about what
// counts as low, even though the clause DISPLAYS a different pair of numbers
// (guest-use/total) than the ones the warn condition is computed from (host
// memAvail/mem, diskFree/diskTotal).
//
// Marker + clause is a single string handed to warnStyle.Render, exactly one
// escape-open/reset pair — ansi.StringWidth (every caller's width budget:
// headerCounts' room calc, m.clipLine's truncation) measures its display
// width correctly regardless, since it already ignores SGR codes.
func capacityClause(clause string, warn bool) string {
	if !warn {
		return clause
	}
	return warnStyle.Render("⚠ " + clause)
}

// hostMemBytesFn/hostDiskFreeFn indirect the header's two host-capacity
// probes (hostMemBytes, hostres_linux.go/hostres_darwin.go; freeDiskBytes,
// form.go) through package-level function variables, so tests can pin
// deterministic values. Real host RAM/disk numbers are not portable across
// machines — exactly the reason the create form's host-derived defaults get
// a behavioural assertion instead of a golden (see teatest_test.go's note) —
// but the header's OWN goldens need exact, reproducible text, so it gets a
// seam instead of skipping the golden.
var (
	hostMemBytesFn = hostMemBytes
	hostDiskFreeFn = freeDiskBytes
	hostCPUsFn     = runtime.NumCPU

	// hostDiskTotalFn is hostDiskFreeFn's total-side companion (totalDiskBytes,
	// form.go) — the local fallback refreshCmd (commands.go) uses when the
	// member's provider reports DiskTotalBytes==0 (local Lima always does; the
	// UI samples this machine directly). Needed for the host-disk
	// low-capacity warning (hostwarn.go), which cannot compute a free%
	// without a denominator.
	hostDiskTotalFn = totalDiskBytes

	// hostMemAvailFn indirects hostMemAvailBytes (hostwarn.go) through a
	// package-level function variable, exactly like the three above — so a
	// teatest golden that boots the REAL refreshCmd (which calls it through a
	// member's real HostFiles) is not at the mercy of the actual test
	// machine's real /proc/meminfo, which is exactly as unportable a number as
	// the ones already pinned (see pinHostCapacity, teatest_test.go).
	hostMemAvailFn = hostMemAvailBytes
)

// humanizeInt is humanizeBytes (format.go) for an already-summed int64, so
// the header's arithmetic goes through the SAME size formatting as every
// other byte count in the TUI rather than a second copy of it.
func humanizeInt(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	return humanizeBytes(strconv.FormatInt(n, 10))
}

// desiredHeaderBands is how many EXTRA header rows (task 10) the fleet wants
// right now: one per member that is connected, disabled, or errored — the
// three states memberStatusLine has something to say about. A connecting
// member says nothing here (fleetConnectingBanner already covers that span,
// while the board is otherwise empty).
//
// The zero-config single-member fleet always wants ZERO extra bands: its one
// capacity clause folds into headerCounts (hostCapacityText), exactly as
// before this task, which is the single-profile parity this task promises.
// applySize (model.go) calls this on every resize AND every member state
// transition, so classifyWithHeaderBands' budget tracks the fleet, not just
// the terminal size.
func (m model) desiredHeaderBands() int {
	if len(m.members) <= 1 {
		return 0
	}
	n := 0
	for i := range m.members {
		switch m.members[i].state {
		case connConnected, connDisabled, connErrored:
			n++
		}
	}
	return n
}

// memberStatusLine is one fleet member's header row, unstyled and
// unclipped: a stats band (mem's own host cpu/mem/disk, hostCapacityTextFor)
// while connected, or a banner naming the reason its tiles are absent while
// disabled/errored. false while connecting — there is nothing to say about a
// member still making its first connection here (see desiredHeaderBands).
func (m model) memberStatusLine(mem fleetMember) (string, bool) {
	switch mem.state {
	case connConnected:
		if cap := m.hostCapacityTextFor(mem); cap != "" {
			return mem.profile.Name + ": " + cap, true
		}
		return mem.profile.Name + ": —", true
	case connDisabled:
		return mem.profile.Name + ": disabled", true
	case connErrored:
		if mem.lastErr != nil {
			return mem.profile.Name + ": error: " + mem.lastErr.Error(), true
		}
		return mem.profile.Name + ": error", true
	default:
		return "", false
	}
}

// headerBandLines renders task 10's per-profile rows: one line per connected
// member (that member's own host cpu/mem/disk) and one BANNER line per
// disabled/errored member (naming the profile and why its tiles are gone),
// so a mixed fleet's status bar grows from the single capacity clause
// headerCounts folds in for the zero-config default.
//
// Degradation at the narrowest supported terminal (80 columns) is two
// independent, explicit rules:
//
//   - WIDTH: each line is truncated (with an ellipsis) to ContentWidth via
//     m.clipLine — exactly the same honest clip every other header/footer
//     line already takes. A long connection error is cut, never wrapped or
//     left to overhang the terminal.
//   - HEIGHT: classifyWithHeaderBands grants at most m.layout.HeaderBandLines
//     rows, budgeted against the same header/messages/footer negotiation the
//     help bar already goes through (layout.go) — bands are shed FIRST when a
//     short terminal cannot afford everything. When the fleet has more lines
//     to say than were granted, the LAST granted row summarizes the rest
//     ("+K more") instead of silently dropping members off the bottom.
func (m model) headerBandLines() []string {
	if len(m.members) <= 1 {
		return nil
	}
	var all []string
	for i := range m.members {
		if line, ok := m.memberStatusLine(m.members[i]); ok {
			all = append(all, line)
		}
	}
	budget := m.layout.HeaderBandLines
	if budget <= 0 || len(all) == 0 {
		return nil
	}
	if len(all) <= budget {
		out := make([]string, len(all))
		for i, s := range all {
			out[i] = m.clipLine(statusStyle.Render(s))
		}
		return out
	}
	// More to say than the layout granted rows for: show as many in full as fit
	// alongside a summary line, and fold the rest into that final "+K more" row
	// rather than truncate the member list itself.
	shown := budget - 1
	if shown < 0 {
		shown = 0
	}
	out := make([]string, 0, budget)
	for _, s := range all[:shown] {
		out = append(out, m.clipLine(statusStyle.Render(s)))
	}
	out = append(out, m.clipLine(statusStyle.Render(fmt.Sprintf("+%d more", len(all)-shown))))
	return out
}
