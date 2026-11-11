package provider

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/pve"
	"github.com/lullabot/sandbar/internal/vm"
)

// The tile's closing line for a Proxmox VM: "up <duration>" from the cluster
// listing's uptime, "last used <ago>" from the VM's RRD history. Before these,
// both were sampled from Lima instance files that a Proxmox VM's Dir never
// holds, so every stopped Proxmox VM read "never used".

// timesResources is one running VM (up two hours) and one stopped VM.
const timesResources = `[
  {"vmid":100,"name":"web","node":"pve1","pool":"sandbar","status":"running","type":"qemu","maxcpu":2,"uptime":7200},
  {"vmid":101,"name":"api","node":"pve1","pool":"sandbar","status":"stopped","type":"qemu","maxcpu":2,"uptime":0}
]`

// rrdRows renders rrddata rows starting at start, step seconds apart, one per
// entry of running: true rows carry a cpu reading, false rows carry only time
// (the shape PVE sends for an interval the VM was stopped).
func rrdRows(start, step int64, running ...bool) string {
	rows := make([]string, len(running))
	for i, r := range running {
		ts := start + int64(i)*step
		if r {
			rows[i] = fmt.Sprintf(`{"time":%d,"cpu":0.02,"maxcpu":2,"maxmem":4294967296}`, ts)
		} else {
			rows[i] = fmt.Sprintf(`{"time":%d,"maxcpu":2,"maxmem":4294967296}`, ts)
		}
	}
	return "[" + strings.Join(rows, ",") + "]"
}

// rrdByTimeframe serves rrddata per timeframe and records the timeframes
// requested, in order. A timeframe with no body is answered with no rows.
func rrdByTimeframe(t *testing.T, m *pveMock, vmid int, bodies map[string]string) *[]string {
	t.Helper()
	var mu sync.Mutex
	asked := &[]string{}
	m.on(fmt.Sprintf("/nodes/pve1/qemu/%d/rrddata", vmid), func(w http.ResponseWriter, r *http.Request) {
		tf := r.URL.Query().Get("timeframe")
		if cf := r.URL.Query().Get("cf"); cf != "AVERAGE" {
			t.Errorf("rrddata cf = %q, want AVERAGE", cf)
		}
		mu.Lock()
		*asked = append(*asked, tf)
		mu.Unlock()
		body, ok := bodies[tf]
		if !ok {
			body = "[]"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":%s}`, body)
	})
	return asked
}

func listByName(t *testing.T, p *proxmoxProvider) map[string]vm.VM {
	t.Helper()
	vms, err := p.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	out := map[string]vm.VM{}
	for _, v := range vms {
		out[v.Name] = v
	}
	return out
}

func TestProxmoxListUpSinceFromClusterUptime(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", timesResources)
	rrdByTimeframe(t, m, 101, nil)
	p := newProxmoxForTest(t, m)

	before := time.Now()
	vms := listByName(t, p)
	after := time.Now()

	web := vms["web"]
	lo, hi := before.Add(-2*time.Hour), after.Add(-2*time.Hour)
	if web.UpSince.Before(lo) || web.UpSince.After(hi) {
		t.Fatalf("web.UpSince = %v, want two hours before the listing (%v..%v)", web.UpSince, lo, hi)
	}
	if !vms["api"].UpSince.IsZero() {
		t.Fatalf("a stopped VM's UpSince = %v, want zero", vms["api"].UpSince)
	}
	if n := m.count("/nodes/pve1/qemu/100/rrddata"); n != 0 {
		t.Fatalf("a running VM's RRD history was read %d times; only stopped VMs need it", n)
	}
}

func TestProxmoxGetUpSinceFromStatusUptime(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", timesResources)
	m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"web","status":"running","uptime":90}`)
	p := newProxmoxForTest(t, m)

	before := time.Now()
	v, err := p.Get("web")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if lo, hi := before.Add(-90*time.Second), time.Now().Add(-90*time.Second); v.UpSince.Before(lo) || v.UpSince.After(hi) {
		t.Fatalf("UpSince = %v, want 90s before the call", v.UpSince)
	}
}

// The last row with a cpu reading is the last interval the VM ran, and the
// row after it starts when that interval ended.
func TestProxmoxListLastUsedFromRRDHistory(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", timesResources)
	const start = int64(1_800_000_000)
	asked := rrdByTimeframe(t, m, 101, map[string]string{
		"day": rrdRows(start, 60, true, true, true, false, false),
	})
	p := newProxmoxForTest(t, m)

	api := listByName(t, p)["api"]
	if want := time.Unix(start+3*60, 0); !api.LastUsed.Equal(want) {
		t.Fatalf("LastUsed = %v, want %v (the end of the last running interval)", api.LastUsed, want)
	}
	if api.LastUsedUnknown {
		t.Fatal("LastUsedUnknown set on an answered lookup")
	}
	if got := strings.Join(*asked, ","); got != "day" {
		t.Fatalf("timeframes asked = %s, want only day when day answers", got)
	}
}

// A VM idle longer than the day window is found in the coarser ones, and the
// search stops at the first window that answers.
func TestProxmoxListLastUsedEscalatesTimeframes(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", timesResources)
	const start = int64(1_790_000_000)
	asked := rrdByTimeframe(t, m, 101, map[string]string{
		"day":   rrdRows(start, 60, false, false),
		"month": rrdRows(start, 1800, true, false, false),
	})
	p := newProxmoxForTest(t, m)

	api := listByName(t, p)["api"]
	if want := time.Unix(start+1800, 0); !api.LastUsed.Equal(want) {
		t.Fatalf("LastUsed = %v, want %v from the month window", api.LastUsed, want)
	}
	if got := strings.Join(*asked, ","); got != "day,month" {
		t.Fatalf("timeframes asked = %s, want day,month (year not needed)", got)
	}
}

// A stopped VM's last-used time cannot change until it runs, so it is read
// once. Seeing the VM running drops the cached answer, and the next stop is
// looked up afresh.
func TestProxmoxListLastUsedIsCachedUntilTheVMRuns(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", timesResources)
	const first = int64(1_800_000_000)
	day := rrdRows(first, 60, true, false)
	m.on("/nodes/pve1/qemu/101/rrddata", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":%s}`, day)
	})
	p := newProxmoxForTest(t, m)

	listByName(t, p)
	ageCaches(p, time.Minute)
	listByName(t, p)
	if n := m.count("/nodes/pve1/qemu/101/rrddata"); n != 1 {
		t.Fatalf("RRD read %d times across two listings of a stopped VM, want 1", n)
	}

	// The VM runs, then stops again later.
	m.data("/cluster/resources", strings.Replace(timesResources, `"status":"stopped","type":"qemu","maxcpu":2,"uptime":0`, `"status":"running","type":"qemu","maxcpu":2,"uptime":60`, 1))
	ageCaches(p, time.Minute)
	if v := listByName(t, p)["api"]; !v.LastUsed.IsZero() {
		t.Fatalf("a running VM has LastUsed %v, want zero", v.LastUsed)
	}
	const second = first + 86_400
	day = rrdRows(second, 60, true, false)
	m.data("/cluster/resources", timesResources)
	ageCaches(p, time.Minute)
	if v := listByName(t, p)["api"]; !v.LastUsed.Equal(time.Unix(second+60, 0)) {
		t.Fatalf("after a second stop LastUsed = %v, want the new stop %v", v.LastUsed, time.Unix(second+60, 0))
	}
}

// A failed lookup is UNKNOWN, not "never used", and is retried by the next
// listing rather than cached.
func TestProxmoxListLastUsedUnknownWhenRRDFails(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", timesResources)
	m.fail("/nodes/pve1/qemu/101/rrddata", http.StatusInternalServerError, "rrdcached unavailable")
	p := newProxmoxForTest(t, m)

	api := listByName(t, p)["api"]
	if !api.LastUsed.IsZero() || !api.LastUsedUnknown {
		t.Fatalf("LastUsed = %v, unknown = %v; want zero and unknown on a failed read", api.LastUsed, api.LastUsedUnknown)
	}
	ageCaches(p, time.Minute)
	listByName(t, p)
	if n := m.count("/nodes/pve1/qemu/101/rrddata"); n != 2 {
		t.Fatalf("RRD read %d times across two listings after a failure, want 2 (a failure must not be cached)", n)
	}
}

// No running sample in a whole year is a real answer — the zero time, "never
// used" — and it is cached, so a never-started VM does not cost three
// requests on every refresh.
func TestProxmoxListLastUsedNeverRunIsCached(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", timesResources)
	asked := rrdByTimeframe(t, m, 101, nil)
	p := newProxmoxForTest(t, m)

	api := listByName(t, p)["api"]
	if !api.LastUsed.IsZero() || api.LastUsedUnknown {
		t.Fatalf("LastUsed = %v, unknown = %v; want zero and known", api.LastUsed, api.LastUsedUnknown)
	}
	ageCaches(p, time.Minute)
	listByName(t, p)
	if got := strings.Join(*asked, ","); got != "day,month,year" {
		t.Fatalf("timeframes asked = %s, want day,month,year exactly once", got)
	}
}

// A cold start over a large stopped fleet looks up a few VMs per listing, the
// rest reading unknown until a later refresh reaches them, and then costs
// nothing at all.
func TestProxmoxListLastUsedLookupsAreBoundedPerCall(t *testing.T) {
	m := newPVEMock(t)
	const n = 7
	var res []string
	for i := range n {
		vmid := 100 + i
		res = append(res, fmt.Sprintf(`{"vmid":%d,"name":"vm%d","node":"pve1","pool":"sandbar","status":"stopped","type":"qemu"}`, vmid, i))
		rrdByTimeframe(t, m, vmid, map[string]string{"day": rrdRows(1_800_000_000, 60, true, false)})
	}
	m.data("/cluster/resources", "["+strings.Join(res, ",")+"]")
	p := newProxmoxForTest(t, m)

	rrdCalls := func() int {
		c := 0
		for _, path := range m.seen() {
			if strings.HasSuffix(path, "/rrddata") {
				c++
			}
		}
		return c
	}
	unknown := func(vms map[string]vm.VM) int {
		c := 0
		for _, v := range vms {
			if v.LastUsedUnknown {
				c++
			}
		}
		return c
	}

	want := []int{n - lastUsedLookupsPerCall, n - 2*lastUsedLookupsPerCall, 0}
	for i, w := range want {
		if w < 0 {
			w = 0
		}
		ageCaches(p, time.Minute)
		if got := unknown(listByName(t, p)); got != w {
			t.Fatalf("listing %d: %d VMs unknown, want %d", i+1, got, w)
		}
	}
	if got := rrdCalls(); got != n {
		t.Fatalf("%d RRD reads to fill %d VMs, want %d", got, n, n)
	}
	ageCaches(p, time.Minute)
	listByName(t, p)
	if got := rrdCalls(); got != n {
		t.Fatalf("a steady-state listing read RRD history %d more times, want 0", got-n)
	}
}

func TestLastRunningSample(t *testing.T) {
	cpu := 0.1
	for _, tc := range []struct {
		name   string
		rows   []pve.RRDPoint
		want   int64
		wantOK bool
	}{
		{"empty", nil, 0, false},
		{"never running", []pve.RRDPoint{{Time: 10}, {Time: 20}}, 0, false},
		{"stopped after running", []pve.RRDPoint{{Time: 10, CPU: &cpu}, {Time: 20}, {Time: 30}}, 20, true},
		{"running through the last row", []pve.RRDPoint{{Time: 10}, {Time: 20, CPU: &cpu}}, 20, true},
		{"gap then running", []pve.RRDPoint{{Time: 10, CPU: &cpu}, {Time: 20}, {Time: 30, CPU: &cpu}, {Time: 40}}, 40, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := lastRunningSample(tc.rows)
			if ok != tc.wantOK || (ok && got.Unix() != tc.want) {
				t.Fatalf("lastRunningSample = %v, %v; want %d, %v", got.Unix(), ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestUpSinceFromUptimeZeroIsUnknown(t *testing.T) {
	if got := upSinceFromUptime(0, time.Now()); !got.IsZero() {
		t.Fatalf("uptime 0 gave %v, want the zero time (unknown), not \"booted now\"", got)
	}
}
