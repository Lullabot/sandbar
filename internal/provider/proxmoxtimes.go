package provider

import (
	"context"
	"time"

	"github.com/lullabot/sandbar/internal/pve"
)

// The tile's closing line ("up 2h14m" / "last used 3d ago") is sampled for
// Lima from files in the instance directory (ha.pid, ha.stderr.log). A Proxmox
// VM's Dir is only sandbar's local state directory, so neither file ever exists
// there: every running Proxmox VM read a bare "up" and every stopped one read
// "never used". This file answers both questions from the PVE API instead, and
// the UI leaves a time the provider already filled alone.

// upSinceFromUptime converts PVE's seconds-since-boot into the boot time. A
// stopped VM reports 0, and so does a VM PVE has not sampled yet, which is
// why 0 yields the zero time ("unknown") rather than "booted just now".
func upSinceFromUptime(uptime int64, now time.Time) time.Time {
	if uptime <= 0 {
		return time.Time{}
	}
	return now.Add(-time.Duration(uptime) * time.Second)
}

// lastUsedTimeframes are the RRD windows searched, shortest first, for a
// stopped VM's last running sample. Each step is consulted only when the one
// before it holds none. With the PVE 9 RRD layout, day has 1-minute rows, month
// 30-minute rows and year 6-hour rows; older nodes use coarser rows. Their
// resolution coarsens about as fast as formatAgo's units do, so the error stays
// below the precision the tile prints. "decade" is deliberately absent: PVE
// before 9 rejects it as an invalid parameter, which would be retried on every
// refresh. A VM idle for more than a year therefore has no known last-used time.
var lastUsedTimeframes = []string{"day", "month", "year"}

// lastUsedLookupsPerCall caps how many uncached VMs one List looks up. A
// recently stopped VM costs one request, but one idle for months costs three,
// so a cold cache over a large stopped fleet would otherwise arrive at the node
// as one burst — the load proxmoxcache.go exists to prevent. Like
// provenanceRevalidatePerCall, the rest are filled in over later refreshes. A
// var so tests can pin it.
var lastUsedLookupsPerCall = 3

// lastUsedAt reports when stopped VM vmid last ran, from its RRD history:
// pvestatd records a stopped VM's cpu as unknown (in both the pve2.3-vm and
// pve-vm-9.0 layouts) and rrddata omits unknown fields, so the last row with a
// cpu reading is the last interval the VM was up.
//
// The answer is cached per VMID because a stopped VM's last-used time cannot
// change until it runs again; refreshLastUsedCache drops the entry the moment a
// listing shows the VM running.
//
// unknown is true when there is no answer yet: the call's lookup budget was
// spent, or a request failed. Neither is cached, so a later refresh retries.
// It must not be reported as a zero time alone, which the tile reads as "never
// used" — the false claim this lookup exists to remove.
func (p *proxmoxProvider) lastUsedAt(ctx context.Context, vmid int, budget *int) (at time.Time, unknown bool) {
	p.mu.Lock()
	cached, ok := p.lastUsed[vmid]
	p.mu.Unlock()
	if ok {
		return cached, false
	}
	if *budget <= 0 {
		return time.Time{}, true
	}
	*budget--
	for _, tf := range lastUsedTimeframes {
		rows, err := p.client.RRDData(ctx, vmid, tf)
		if err != nil {
			return time.Time{}, true
		}
		var found bool
		if at, found = lastRunningSample(rows); found {
			break
		}
	}
	// A history with no running sample in the last year is cached too (as
	// the zero time, read as "never used"), so a never-started VM does not
	// cost three requests on every refresh.
	p.mu.Lock()
	if p.lastUsed == nil {
		p.lastUsed = map[int]time.Time{}
	}
	p.lastUsed[vmid] = at
	p.mu.Unlock()
	return at, false
}

// lastRunningSample returns the END of the last interval in rows that has a
// cpu reading. A row's time is the start of its interval, so the end is the
// start of the following row; that is when the VM was last seen up. When the
// running row is the final one there is no following row, and its own start
// is the best available answer.
func lastRunningSample(rows []pve.RRDPoint) (time.Time, bool) {
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].CPU == nil {
			continue
		}
		if i+1 < len(rows) {
			return time.Unix(rows[i+1].Time, 0), true
		}
		return time.Unix(rows[i].Time, 0), true
	}
	return time.Time{}, false
}

// refreshLastUsedCache keeps the cache consistent with a fresh listing. A VM
// seen running has a new last-used time from the moment it stops, and a VMID
// that is no longer listed may be reallocated to a different VM, so both
// entries are dropped.
func (p *proxmoxProvider) refreshLastUsedCache(resources []pve.VMResource, index map[string]int) {
	keep := make(map[int]bool, len(index))
	for _, r := range resources {
		if id, ok := index[r.Name]; ok && id == r.VMID && r.Status != pveRunning {
			keep[r.VMID] = true
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for id := range p.lastUsed {
		if !keep[id] {
			delete(p.lastUsed, id)
		}
	}
}
