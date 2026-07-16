package ui

// hostwarn.go implements the HOST half of the low-capacity-warning feature: an
// edge-triggered line in the session's Messages log (messages.go) the moment a
// CONNECTED fleet member's host memory or disk drops below lowFreeThreshold
// (tile.go) free, and a re-arm the instant it recovers — so the 5s refresh
// loop (refresh.go) cannot spam the ring with the same warning every tick
// while a host sits below the line. The VM-tile half (a per-VM mem/disk
// badge) lives in tile.go.
//
// # Where the numbers come from
//
//   - Memory free: /proc/meminfo's MemAvailable (never MemFree — see
//     guestSample's own doc comment on why: MemFree excludes the page cache
//     and reads alarmingly low on a host that has simply been up a while),
//     read through the member's OWN HostFiles (hostMemAvailBytes) — the exact
//     seam fleet.go's hostFiles doc describes: the local filesystem for local
//     Lima, or a `cat` over a remote member's ssh connection, now multiplexed
//     (see ControlMaster) and so cheap. /proc/meminfo does not exist on a
//     macOS host, local or remote — ReadFile then errors, hostMemAvailBytes
//     reports false, and this member gets NO memory warning: never guessed
//     from the total alone.
//   - Disk free: unchanged (statfs's Bavail locally, `df -Pk`'s free column
//     over ssh — commands.go's refreshCmd). Disk TOTAL is new: statfs's
//     Blocks locally (hostDiskTotalBytes), `df -Pk`'s total column over ssh
//     (lima.SSHHost.HostResources), sampled beside the free reading that
//     already existed so a free% is computable for both without a second
//     host round trip.
//
// A member's host sample (fleet.go's hostSample) holds all four numbers; a
// ZERO field means "not sampled", exactly like its two original siblings
// (mem, diskFree) — and every check below refuses to compute a percentage
// from one, per the same "absent reading, no warning" rule the tile badges
// follow.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lullabot/sandbar/internal/lima"
)

// checkHostCapacityWarn is model.go's vmsLoadedMsg success-branch hook: called
// once per successful list of a CONNECTED member, with that member's
// freshly-adopted host sample already on it (mem is the pointer straight into
// m.members, so the latch fields set here persist). This is rules 1 and 2 of
// the low-capacity-warning feature.
func (m *model) checkHostCapacityWarn(mem *fleetMember) {
	m.checkHostMemWarn(mem)
	m.checkHostDiskWarn(mem)
}

// hostMemHasReading/hostDiskHasReading report whether host carries both
// numbers a low-memory/low-disk check needs — the "zero means never sampled,
// never guess" rule every hostSample field follows (fleet.go).
func hostMemHasReading(host hostSample) bool  { return host.mem > 0 && host.memAvail > 0 }
func hostDiskHasReading(host hostSample) bool { return host.diskFree > 0 && host.diskTotal > 0 }

// hostMemLow reports whether host's available memory is below
// lowFreeThreshold free, given a reading — the exact condition
// checkHostMemWarn (below) latches its once-per-crossing Messages-log warning
// on. header.go's band/counts clause highlighting calls this SAME function
// (never a re-derived copy of the fraction check) so the steady visual state
// in the header and the edge-triggered log line can never disagree about
// what counts as low. Callers must guard with hostMemHasReading first: with
// no reading this reports false, same as "not low", which is the right
// answer for the header (no highlight) but NOT distinguishable from "healthy"
// — checkHostMemWarn relies on the reading guard, not this return value, to
// tell the two apart when deciding whether to re-arm its latch.
func hostMemLow(host hostSample) bool {
	if !hostMemHasReading(host) {
		return false
	}
	return float64(host.memAvail)/float64(host.mem) < lowFreeThreshold
}

// hostDiskLow is hostMemLow's disk twin, shared the same way with
// checkHostDiskWarn and header.go's clause highlighting.
func hostDiskLow(host hostSample) bool {
	if !hostDiskHasReading(host) {
		return false
	}
	return float64(host.diskFree)/float64(host.diskTotal) < lowFreeThreshold
}

// checkHostMemWarn edge-triggers rule 1: a warning logged once when mem.host's
// available memory crosses below lowFreeThreshold, latched on
// mem.warnedHostMem (fleet.go) until the member recovers to at least lowFreeThreshold free, at
// which point the latch clears and a later re-crossing warns again.
func (m *model) checkHostMemWarn(mem *fleetMember) {
	if !hostMemHasReading(mem.host) {
		return // one of the two numbers has never been sampled — never guess, and never touch the latch
	}
	if hostMemLow(mem.host) {
		if !mem.warnedHostMem {
			mem.warnedHostMem = true
			m.logWarn(fmt.Sprintf("%s memory low — %s free of %s (<%.0f%%)",
				mem.profile.Name, humanizeInt(mem.host.memAvail), humanizeInt(mem.host.mem), lowFreeThreshold*100))
		}
		return
	}
	mem.warnedHostMem = false // recovered: re-arm for a later crossing
}

// checkHostDiskWarn is checkHostMemWarn's disk twin (rule 2) — applies to any
// CONNECTED member, local included: a full local disk is exactly what a user
// running sand on their own laptop wants to hear about.
func (m *model) checkHostDiskWarn(mem *fleetMember) {
	if !hostDiskHasReading(mem.host) {
		return
	}
	if hostDiskLow(mem.host) {
		if !mem.warnedHostDisk {
			mem.warnedHostDisk = true
			m.logWarn(fmt.Sprintf("%s disk low — %s free of %s (<%.0f%%)",
				mem.profile.Name, humanizeInt(mem.host.diskFree), humanizeInt(mem.host.diskTotal), lowFreeThreshold*100))
		}
		return
	}
	mem.warnedHostDisk = false
}

// hostMemAvailBytes reads MemAvailable off hf's /proc/meminfo — see this
// file's doc comment for why this, rather than Provider.HostResources, is the
// seam: it must resolve identically for local Lima (a direct file read) and a
// remote member (a `cat` over the already-open, multiplexed ssh connection),
// and it must fail closed (0, false) rather than invent a number on any host
// that has no /proc at all (macOS, local or remote) or whose content this
// parser doesn't recognise.
func hostMemAvailBytes(hf lima.HostFiles) (int64, bool) {
	if hf == nil {
		return 0, false
	}
	data, err := hf.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line[len("MemAvailable:"):])
		if len(fields) == 0 {
			return 0, false
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}
