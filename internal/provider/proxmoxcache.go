package provider

// proxmoxcache.go bounds what the TUI's polling costs a Proxmox node.
//
// The board polls a provider on a timer (a fleet refresh every few seconds,
// and a utilization heartbeat per running VM), and every one of those calls
// used to become its own API request: per refresh, a cluster listing for List,
// another for Provenance, a config read PER MANAGED VM, the node and storage
// status, and the storage content listing; per heartbeat sample, a
// status/current PER RUNNING VM. With 22 VMs that is ~27 requests per refresh
// plus 11 per second of heartbeat — a load that grew with the fleet and that
// helped push a real node's pveproxy into file-descriptor exhaustion.
//
// The rules here make a steady-state refresh cost ONE request regardless of
// fleet size:
//
//   - One /cluster/resources listing serves List, Provenance, the name->VMID
//     index and every VM's host memory reading. It already carries status,
//     cpu, mem, maxmem, disk and uptime for every guest, so a per-VM
//     status/current is never needed to draw the board. Concurrent callers
//     share one in-flight fetch (single-flight), and a listing younger than the
//     caller's max age is reused.
//   - Facts that change only when someone acts — a VM's provenance marker
//     (its description), the storage's per-VM allocation, the node's capacity —
//     are cached with long TTLs and invalidated by this provider's own
//     mutations. A cold provenance cache is filled in full (an absent marker
//     would hide a VM's tile), but expired entries are revalidated a few per
//     call, so a large fleet's markers trickle in instead of arriving as one
//     burst.
//   - A guest agent that fails is not asked again for a while (agentBackoff):
//     an agent-less VM otherwise costs three requests every time a heartbeat
//     or checkout sweep tries to reach it.
//
// Lifecycle verbs (Get/Status/Start/Stop/Delete) are NOT served from these
// caches: they keep their verified, live per-VM reads (resolve), because acting
// on a stale answer there is a correctness problem, not a freshness one.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/lullabot/sandbar/internal/pve"
)

// Cache tunables. Vars so tests can shrink or pin them.
var (
	// listingMaxAge is how old a cluster listing List, Provenance and
	// lookupVMID will accept. Short enough that every board refresh (5s) sees
	// a fresh listing; long enough that List and Provenance in the SAME
	// refresh share one.
	listingMaxAge = 2 * time.Second

	// hostMemMaxAge is how old a listing VMHostMemory will accept. The
	// heartbeat asks every couple of seconds per VM; PVE itself only refreshes
	// a guest's figures periodically, so a few seconds of age costs no
	// accuracy, and it means the heartbeat rides the refresh's listing instead
	// of adding its own.
	hostMemMaxAge = 10 * time.Second

	// diskUsedTTL bounds the storage content listing's age. Allocation changes
	// only on create, delete and resize; this provider's own create/delete
	// invalidate it.
	diskUsedTTL = time.Minute

	// hostResourcesTTL bounds the node/storage status's age. Capacity is
	// effectively static and free disk moves slowly.
	hostResourcesTTL = 30 * time.Second

	// provenanceTTL is how long a READY VM's marker is trusted before it is
	// re-read. Markers are written by this provider (which updates the cache
	// itself) or by another sandbar controller finishing a build — the latter
	// is the only change this TTL has to catch.
	provenanceTTL = 5 * time.Minute

	// provenanceBuildingTTL is the much shorter TTL for a marker that says a
	// build is IN FLIGHT, so another controller's build progress stays live.
	provenanceBuildingTTL = 10 * time.Second

	// provenanceRevalidatePerCall caps how many EXPIRED (but cached) markers
	// one Provenance call re-reads; the rest keep their cached value until a
	// later call. Missing markers are never deferred.
	provenanceRevalidatePerCall = 2

	// agentBackoffBase/agentBackoffMax bound how long a VM whose guest agent
	// just failed is left alone by the board's address lookups.
	agentBackoffBase = 30 * time.Second
	agentBackoffMax  = 5 * time.Minute
)

// proxmoxCache holds every polled fact. Its own mutex, not the provider's mu:
// a cache fill must never wait behind (or block) a lifecycle call's index
// updates.
type proxmoxCache struct {
	mu sync.Mutex

	// The cluster listing. snapGen is bumped by invalidate, so a fetch that
	// was already in flight when a mutation happened cannot install its
	// pre-mutation listing as fresh.
	snap      []pve.VMResource
	snapAt    time.Time
	snapValid bool
	snapGen   uint64
	flight    *listingFlight

	disk      map[int]int64
	diskAt    time.Time
	diskValid bool

	host      HostResources
	hostAt    time.Time
	hostValid bool

	prov map[int]provenanceEntry

	agent map[string]agentState
}

type listingFlight struct {
	done chan struct{}
	res  []pve.VMResource
	err  error
}

// provenanceEntry is one VM's cached marker read. ok=false caches "tagged but
// carries no readable block" just as firmly as a marker.
type provenanceEntry struct {
	name    string
	pv      Provenance
	ok      bool
	fetched time.Time
}

func (e provenanceEntry) expired(now time.Time) bool {
	ttl := provenanceTTL
	if e.ok && e.pv.Provisioning {
		ttl = provenanceBuildingTTL
	}
	return now.Sub(e.fetched) >= ttl
}

// agentState is one VM's guest-agent failure streak.
type agentState struct {
	failures int
	until    time.Time
	err      error
}

// invalidate drops everything a lifecycle change can make wrong: the listing
// (status, tags, membership), the disk allocation and the host's free space.
// Provenance entries are per VM and updated by their own writers.
func (c *proxmoxCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapValid = false
	c.snapGen++
	c.flight = nil
	c.diskValid = false
	c.hostValid = false
}

// listing returns the pool's cluster listing, no older than maxAge. Callers
// must treat the slice as READ-ONLY: it is shared with every other caller
// until the next fetch replaces it.
func (p *proxmoxProvider) listing(ctx context.Context, maxAge time.Duration) ([]pve.VMResource, error) {
	c := &p.cache
	c.mu.Lock()
	if c.snapValid && time.Since(c.snapAt) <= maxAge {
		res := c.snap
		c.mu.Unlock()
		return res, nil
	}
	if f := c.flight; f != nil {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.res, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := &listingFlight{done: make(chan struct{})}
	c.flight = f
	gen := c.snapGen
	c.mu.Unlock()

	res, err := p.client.ListVMs(ctx, p.pool)

	c.mu.Lock()
	if c.flight == f {
		c.flight = nil
	}
	if err == nil && gen == c.snapGen {
		c.snap, c.snapAt, c.snapValid = res, time.Now(), true
	}
	c.mu.Unlock()
	f.res, f.err = res, err
	close(f.done)
	return res, err
}

// invalidateListing is the hook every lifecycle mutation calls.
func (p *proxmoxProvider) invalidateListing() { p.cache.invalidate() }

// cachedDiskUsed returns the per-VM allocation index, refetching the storage
// content listing only when the cached one is older than diskUsedTTL. A failed
// fetch keeps serving the previous index (an allocation figure that is a
// minute stale beats an absent one) and caches nothing new.
func (p *proxmoxProvider) cachedDiskUsed(ctx context.Context) map[int]int64 {
	if p.storage == "" {
		return nil
	}
	c := &p.cache
	c.mu.Lock()
	if c.diskValid && time.Since(c.diskAt) < diskUsedTTL {
		d := c.disk
		c.mu.Unlock()
		return d
	}
	prev := c.disk
	c.mu.Unlock()

	items, err := p.client.StorageContent(ctx, p.storage)
	if err != nil {
		return prev
	}
	d := diskUsedByVMID(items)
	c.mu.Lock()
	c.disk, c.diskAt, c.diskValid = d, time.Now(), true
	c.mu.Unlock()
	return d
}

// cachedHostResources serves HostResources from cache for hostResourcesTTL.
// Only a reading with real numbers is cached, so a failed sample is retried
// on the next refresh rather than pinned as zeros.
func (p *proxmoxProvider) cachedHostResources(fetch func() HostResources) HostResources {
	c := &p.cache
	c.mu.Lock()
	if c.hostValid && time.Since(c.hostAt) < hostResourcesTTL {
		hr := c.host
		c.mu.Unlock()
		return hr
	}
	c.mu.Unlock()

	hr := fetch()
	if hr.CPUs > 0 || hr.MemBytes > 0 || hr.DiskTotalBytes > 0 {
		c.mu.Lock()
		c.host, c.hostAt, c.hostValid = hr, time.Now(), true
		c.mu.Unlock()
	}
	return hr
}

// --- provenance -----------------------------------------------------------------

// provenanceWork splits the candidates into the ones to fetch now and the
// cached answers to serve, and prunes entries for VMs no longer tagged.
func (p *proxmoxProvider) provenanceWork(candidates []provenanceCandidate, now time.Time) (fetch []provenanceCandidate, out map[string]Provenance) {
	c := &p.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.prov == nil {
		c.prov = map[int]provenanceEntry{}
	}
	live := make(map[int]bool, len(candidates))
	out = make(map[string]Provenance, len(candidates))
	var expired []provenanceCandidate
	for _, cand := range candidates {
		live[cand.vmid] = true
		e, ok := c.prov[cand.vmid]
		switch {
		case !ok || e.name != cand.name:
			// Cold, or the VMID now names a different VM: never serve another
			// VM's marker, and never defer — an absent marker hides a tile.
			fetch = append(fetch, cand)
		case e.expired(now):
			expired = append(expired, cand)
			if e.ok {
				out[cand.name] = e.pv // served stale until revalidated
			}
		default:
			if e.ok {
				out[cand.name] = e.pv
			}
		}
	}
	for vmid := range c.prov {
		if !live[vmid] {
			delete(c.prov, vmid)
		}
	}
	// Oldest first, so every expired entry is eventually revalidated.
	slices.SortFunc(expired, func(a, b provenanceCandidate) int {
		return c.prov[a.vmid].fetched.Compare(c.prov[b.vmid].fetched)
	})
	if len(expired) > provenanceRevalidatePerCall {
		expired = expired[:provenanceRevalidatePerCall]
	}
	return append(fetch, expired...), out
}

// storeProvenance records one fetched marker (ok=false: tagged, no block).
func (p *proxmoxProvider) storeProvenance(vmid int, name string, pv Provenance, ok bool) {
	c := &p.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.prov == nil {
		c.prov = map[int]provenanceEntry{}
	}
	c.prov[vmid] = provenanceEntry{name: name, pv: pv, ok: ok, fetched: time.Now()}
}

// dropProvenance forgets one VM's cached marker, so the next read fetches it.
func (p *proxmoxProvider) dropProvenance(vmid int) {
	c := &p.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.prov, vmid)
}

// IsDegraded reports whether err means a backend's API is not answering — a
// proxy/gateway 5xx, a transport failure, or the client backing off after
// one — as opposed to answering with an error. The board shows such a member
// as degraded and retrying, not as broken: nothing about its configuration is
// wrong, and the client is already pacing its retries. It exists so the UI
// can ask without importing a backend's client package.
func IsDegraded(err error) bool { return pve.IsUnavailable(err) }

// --- guest agent backoff ---------------------------------------------------------

// isAgentFailure reports whether err is the guest agent itself failing (PVE
// answers every agent failure with an HTTP 500 whose message says which), as
// opposed to the API or the network failing.
func isAgentFailure(err error) bool {
	var ae *pve.APIError
	return errors.As(err, &ae) && ae.Status == http.StatusInternalServerError && pve.AgentUnavailableReason(err) != ""
}

// agentBackoffErr returns an error while name's agent is in its backoff
// window, without touching the API.
func (p *proxmoxProvider) agentBackoffErr(name string) error {
	c := &p.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.agent[name]
	if !ok {
		return nil
	}
	if wait := time.Until(st.until); wait > 0 {
		return fmt.Errorf("proxmox: %s's guest agent is not answering; not asking again for %s (last error: %w)", name, wait.Round(time.Second), st.err)
	}
	return nil
}

// noteAgentFailure extends name's backoff after an agent failure: base
// doubled per consecutive failure, capped.
func (p *proxmoxProvider) noteAgentFailure(name string, err error) {
	c := &p.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.agent == nil {
		c.agent = map[string]agentState{}
	}
	st := c.agent[name]
	st.failures++
	wait := agentBackoffBase
	for i := 1; i < st.failures && wait < agentBackoffMax; i++ {
		wait *= 2
	}
	if wait > agentBackoffMax {
		wait = agentBackoffMax
	}
	st.until, st.err = time.Now().Add(wait), err
	c.agent[name] = st
}

// clearAgentFailure ends name's backoff: its agent answered, or the VM went
// through a power transition (a fresh boot gets a fresh chance).
func (p *proxmoxProvider) clearAgentFailure(name string) {
	c := &p.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.agent, name)
}
