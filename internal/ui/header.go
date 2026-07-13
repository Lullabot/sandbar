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

// compactPrefix folds the title into the counts line when classify sheds the
// title row. It is charged against the counts' width budget, not rendered on top
// of it — a prefix outside the budget is a line seven cells wider than the band
// it lives in, and lipgloss will happily paint it past the right edge of the
// terminal.
const compactPrefix = "sand · "

// headerView renders the pinned header band, HeaderHeight lines exactly:
// full (a title line plus the counts) when the terminal is tall enough, or
// compact (folded onto one line) once classify sheds the title (layout.go).
// Every line is clipped to ContentWidth, the same honest clip the footer and the
// activity line take.
func (m model) headerView() string {
	if m.layout.HeaderFull {
		return m.clipLine(titleStyle.Render("sand")) + "\n" +
			m.clipLine(statusStyle.Render(m.headerCounts(m.layout.ContentWidth)))
	}
	budget := m.layout.ContentWidth - ansi.StringWidth(compactPrefix)
	return m.clipLine(statusStyle.Render(compactPrefix + m.headerCounts(budget)))
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
	if capacity := m.hostCapacityText(); capacity != "" {
		sep := " · "
		room := width - ansi.StringWidth(core) - ansi.StringWidth(sep)
		if room >= ansi.StringWidth(capacity) {
			core += sep + capacity
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
		switch m.statusOf(v) {
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
func (m model) hostCapacityText() string {
	roster := m.boardVMs()

	// Sum only the VMs actually reporting. A VM's CPUPct is a percentage of ITS OWN
	// vCPUs, so scaling by that VM's CPUs converts each guest's private percentage
	// into host vCPUs busy — the only form in which they can be added together, or
	// compared against the host's core count.
	var cpusUsed float64
	var memUsed int64
	// An EMPTY board is not an absent reading, it is a known zero: no sandboxes are
	// using anything, because there are no sandboxes. The em dash is reserved for a
	// VM that is up and simply has not reported. The readout stays on an empty board
	// because free disk is precisely what a user wants to see in the moment BEFORE
	// they create their first VM.
	haveCPU, haveMem := len(roster) == 0, len(roster) == 0
	for _, v := range roster {
		s, ok := m.sampleOf(v.Name)
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

	var parts []string
	if hostCPUs := hostCPUsFn(); hostCPUs > 0 {
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
	if hostMem := hostMemBytesFn(); hostMem > 0 {
		if haveMem {
			parts = append(parts, fmt.Sprintf("mem %s/%s", humanizeInt(memUsed), humanizeInt(hostMem)))
		} else {
			parts = append(parts, fmt.Sprintf("mem —/%s", humanizeInt(hostMem)))
		}
	}
	if free := hostDiskFreeFn(); free > 0 {
		parts = append(parts, fmt.Sprintf("%s disk free", humanizeInt(free)))
	}
	return strings.Join(parts, ", ")
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
