package ui

// header.go is the board's pinned header band: fleet counts, host capacity
// (the "am I about to over-commit?" readout), and — the one line that is not
// optional — the hidden count.
//
// Task 08 filtered the board to sand-managed clones only, unconditionally: no
// base image and no unmanaged VM gets a tile, and there is no toggle to bring
// either back. That is a real cost — the TUI can no longer show the user
// everything `sand` put on their host, and a stale base image is HEAVY (multi
// gigabyte) and now invisible and unmanageable from here — and the plan
// accepted it ONLY on the condition that the header says what is missing,
// e.g. "3 sandboxes · 1 base, 2 external hidden". That is the ENTIRE
// mitigation. A header that drops the count to fit a narrower terminal has
// re-opened the exact hole the plan closed, which is why hostCapacityText
// (the most sheddable clause) is the one that gives way, never the hidden
// count — see headerCounts.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// headerView renders the pinned header band, HeaderHeight lines exactly:
// full (a title line plus the counts) when the terminal is tall enough, or
// compact (folded onto one line) once classify sheds the title (layout.go).
// Either way the counts line carries the hidden count — see headerCounts.
func (m model) headerView() string {
	counts := m.headerCounts()
	if m.layout.HeaderFull {
		return titleStyle.Render("sand") + "\n" + statusStyle.Render(counts)
	}
	return statusStyle.Render("sand · " + counts)
}

// headerCounts assembles the header's one line of truth, in priority order:
// fleet counts and the hidden count are ALWAYS included in full; host
// capacity — useful, but the least essential of the three — is appended only
// if it fits beside them. The whole line is then clipped, honestly, to
// ContentWidth as a last-resort safety net for a pathologically narrow
// terminal; in the realistic range (80 columns and up) that clip never
// touches the fleet/hidden clauses, because capacity is what gave way first.
func (m model) headerCounts() string {
	width := m.layout.ContentWidth
	fleet := m.fleetCountsText()
	hidden := hiddenCountText(m.hiddenCounts())

	core := fleet
	if hidden != "" {
		core += " · " + hidden
	}
	if capacity := m.hostCapacityText(); capacity != "" {
		sep := " · "
		room := width - ansi.StringWidth(core) - ansi.StringWidth(sep)
		if room >= ansi.StringWidth(capacity) {
			// Splice capacity in AFTER fleet but BEFORE hidden, so the hidden
			// clause — the header's whole reason for existing — always reads
			// as the line's last word, exactly as the plan's own example
			// ("… · 1 base, 2 external hidden") shows it.
			if hidden != "" {
				core = fleet + sep + capacity + sep + hidden
			} else {
				core = fleet + sep + capacity
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

// hostCapacityText is the "am I about to over-commit?" readout: the ON-BOARD
// fleet's allocated vCPUs/RAM against the host's own (hostMemBytes,
// hostres_linux.go/hostres_darwin.go), plus free disk on the volume backing
// Lima's instance store (freeDiskBytes, form.go — the same probe the create
// form already uses, not a second copy of its path resolution). Every size
// string here goes through humanizeBytes (format.go), same as everywhere
// else in the TUI. "" when nothing is on the board yet — an empty fleet has
// no capacity claim to make.
func (m model) hostCapacityText() string {
	roster := m.boardVMs()
	if len(roster) == 0 {
		return ""
	}
	var cpus int
	var memBytes int64
	for _, v := range roster {
		cpus += v.CPUs
		memBytes += parseVMBytes(v.Memory)
	}

	parts := []string{fmt.Sprintf("%d vCPU", cpus)}
	if total := hostMemBytesFn(); total > 0 {
		parts = append(parts, fmt.Sprintf("%s/%s RAM", humanizeInt(memBytes), humanizeInt(total)))
	} else {
		parts = append(parts, fmt.Sprintf("%s RAM alloc", humanizeInt(memBytes)))
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
)

// hiddenCounts reports how many of the VMs Lima actually knows about (m.vms,
// the ground truth for "what's really on this host") get NO tile: split into
// base images (clone sources, kept off the board by design) and everything
// else unmanaged ("external" — someone else's VM, or a stray). A VM the
// roster DOES show (a managed clone, or one mid-build) is never counted here,
// whatever `limactl list` currently says about it.
func (m model) hiddenCounts() (base, external int) {
	onBoard := make(map[string]bool, len(m.vms))
	for _, v := range m.boardVMs() {
		onBoard[v.Name] = true
	}
	for _, v := range m.vms {
		if onBoard[v.Name] {
			continue
		}
		if m.isBaseImage(v.Name) {
			base++
		} else {
			external++
		}
	}
	return base, external
}

// hiddenCountText renders the header's honesty clause in the plan's own
// wording ("1 base, 2 external hidden"). "" when nothing is hidden, so a host
// with nothing but managed clones doesn't grow a permanent "0 hidden" tail.
func hiddenCountText(base, external int) string {
	if base == 0 && external == 0 {
		return ""
	}
	var parts []string
	if base > 0 {
		parts = append(parts, fmt.Sprintf("%d base", base))
	}
	if external > 0 {
		parts = append(parts, fmt.Sprintf("%d external", external))
	}
	return strings.Join(parts, ", ") + " hidden"
}

// parseVMBytes reads a vm.VM.Memory-shaped raw byte count (Lima's
// `list --format json`, e.g. "8589934592"). A value that isn't a plain
// non-negative integer (empty, or an already-formatted size like the create
// form's "8GiB") contributes 0 rather than corrupting the sum.
func parseVMBytes(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// humanizeInt is humanizeBytes (format.go) for an already-summed int64, so
// the header's arithmetic goes through the SAME size formatting as every
// other byte count in the TUI rather than a second copy of it.
func humanizeInt(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	return humanizeBytes(strconv.FormatInt(n, 10))
}
