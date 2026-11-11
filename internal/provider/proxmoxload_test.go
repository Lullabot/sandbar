package provider

// proxmoxload_test.go pins the request budget the board's polling may cost a
// Proxmox node (proxmoxcache.go). The counts are taken at the mock PVE server,
// i.e. what would actually reach pveproxy — not what the provider believes it
// sent.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fleetMock serves a pool of n running, sandbar-managed VMs (VMIDs 100..),
// each with a ready provenance marker, plus the node/storage endpoints a
// refresh reads.
func fleetMock(t *testing.T, n int) *pveMock {
	t.Helper()
	m := newPVEMock(t)
	var res []string
	payload, err := json.Marshal(Provenance{SchemaVersion: MarkerSchemaVersion, Base: "sandbar-base"})
	if err != nil {
		t.Fatal(err)
	}
	desc := provenanceBeginMarker + "\n" + string(payload) + "\n" + provenanceEndMarker
	for i := range n {
		vmid := 100 + i
		res = append(res, fmt.Sprintf(`{"vmid":%d,"name":"vm%d","node":"pve1","pool":"sandbar","status":"running","type":"qemu","tags":"sandbar","mem":1073741824,"maxmem":4294967296,"maxcpu":2}`, vmid, i))
		cfg, _ := json.Marshal(map[string]string{"description": desc, "tags": "sandbar"})
		m.data(fmt.Sprintf("/nodes/pve1/qemu/%d/config", vmid), string(cfg))
	}
	m.data("/cluster/resources", "["+strings.Join(res, ",")+"]")
	m.data("/nodes/pve1/status", `{"pveversion":"pve-manager/9.0.4/abcdef","cpuinfo":{"cpus":16},"memory":{"total":68719476736},"current-kernel":{"machine":"x86_64"}}`)
	m.data("/nodes/pve1/storage/local-lvm/status", `{"total":1000000000000,"avail":400000000000,"active":1,"enabled":1,"content":"images"}`)
	m.data("/nodes/pve1/storage/local-lvm/content", storageContentFixture)
	return m
}

// ageCaches moves every cached fact d into the past, standing in for wall
// time passing between refreshes without the test sleeping.
func ageCaches(p *proxmoxProvider, d time.Duration) {
	c := &p.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, at := range []*time.Time{&c.snap.at, &c.disk.at, &c.host.at} {
		if !at.IsZero() { // zero means invalid; keep it that way
			*at = at.Add(-d)
		}
	}
	for id, e := range c.prov {
		e.fetched = e.fetched.Add(-d)
		c.prov[id] = e
	}
	for name, st := range c.agent {
		st.until = st.until.Add(-d)
		c.agent[name] = st
	}
}

// boardRefresh is what the TUI's refreshCmd asks of a provider on every tick,
// followed by the heartbeat's host-memory readings that land before the next
// tick (two samples per running VM at a 2s heartbeat and a 5s refresh).
func boardRefresh(t *testing.T, p *proxmoxProvider) {
	t.Helper()
	vms, err := p.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	_ = p.HostResources()
	if _, err := p.Provenance(context.Background()); err != nil {
		t.Fatalf("Provenance: %v", err)
	}
	for range 2 {
		for _, v := range vms {
			if _, err := p.VMHostMemory(context.Background(), v.Name); err != nil {
				t.Fatalf("VMHostMemory(%s): %v", v.Name, err)
			}
		}
	}
}

// The headline property: a minute of board polling (12 refreshes at 5s, with
// heartbeat readings between them) costs the node the SAME number of requests
// whether the pool holds 1 VM or 60. Before the caches, each refresh cost
// 5 + one config read per managed VM, and each heartbeat sample one
// status/current per running VM.
func TestProxmoxRefreshCostDoesNotGrowWithFleetSize(t *testing.T) {
	totals := map[int]int{}
	for _, n := range []int{1, 5, 22, 60} {
		m := fleetMock(t, n)
		p := newProxmoxForTest(t, m)

		boardRefresh(t, p) // cold start: fills every cache, markers included
		m.reset()

		perRefreshMax := 0
		for range 12 {
			ageCaches(p, 5*time.Second)
			before := len(m.seen())
			boardRefresh(t, p)
			if d := len(m.seen()) - before; d > perRefreshMax {
				perRefreshMax = d
			}
		}
		total := len(m.seen())
		totals[n] = total

		for _, path := range m.seen() {
			if strings.Contains(path, "/status/current") || strings.HasSuffix(path, "/config") {
				t.Errorf("n=%d: steady-state polling requested %s; per-VM reads must not be on the polling path", n, path)
			}
		}
		// One listing per refresh, plus the long-TTL node/storage/content
		// reads when they lapse: never more than a handful in one refresh.
		if perRefreshMax > 4 {
			t.Errorf("n=%d: a single refresh cost %d requests, want <= 4", n, perRefreshMax)
		}
		if got := m.count("/cluster/resources"); got != 12 {
			t.Errorf("n=%d: %d cluster listings for 12 refreshes, want exactly 12 (List, Provenance and heartbeats share one)", n, got)
		}
	}
	t.Logf("requests per simulated minute by fleet size: %v", totals)
	if totals[1] != totals[60] || totals[5] != totals[60] || totals[22] != totals[60] {
		t.Fatalf("requests per simulated minute grew with fleet size: %v", totals)
	}
	if totals[60] > 20 {
		t.Fatalf("a simulated minute cost %d requests, want <= 20", totals[60])
	}
}

// Expired markers are revalidated a few per call, oldest first, while every
// VM keeps its cached marker — so the TTL lapsing for a whole fleet at once is
// a trickle, never a burst, and no tile loses its marker in the meantime.
func TestProxmoxProvenanceRevalidatesABoundedNumberPerCall(t *testing.T) {
	const n = 22
	m := fleetMock(t, n)
	p := newProxmoxForTest(t, m)
	if got, err := p.Provenance(context.Background()); err != nil || len(got) != n {
		t.Fatalf("cold Provenance = %d markers, %v; want %d", len(got), err, n)
	}

	ageCaches(p, provenanceTTL)
	m.reset()
	got, err := p.Provenance(context.Background())
	if err != nil {
		t.Fatalf("Provenance: %v", err)
	}
	if len(got) != n {
		t.Fatalf("expired markers must still be served while revalidating: got %d of %d", len(got), n)
	}
	configs := 0
	for _, path := range m.seen() {
		if strings.HasSuffix(path, "/config") {
			configs++
		}
	}
	if configs != provenanceRevalidatePerCall {
		t.Fatalf("revalidated %d markers in one call, want %d", configs, provenanceRevalidatePerCall)
	}
}

// A marker that says a build is in flight (another controller's, typically)
// stays on the short TTL so its progress keeps moving on this board.
func TestProxmoxProvenanceInFlightBuildStaysLive(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", `[{"vmid":100,"name":"web","node":"pve1","pool":"sandbar","status":"running","type":"qemu","tags":"sandbar"}]`)
	payload, _ := json.Marshal(Provenance{SchemaVersion: MarkerSchemaVersion, Provisioning: true})
	cfg, _ := json.Marshal(map[string]string{"description": provenanceBeginMarker + "\n" + string(payload) + "\n" + provenanceEndMarker})
	m.data("/nodes/pve1/qemu/100/config", string(cfg))
	p := newProxmoxForTest(t, m)

	if _, err := p.Provenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	ageCaches(p, provenanceBuildingTTL)
	m.reset()
	if _, err := p.Provenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !m.sawPath("/nodes/pve1/qemu/100/config") {
		t.Fatalf("an in-flight marker past provenanceBuildingTTL must be re-read: %v", m.seen())
	}
}

// This provider's own marker write updates the cache, so the next refresh
// shows it without a config read.
func TestProxmoxMarkManagedUpdatesTheCachedMarker(t *testing.T) {
	m, cfgs := newStatefulConfigMock(t)
	m.data("/cluster/resources", `[{"vmid":100,"name":"web","node":"pve1","pool":"sandbar","status":"running","type":"qemu","tags":"sandbar"}]`)
	m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"web","status":"running"}`)
	cfgs.set(100, map[string]string{"tags": "sandbar"})
	p := newProxmoxForTest(t, m)

	if _, err := p.Provenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.MarkManaged(context.Background(), "web", Provenance{SchemaVersion: MarkerSchemaVersion, Base: "fresh"}); err != nil {
		t.Fatalf("MarkManaged: %v", err)
	}
	m.reset()
	got, err := p.Provenance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got["web"].Base != "fresh" {
		t.Fatalf("Provenance after MarkManaged = %+v, want the marker just written", got["web"])
	}
	if m.sawPath("/nodes/pve1/qemu/100/config") {
		t.Fatalf("Provenance re-read a marker this provider had just written: %v", m.seen())
	}
	if !m.sawPath("/cluster/resources") {
		t.Fatalf("MarkManaged must invalidate the listing (the tag changed): %v", m.seen())
	}
}

// A cached marker belongs to a VMID AND a name: when PVE reuses a VMID for a
// differently-named VM, the old marker must never be attributed to it.
func TestProxmoxProvenanceCacheDoesNotSurviveVMIDReuse(t *testing.T) {
	m := fleetMock(t, 1)
	p := newProxmoxForTest(t, m)
	if _, err := p.Provenance(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.data("/cluster/resources", `[{"vmid":100,"name":"newcomer","node":"pve1","pool":"sandbar","status":"running","type":"qemu","tags":"sandbar"}]`)
	m.data("/nodes/pve1/qemu/100/config", `{"tags":"sandbar"}`)
	p.invalidateListing()
	m.reset()

	got, err := p.Provenance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["newcomer"]; ok {
		t.Fatalf("a reused VMID inherited the previous VM's marker: %+v", got)
	}
	if !m.sawPath("/nodes/pve1/qemu/100/config") {
		t.Fatalf("a VMID that now names a different VM must be re-read: %v", m.seen())
	}
}

// Every running VM's heartbeat asks for host memory at about the same moment;
// the listing behind those readings must be fetched once, not once per VM.
func TestProxmoxConcurrentMemoryReadingsShareOneListing(t *testing.T) {
	const n = 22
	m := fleetMock(t, n)
	var hits atomic.Int32
	inner := m.routes["/api2/json/cluster/resources"]
	m.on("/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(30 * time.Millisecond) // long enough for every caller to pile in
		inner(w, r)
	})
	p := newProxmoxForTest(t, m)

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.VMHostMemory(context.Background(), fmt.Sprintf("vm%d", i)); err != nil {
				t.Errorf("VMHostMemory: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := hits.Load(); got != 1 {
		t.Fatalf("%d concurrent memory readings fetched the listing %d times, want 1", n, got)
	}
}

// The shared listing fetch must not inherit its first caller's context. The
// heartbeat passes context.Background(); a fetch bound to that and stuck on a
// host that accepts connections but never answers used to stay the in-flight
// listing forever, so every later List joined it and timed out without ever
// fetching again.
func TestProxmoxListingFetchIsBoundedIndependentlyOfItsCaller(t *testing.T) {
	m := fleetMock(t, 1)
	var hang atomic.Bool
	hang.Store(true)
	inner := m.routes["/api2/json/cluster/resources"]
	m.on("/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		if hang.Load() {
			<-r.Context().Done()
			return
		}
		inner(w, r)
	})
	p := newProxmoxForTest(t, m)
	orig := apiTimeout
	apiTimeout = 50 * time.Millisecond
	t.Cleanup(func() { apiTimeout = orig })

	// The first caller brings no deadline of its own, like the heartbeat.
	leader := make(chan error, 1)
	go func() {
		_, err := p.listing(context.Background(), listingMaxAge)
		leader <- err
	}()
	time.Sleep(20 * time.Millisecond) // let it become the in-flight fetch
	hang.Store(false)
	deadline := time.Now().Add(2 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := p.listing(ctx, listingMaxAge)
		cancel()
		if err == nil {
			if err := <-leader; err == nil {
				t.Fatal("the first caller's fetch against a hung host succeeded")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the listing never recovered after the host started answering")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A VM whose guest agent fails is left alone for a while: the next polling
// address lookup (a heartbeat or sweep reconnecting, AsPoll) is answered locally instead of
// costing a status read, a config read and another failing agent call.
func TestProxmoxGuestAgentFailureIsNotRetriedImmediately(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", clusterResources)
	m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"web","status":"running"}`)
	m.data("/nodes/pve1/qemu/100/config", `{"net0":"virtio=`+testMAC+`,bridge=vmbr0"}`)
	m.fail("/nodes/pve1/qemu/100/agent/network-get-interfaces", http.StatusInternalServerError, "QEMU guest agent is not running")
	p := newProxmoxForTest(t, m)
	poll := AsPoll(context.Background())

	if _, err := p.guestIP(poll, "web"); err == nil {
		t.Fatal("guestIP: expected the agent failure")
	}
	if n := m.count("/nodes/pve1/qemu/100/agent/network-get-interfaces"); n != 1 {
		t.Fatalf("first lookup asked the agent %d times, want 1", n)
	}

	m.reset()
	for range 10 {
		if _, err := p.guestIP(poll, "web"); err == nil {
			t.Fatal("guestIP: expected the remembered agent failure")
		}
	}
	if got := m.seen(); len(got) != 0 {
		t.Fatalf("lookups during the agent backoff reached the API: %v", got)
	}

	// Once the backoff lapses the agent is asked again.
	ageCaches(p, agentBackoffMax)
	if _, err := p.guestIP(poll, "web"); err == nil {
		t.Fatal("guestIP: expected the agent failure again")
	}
	if !m.sawPath("/nodes/pve1/qemu/100/agent/network-get-interfaces") {
		t.Fatalf("the agent must be asked again after its backoff: %v", m.seen())
	}

	// A lookup without the poll mark (a shell, a copy) is not refused by the
	// board's backoff: the agent may well have come up since.
	m.reset()
	if _, err := p.guestIP(context.Background(), "web"); err == nil {
		t.Fatal("guestIP: expected the agent failure")
	}
	if !m.sawPath("/nodes/pve1/qemu/100/agent/network-get-interfaces") {
		t.Fatalf("a user-initiated lookup must ask the agent despite the backoff: %v", m.seen())
	}

	// And a failure streak lengthens the window.
	p.cache.mu.Lock()
	st := p.cache.agent["web"]
	p.cache.mu.Unlock()
	if st.failures != 3 || time.Until(st.until) <= agentBackoffBase {
		t.Fatalf("third failure: streak %d, window %v; want 3 and longer than %v", st.failures, time.Until(st.until), agentBackoffBase)
	}
}
