package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/vm"
)

// --- mock PVE server ------------------------------------------------------------

// pveMock is an httptest stand-in for the Proxmox API. It records EVERY request
// path in order, which is how the tests below assert not just what an operation
// returned but which endpoints it did and did not touch — the difference between
// Get asking about one VM and Get scanning the whole cluster, and the difference
// between a readiness wait aborting on a 403 and spinning on it.
type pveMock struct {
	srv    *httptest.Server
	mu     sync.Mutex
	paths  []string
	routes map[string]http.HandlerFunc
}

func newPVEMock(t *testing.T) *pveMock {
	t.Helper()
	m := &pveMock{routes: map[string]http.HandlerFunc{}}
	m.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.paths = append(m.paths, r.URL.Path)
		h, ok := m.routes[r.URL.Path]
		m.mu.Unlock()
		if !ok {
			// Not a silent 404: an unregistered path means the code under test
			// reached for an endpoint the test did not expect, which is a
			// finding, not a fixture gap to paper over with a default body.
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = fmt.Fprintf(w, `{"data":null,"message":"unmocked path %s"}`, r.URL.Path)
			return
		}
		h(w, r)
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *pveMock) host() string { return strings.TrimPrefix(m.srv.URL, "https://") }

// on registers a handler for one exact (decoded) request path.
func (m *pveMock) on(path string, h http.HandlerFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routes["/api2/json"+path] = h
}

// data registers a 200 response wrapping body in PVE's {"data": …} envelope.
func (m *pveMock) data(path, body string) {
	m.on(path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":%s}`, body)
	})
}

// fail registers a non-2xx response, the shape every PVE error path takes.
func (m *pveMock) fail(path string, code int, message string) {
	m.on(path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = fmt.Fprintf(w, `{"data":null,"message":%q}`, message)
	})
}

func (m *pveMock) seen() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.paths)
}

func (m *pveMock) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.paths = nil
}

// count reports how many times a path was requested.
func (m *pveMock) count(path string) int {
	n := 0
	for _, p := range m.seen() {
		if p == "/api2/json"+path {
			n++
		}
	}
	return n
}

// sawPath reports whether a path was requested at all.
func (m *pveMock) sawPath(path string) bool { return m.count(path) > 0 }

// --- fixtures -------------------------------------------------------------------

const (
	testUPID = "UPID:pve1:0000ABCD:00000000:65000000:qmstart:100:sandbar@pve!prov:"
	// testMAC is net0's MAC as PVE renders it in the config (upper case) — the
	// guest agent reports it lower case, which is exactly why the match must be
	// case-insensitive.
	testMAC = "BC:24:11:AA:BB:CC"
)

// okTask registers a task that is already finished and succeeded, so WaitTask
// returns on its first poll without sleeping.
func (m *pveMock) okTask(upid string) {
	m.data("/nodes/pve1/tasks/"+upid+"/status", `{"status":"stopped","exitstatus":"OK"}`)
}

// newProxmoxForTest builds a provider pointed at the mock, with local state
// isolated to a temp dir (no test may touch the developer's real state) and a
// 0600 token file, so the real constructor — including its token load — is what
// is under test.
func newProxmoxForTest(t *testing.T, m *pveMock, mutate ...func(*TargetConfig)) *proxmoxProvider {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("sandbar@pve!prov=1234\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	cfg := TargetConfig{
		Provider:     ProxmoxProviderID,
		Host:         m.host(),
		Node:         "pve1",
		Pool:         "sandbar",
		Storage:      "local-lvm",
		Bridge:       "vmbr0",
		User:         "dev",
		IdentityPath: "/keys/id_ed25519",
		TokenFile:    tokenFile,
		Insecure:     true,
	}
	for _, f := range mutate {
		f(&cfg)
	}

	p, err := NewProxmox(cfg)
	if err != nil {
		t.Fatalf("NewProxmox: %v", err)
	}
	pp, ok := p.(*proxmoxProvider)
	if !ok {
		t.Fatalf("NewProxmox returned %T; want *proxmoxProvider", p)
	}
	return pp
}

// shortAgentPolling shrinks the readiness-wait timings for the duration of a
// test. The suite is deliberately serial (AGENTS.md), so pinning these package
// vars is safe.
func shortAgentPolling(t *testing.T, timeout time.Duration) {
	t.Helper()
	oldInterval, oldTimeout := agentPollInterval, agentWaitTimeout
	agentPollInterval, agentWaitTimeout = time.Millisecond, timeout
	t.Cleanup(func() { agentPollInterval, agentWaitTimeout = oldInterval, oldTimeout })
}

// --- construction ---------------------------------------------------------------

// TestProxmoxSatisfiesProviderInterface is the compile-time proof restated as a
// test so the acceptance criterion has a named home.
func TestProxmoxSatisfiesProviderInterface(t *testing.T) {
	var _ Provider = (*proxmoxProvider)(nil)
}

// TestProxmoxNewDoesNoNetworkIO pins the constructor's contract: BuildFleet
// constructs one provider per enabled profile on the TUI's startup path, so a
// round trip here would make launching sand as slow as the least reachable
// endpoint. Reachability is Preflight's job.
func TestProxmoxNewDoesNoNetworkIO(t *testing.T) {
	m := newPVEMock(t)
	p := newProxmoxForTest(t, m)

	if got := m.seen(); len(got) != 0 {
		t.Fatalf("NewProxmox made %d request(s): %v; want none", len(got), got)
	}
	if p.HostFiles() == nil {
		t.Error("HostFiles() = nil; want a usable handle")
	}
}

// TestProxmoxNewRejectsMissingIdentity proves the fields without which nothing
// can work fail at construction (where BuildFleet turns them into a clear
// per-profile error binding) rather than as an opaque 404 on first use.
func TestProxmoxNewRejectsMissingIdentity(t *testing.T) {
	m := newPVEMock(t)
	cases := []struct {
		name  string
		blank func(*TargetConfig)
		want  string
	}{
		{"no host", func(c *TargetConfig) { c.Host = "" }, "host"},
		{"no node", func(c *TargetConfig) { c.Node = "" }, "node"},
		{"no pool", func(c *TargetConfig) { c.Pool = "" }, "pool"},
		{"no token file", func(c *TargetConfig) { c.TokenFile = "" }, "token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			tokenFile := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(tokenFile, []byte("tok"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := TargetConfig{Provider: ProxmoxProviderID, Host: m.host(), Node: "pve1", Pool: "sandbar", TokenFile: tokenFile}
			tc.blank(&cfg)
			_, err := NewProxmox(cfg)
			if err == nil {
				t.Fatalf("NewProxmox with %s: want an error", tc.name)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Errorf("error = %q; want it to name the missing %s", err, tc.want)
			}
		})
	}
}

// TestProxmoxScopeMatchesProfileTarget pins the registry identity: it must equal
// profiles.Profile.proxmoxTarget's "host:node/pool" exactly, or the same pool
// would own two different sets of managed VMs depending on which code path
// derived the scope.
func TestProxmoxScopeMatchesProfileTarget(t *testing.T) {
	cfg := TargetConfig{Provider: ProxmoxProviderID, Host: "pve.example.com", Node: "pve1", Pool: "sandbar", User: "dev", Port: 22}
	got := cfg.Scope()
	if got.Provider != ProxmoxProviderID {
		t.Errorf("Scope().Provider = %q; want %q", got.Provider, ProxmoxProviderID)
	}
	if want := "pve.example.com:pve1/sandbar"; got.RemoteTarget != want {
		t.Errorf("Scope().RemoteTarget = %q; want %q", got.RemoteTarget, want)
	}
}

// TestProxmoxHostUserIsGuestLoginUser pins the interface's explicit warning:
// HostUser seeds a new VM's user, so returning the LAPTOP's user would leave the
// guest login account unprovisioned. For Proxmox the answer is the cloud-init
// ciuser this provider itself configures.
func TestProxmoxHostUserIsGuestLoginUser(t *testing.T) {
	m := newPVEMock(t)
	p := newProxmoxForTest(t, m, func(c *TargetConfig) { c.User = "sandbox" })

	if got := p.HostUser(); got != "sandbox" {
		t.Fatalf("HostUser() = %q; want the configured guest user %q", got, "sandbox")
	}
	if got := p.GuestUser(vm.VM{Name: "web"}); got != "sandbox" {
		t.Errorf("GuestUser() = %q; want %q", got, "sandbox")
	}
	if got, want := p.GuestHome(vm.VM{Name: "web"}), "/home/sandbox"; got != want {
		t.Errorf("GuestHome() = %q; want %q", got, want)
	}
}

// TestProxmoxHostResourcesIsUnknown pins the zero value while the real sampling
// is unimplemented: every field is "unknown", which the header drops, rather
// than a fabricated zero that would read as no capacity at all.
func TestProxmoxHostResourcesIsUnknown(t *testing.T) {
	p := newProxmoxForTest(t, newPVEMock(t))
	if got := p.HostResources(); got != (HostResources{}) {
		t.Fatalf("HostResources() = %+v; want the zero value", got)
	}
}

// TestProxmoxProvisioningIsStubbed proves the three lifecycle methods refuse
// clearly instead of half-doing something.
func TestProxmoxProvisioningIsStubbed(t *testing.T) {
	p := newProxmoxForTest(t, newPVEMock(t))
	ctx, cfg, out := context.Background(), vm.CreateConfig{Name: "web"}, io.Discard

	for name, err := range map[string]error{
		"Create":   p.Create(ctx, cfg, provision.CreateOptions{}, out),
		"Recreate": p.Recreate(ctx, cfg, provision.CreateOptions{}, out),
		"Reset":    p.Reset(ctx, cfg, provision.ResetOptions{}, out),
	} {
		if err == nil {
			t.Errorf("%s: want a not-implemented error, got nil", name)
			continue
		}
		if !strings.Contains(err.Error(), "not yet implemented") {
			t.Errorf("%s error = %q; want it to say the feature is not yet implemented", name, err)
		}
	}
}

// --- discovery ------------------------------------------------------------------

// clusterResources is the fixture List reads: one running and one stopped qemu
// VM in the configured pool, an LXC container in the same pool, a qemu VM in
// ANOTHER pool, and a qemu VM in the pool but on another node.
const clusterResources = `[
  {"vmid":100,"name":"web","node":"pve1","pool":"sandbar","status":"running","type":"qemu","maxmem":8589934592,"maxdisk":107374182400,"cpus":4},
  {"vmid":101,"name":"api","node":"pve1","pool":"sandbar","status":"stopped","type":"qemu","maxmem":4294967296,"maxdisk":53687091200,"cpus":2},
  {"vmid":102,"name":"ct","node":"pve1","pool":"sandbar","status":"running","type":"lxc","maxmem":1073741824},
  {"vmid":103,"name":"other","node":"pve1","pool":"tenants","status":"running","type":"qemu"},
  {"vmid":104,"name":"elsewhere","node":"pve2","pool":"sandbar","status":"running","type":"qemu"}
]`

func TestProxmoxListReturnsOnlyPoolQemuOnThisNode(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", clusterResources)
	p := newProxmoxForTest(t, m)

	vms, err := p.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	var names []string
	for _, v := range vms {
		names = append(names, v.Name)
	}
	if want := []string{"web", "api"}; !slices.Equal(names, want) {
		t.Fatalf("List() names = %v; want %v (no lxc, no other pool, no other node)", names, want)
	}

	web := vms[0]
	if web.Status != "Running" {
		t.Errorf("running VM Status = %q; want %q — the UI's status bands match this string exactly", web.Status, "Running")
	}
	if vms[1].Status != "Stopped" {
		t.Errorf("stopped VM Status = %q; want %q", vms[1].Status, "Stopped")
	}
	if web.CPUs != 4 {
		t.Errorf("CPUs = %d; want 4", web.CPUs)
	}
	if web.Memory != "8589934592" || web.Disk != "107374182400" {
		t.Errorf("Memory/Disk = %q/%q; want the decimal byte strings the UI humanizes", web.Memory, web.Disk)
	}
	if want := filepath.Join(p.files.LimaHome(), "web"); web.Dir != want {
		t.Errorf("Dir = %q; want the per-VM state dir %q", web.Dir, want)
	}
}

// TestProxmoxGetUsesSingleVMEndpoint is the interface's explicit contract: Get
// must ask about ONE VM, never scan a listing. The cluster-wide endpoint is the
// thing being asserted absent.
func TestProxmoxGetUsesSingleVMEndpoint(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", clusterResources)
	m.data("/nodes/pve1/qemu/100/status/current",
		`{"vmid":100,"name":"web","status":"running","maxmem":8589934592,"maxdisk":107374182400,"cpus":4}`)
	p := newProxmoxForTest(t, m)

	// Warm the name->VMID index the way the board does, then watch only what
	// Get itself asks for. PVE addresses VMs by numeric id and offers no
	// lookup-by-name endpoint, so the index is the one thing a listing is
	// legitimately needed for — and it must not be re-fetched per Get.
	if _, err := p.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	m.reset()

	got, err := p.Get("web")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "web" || got.Status != "Running" {
		t.Errorf("Get() = %+v; want the single-VM status of web", got)
	}
	if m.sawPath("/cluster/resources") {
		t.Errorf("Get scanned /cluster/resources; the interface forbids implementing Get over a listing (requests: %v)", m.seen())
	}
	if !m.sawPath("/nodes/pve1/qemu/100/status/current") {
		t.Errorf("Get did not use the single-VM endpoint; requests: %v", m.seen())
	}
}

// TestProxmoxGetUnknownIsErrNoSuchInstance covers both ways a VM can be unknown:
// absent from the pool entirely, and a cached id the API now 404s. Consumers
// branch on this sentinel, so a differently-shaped error silently breaks them.
func TestProxmoxGetUnknownIsErrNoSuchInstance(t *testing.T) {
	t.Run("absent from the pool", func(t *testing.T) {
		m := newPVEMock(t)
		m.data("/cluster/resources", clusterResources)
		p := newProxmoxForTest(t, m)

		_, err := p.Get("ghost")
		if !errors.Is(err, lima.ErrNoSuchInstance) {
			t.Fatalf("Get(ghost) err = %v; want lima.ErrNoSuchInstance", err)
		}
	})

	t.Run("404 from the single-VM endpoint", func(t *testing.T) {
		m := newPVEMock(t)
		m.data("/cluster/resources", clusterResources)
		m.fail("/nodes/pve1/qemu/100/status/current", http.StatusNotFound, "Configuration file 'nodes/pve1/qemu-server/100.conf' does not exist")
		p := newProxmoxForTest(t, m)

		if _, err := p.List(); err != nil {
			t.Fatalf("List: %v", err)
		}
		_, err := p.Get("web")
		if !errors.Is(err, lima.ErrNoSuchInstance) {
			t.Fatalf("Get after a 404 err = %v; want lima.ErrNoSuchInstance", err)
		}
	})
}

// TestProxmoxGetPermissionErrorIsNotMistakenForAbsence proves a 403 stays a 403.
// Reporting "no such instance" for a permission problem would send an operator
// looking for a missing VM instead of a missing privilege — and, worse, a caller
// that treats the sentinel as "safe to create" would build a duplicate.
func TestProxmoxGetPermissionErrorIsNotMistakenForAbsence(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", clusterResources)
	m.fail("/nodes/pve1/qemu/100/status/current", http.StatusForbidden, "")
	p := newProxmoxForTest(t, m)

	if _, err := p.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	_, err := p.Get("web")
	if err == nil {
		t.Fatal("Get: want an error for a 403")
	}
	if errors.Is(err, lima.ErrNoSuchInstance) {
		t.Fatalf("Get err = %v; a 403 must NOT be reported as a missing instance", err)
	}
	if !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("Get err = %q; want the 403's reason phrase, which is the only detail PVE gives", err)
	}
}

// TestProxmoxStaleVMIDIsNotActedOn is the safety property behind caching a
// name->id mapping at all: PVE hands out the LOWEST free VMID, so a deleted VM's
// id is quickly reused by an unrelated one. If a stale entry were trusted, a
// `sand delete web` could delete a stranger's VM. The provider must notice the
// id now answers to a different name and re-resolve.
func TestProxmoxStaleVMIDIsNotActedOn(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", clusterResources)
	// VMID 100 has been recycled and now belongs to "unrelated"; "web" moved to 105.
	m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"unrelated","status":"running"}`)
	m.data("/nodes/pve1/qemu/105/status/current", `{"vmid":105,"name":"web","status":"running","maxmem":1024}`)
	p := newProxmoxForTest(t, m)

	if _, err := p.List(); err != nil { // warms web -> 100
		t.Fatalf("List: %v", err)
	}
	// The cluster now reports web at 105.
	m.data("/cluster/resources", `[{"vmid":105,"name":"web","node":"pve1","pool":"sandbar","status":"running","type":"qemu"}]`)

	got, err := p.Get("web")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "web" {
		t.Fatalf("Get() = %+v; want the VM actually named web", got)
	}
	if !m.sawPath("/nodes/pve1/qemu/105/status/current") {
		t.Errorf("Get did not re-resolve to the new VMID; requests: %v", m.seen())
	}
}

func TestProxmoxStatusMapsPVEStates(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", clusterResources)
	m.data("/nodes/pve1/qemu/101/status/current", `{"vmid":101,"name":"api","status":"stopped"}`)
	p := newProxmoxForTest(t, m)

	got, err := p.Status("api")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if got != "Stopped" {
		t.Fatalf("Status() = %q; want %q", got, "Stopped")
	}
}

// --- guest IP discovery ---------------------------------------------------------

// agentInterfaces is the deliberately hostile fixture: `lo` first (always
// present, always carrying 127.0.0.1), then a second NIC that is UP but
// UNADDRESSED — which omits the ip-addresses key ENTIRELY rather than sending an
// empty array — then a decoy NIC holding a perfectly plausible global address on
// a MAC that is not net0's, and finally net0 itself, whose IPv6 link-local comes
// before the address actually wanted. Only a MAC match with link-local filtering
// gets this right; matching by name, or taking the first global address, picks
// the decoy.
const agentInterfaces = `{"result":[
  {"name":"lo","hardware-address":"00:00:00:00:00:00","ip-addresses":[
    {"ip-address":"127.0.0.1","ip-address-type":"ipv4","prefix":8},
    {"ip-address":"::1","ip-address-type":"ipv6","prefix":128}]},
  {"name":"eth0","hardware-address":"BC:24:11:DE:AD:00"},
  {"name":"eth1","hardware-address":"BC:24:11:DE:AD:01","ip-addresses":[
    {"ip-address":"10.9.9.9","ip-address-type":"ipv4","prefix":24}]},
  {"name":"enp6s18","hardware-address":"bc:24:11:aa:bb:cc","ip-addresses":[
    {"ip-address":"fe80::be24:11ff:feaa:bbcc","ip-address-type":"ipv6","prefix":64},
    {"ip-address":"192.168.1.50","ip-address-type":"ipv4","prefix":24}]}
]}`

func TestProxmoxGuestIPMatchesNet0MAC(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", clusterResources)
	m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"web","status":"running"}`)
	m.data("/nodes/pve1/qemu/100/config",
		fmt.Sprintf(`{"name":"web","net0":"virtio=%s,bridge=vmbr0,firewall=1"}`, testMAC))
	m.data("/nodes/pve1/qemu/100/agent/network-get-interfaces", agentInterfaces)
	p := newProxmoxForTest(t, m)

	if _, err := p.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	got, err := p.guestIP(context.Background(), "web")
	if err != nil {
		t.Fatalf("guestIP: %v", err)
	}
	if got != "192.168.1.50" {
		t.Fatalf("guestIP = %q; want 192.168.1.50 (net0's MAC, skipping lo, the unaddressed NIC, the decoy, and fe80::/10)", got)
	}
}

// TestProxmoxGuestIPIsCachedAndInvalidated proves the cache both saves round
// trips and does not outlive a power transition, which is precisely when a DHCP
// lease can change hands.
func TestProxmoxGuestIPIsCachedAndInvalidated(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", clusterResources)
	m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"web","status":"running"}`)
	m.data("/nodes/pve1/qemu/100/config", fmt.Sprintf(`{"net0":"virtio=%s,bridge=vmbr0"}`, testMAC))
	m.data("/nodes/pve1/qemu/100/agent/network-get-interfaces", agentInterfaces)
	p := newProxmoxForTest(t, m)

	if _, err := p.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	for range 3 {
		if _, err := p.guestIP(context.Background(), "web"); err != nil {
			t.Fatalf("guestIP: %v", err)
		}
	}
	if n := m.count("/nodes/pve1/qemu/100/agent/network-get-interfaces"); n != 1 {
		t.Errorf("agent queried %d times for 3 resolutions; want 1 (cached)", n)
	}

	p.invalidateGuest("web")
	if _, err := p.guestIP(context.Background(), "web"); err != nil {
		t.Fatalf("guestIP after invalidation: %v", err)
	}
	if n := m.count("/nodes/pve1/qemu/100/agent/network-get-interfaces"); n != 2 {
		t.Errorf("agent queried %d times after invalidation; want a fresh resolution", n)
	}
}

// TestProxmoxGuestIPNoUsableAddress proves an up-but-unaddressed net0 is
// reported as such, rather than falling back to some other interface's address —
// which would send every guest command to the wrong machine.
func TestProxmoxGuestIPNoUsableAddress(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", clusterResources)
	m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"web","status":"running"}`)
	m.data("/nodes/pve1/qemu/100/config", fmt.Sprintf(`{"net0":"virtio=%s,bridge=vmbr0"}`, testMAC))
	// net0's own NIC is present but carries only a link-local address.
	m.data("/nodes/pve1/qemu/100/agent/network-get-interfaces", `{"result":[
	  {"name":"lo","hardware-address":"00:00:00:00:00:00","ip-addresses":[{"ip-address":"127.0.0.1","ip-address-type":"ipv4","prefix":8}]},
	  {"name":"enp6s18","hardware-address":"bc:24:11:aa:bb:cc","ip-addresses":[{"ip-address":"fe80::1","ip-address-type":"ipv6","prefix":64}]},
	  {"name":"eth9","hardware-address":"BC:24:11:DE:AD:01","ip-addresses":[{"ip-address":"10.9.9.9","ip-address-type":"ipv4","prefix":24}]}
	]}`)
	p := newProxmoxForTest(t, m)

	if _, err := p.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	got, err := p.guestIP(context.Background(), "web")
	if err == nil {
		t.Fatalf("guestIP = %q; want an error — net0 has no routable address", got)
	}
	if !strings.Contains(err.Error(), "web") {
		t.Errorf("guestIP err = %q; want it to name the VM", err)
	}
}

// TestProxmoxGuestIPNoMatchingInterface covers the guest agent answering before
// net0 appears at all — a real state during boot.
func TestProxmoxGuestIPNoMatchingInterface(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", clusterResources)
	m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"web","status":"running"}`)
	m.data("/nodes/pve1/qemu/100/config", fmt.Sprintf(`{"net0":"virtio=%s,bridge=vmbr0"}`, testMAC))
	m.data("/nodes/pve1/qemu/100/agent/network-get-interfaces", `{"result":[
	  {"name":"lo","hardware-address":"00:00:00:00:00:00","ip-addresses":[{"ip-address":"127.0.0.1","ip-address-type":"ipv4","prefix":8}]}
	]}`)
	p := newProxmoxForTest(t, m)

	if _, err := p.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := p.guestIP(context.Background(), "web"); err == nil {
		t.Fatal("guestIP: want an error when no interface carries net0's MAC")
	}
}

func TestProxmoxNet0MACParsing(t *testing.T) {
	cases := []struct {
		name, net0, want string
	}{
		{"model=mac form", "virtio=BC:24:11:AA:BB:CC,bridge=vmbr0,firewall=1", "bc:24:11:aa:bb:cc"},
		{"other model", "e1000=00:11:22:33:44:55,bridge=vmbr1", "00:11:22:33:44:55"},
		{"explicit macaddr", "model=virtio,macaddr=AA:BB:CC:DD:EE:FF,bridge=vmbr0", "aa:bb:cc:dd:ee:ff"},
		{"no mac", "model=virtio,bridge=vmbr0", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := net0MAC(tc.net0); got != tc.want {
				t.Fatalf("net0MAC(%q) = %q; want %q", tc.net0, got, tc.want)
			}
		})
	}
}

// --- guest-agent readiness ------------------------------------------------------

// TestProxmoxAgentWait403AbortsImmediately is the canonical failure mode this
// provider must not reproduce: a readiness predicate that swallows a 403 as "not
// ready yet" hangs forever instead of reporting a permission problem. One ping,
// then out.
func TestProxmoxAgentWait403AbortsImmediately(t *testing.T) {
	shortAgentPolling(t, 10*time.Second) // long enough that a retry loop would hang the test
	m := newPVEMock(t)
	m.fail("/nodes/pve1/qemu/100/agent/ping", http.StatusForbidden, "")
	p := newProxmoxForTest(t, m)

	start := time.Now()
	err := p.waitAgent(context.Background(), 100, "web", io.Discard)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("waitAgent: want an error for a 403")
	}
	if n := m.count("/nodes/pve1/qemu/100/agent/ping"); n != 1 {
		t.Errorf("agent pinged %d times; want exactly 1 — a permission failure is PERMANENT and must never be retried", n)
	}
	if elapsed > time.Second {
		t.Errorf("waitAgent took %v; want an immediate abort", elapsed)
	}
	if !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("err = %q; want the 403's reason phrase, where PVE puts the only detail", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "permission") {
		t.Errorf("err = %q; want it to name the failure as a permission problem", err)
	}
}

// TestProxmoxAgentWaitNotConfiguredIsPermanent proves the other permanent arm: a
// VM with no guest agent configured will never become ready, so polling until
// the timeout only delays a certain failure.
func TestProxmoxAgentWaitNotConfiguredIsPermanent(t *testing.T) {
	shortAgentPolling(t, 10*time.Second)
	m := newPVEMock(t)
	m.fail("/nodes/pve1/qemu/100/agent/ping", http.StatusInternalServerError, "No QEMU guest agent configured")
	p := newProxmoxForTest(t, m)

	err := p.waitAgent(context.Background(), 100, "web", io.Discard)
	if err == nil {
		t.Fatal("waitAgent: want an error when no agent is configured")
	}
	if n := m.count("/nodes/pve1/qemu/100/agent/ping"); n != 1 {
		t.Errorf("agent pinged %d times; want exactly 1 for a permanent condition", n)
	}
	if !strings.Contains(err.Error(), "guest agent") {
		t.Errorf("err = %q; want it to name the missing guest agent", err)
	}
}

// TestProxmoxAgentWaitRetriesTransient proves the transient arms really do keep
// polling — the whole point of the wait — so a VM that is merely still booting
// is not reported as broken.
func TestProxmoxAgentWaitRetriesTransient(t *testing.T) {
	shortAgentPolling(t, 5*time.Second)
	m := newPVEMock(t)

	var mu sync.Mutex
	attempt := 0
	m.on("/nodes/pve1/qemu/100/agent/ping", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		attempt++
		n := attempt
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"data":null,"message":"VM 100 is not running"}`))
		case 2:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"data":null,"message":"QEMU guest agent is not running"}`))
		case 3:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"data":null,"message":"got timeout"}`))
		default:
			_, _ = w.Write([]byte(`{"data":null}`))
		}
	})
	p := newProxmoxForTest(t, m)

	if err := p.waitAgent(context.Background(), 100, "web", io.Discard); err != nil {
		t.Fatalf("waitAgent: %v", err)
	}
	if n := m.count("/nodes/pve1/qemu/100/agent/ping"); n != 4 {
		t.Errorf("agent pinged %d times; want 4 (three transient failures then success)", n)
	}
}

// TestProxmoxAgentWaitTimesOut proves the wait is bounded and says what it was
// still waiting for.
func TestProxmoxAgentWaitTimesOut(t *testing.T) {
	shortAgentPolling(t, 50*time.Millisecond)
	m := newPVEMock(t)
	m.fail("/nodes/pve1/qemu/100/agent/ping", http.StatusInternalServerError, "QEMU guest agent is not running")
	p := newProxmoxForTest(t, m)

	err := p.waitAgent(context.Background(), 100, "web", io.Discard)
	if err == nil {
		t.Fatal("waitAgent: want a timeout error")
	}
	if !strings.Contains(err.Error(), "web") || !strings.Contains(err.Error(), "guest agent") {
		t.Errorf("err = %q; want it to name the VM and what it waited for", err)
	}
}

// --- power ----------------------------------------------------------------------

func TestProxmoxStartWaitsForTaskAndAgent(t *testing.T) {
	shortAgentPolling(t, 5*time.Second)
	m := newPVEMock(t)
	m.data("/cluster/resources", clusterResources)
	m.data("/nodes/pve1/qemu/101/status/current", `{"vmid":101,"name":"api","status":"stopped"}`)
	m.on("/nodes/pve1/qemu/101/status/start", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":%q}`, testUPID)
	})
	m.okTask(testUPID)
	m.data("/nodes/pve1/qemu/101/agent/ping", `null`)
	m.data("/nodes/pve1/qemu/101/config", `{"net0":"virtio=BC:24:11:AA:BB:CC,bridge=vmbr0"}`)
	m.data("/nodes/pve1/qemu/101/agent/network-get-interfaces", agentInterfaces)
	p := newProxmoxForTest(t, m)

	if _, err := p.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	var out bytes.Buffer
	if err := p.StartStreaming(context.Background(), "api", &out); err != nil {
		t.Fatalf("StartStreaming: %v", err)
	}
	for _, want := range []string{
		"/nodes/pve1/qemu/101/status/start",
		"/nodes/pve1/tasks/" + testUPID + "/status",
		"/nodes/pve1/qemu/101/agent/ping",
	} {
		if !m.sawPath(want) {
			t.Errorf("start did not request %s; requests: %v", want, m.seen())
		}
	}
	if out.Len() == 0 {
		t.Error("StartStreaming wrote no progress; the TUI shows this while the boot runs")
	}
	if ip, ok := p.cachedGuestIP("api"); !ok || ip != "192.168.1.50" {
		t.Errorf("cached IP after start = %q, %v; a boot must leave the address resolved (a ping alone can precede DHCP)", ip, ok)
	}
}

// TestProxmoxStartOnRunningVMIsIdempotent proves a start against an already
// running VM does not issue a doomed power call — PVE rejects it — and instead
// just confirms readiness, matching `limactl start`'s behaviour.
func TestProxmoxStartOnRunningVMIsIdempotent(t *testing.T) {
	shortAgentPolling(t, 5*time.Second)
	m := newPVEMock(t)
	m.data("/cluster/resources", clusterResources)
	m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"web","status":"running"}`)
	m.data("/nodes/pve1/qemu/100/agent/ping", `null`)
	m.data("/nodes/pve1/qemu/100/config", `{"net0":"virtio=BC:24:11:AA:BB:CC,bridge=vmbr0"}`)
	m.data("/nodes/pve1/qemu/100/agent/network-get-interfaces", agentInterfaces)
	p := newProxmoxForTest(t, m)

	if _, err := p.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	if err := p.Start("web"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if m.sawPath("/nodes/pve1/qemu/100/status/start") {
		t.Errorf("Start powered on an already-running VM; requests: %v", m.seen())
	}
}

func TestProxmoxStopShutsDownGracefully(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", clusterResources)
	m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"web","status":"running"}`)
	m.on("/nodes/pve1/qemu/100/status/shutdown", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":%q}`, testUPID)
	})
	m.okTask(testUPID)
	p := newProxmoxForTest(t, m)

	if _, err := p.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	// Warm the IP cache so the invalidation below is observable.
	p.setGuestIP("web", "192.168.1.50")

	if err := p.Stop("web"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !m.sawPath("/nodes/pve1/qemu/100/status/shutdown") {
		t.Errorf("Stop did not issue a graceful shutdown; requests: %v", m.seen())
	}
	if _, ok := p.cachedGuestIP("web"); ok {
		t.Error("Stop left a stale IP cached; a power transition can change the lease")
	}
}

func TestProxmoxDeletePurgesAndRefusesRunningWithoutForce(t *testing.T) {
	newMock := func(t *testing.T, status string) (*pveMock, *proxmoxProvider) {
		m := newPVEMock(t)
		m.data("/cluster/resources", clusterResources)
		m.data("/nodes/pve1/qemu/100/status/current", fmt.Sprintf(`{"vmid":100,"name":"web","status":%q}`, status))
		m.on("/nodes/pve1/qemu/100/status/stop", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"data":%q}`, testUPID)
		})
		m.on("/nodes/pve1/qemu/100", func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("purge"); got != "1" {
				t.Errorf("delete purge = %q; want 1 so backup jobs and HA resources go too", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"data":%q}`, testUPID)
		})
		m.okTask(testUPID)
		p := newProxmoxForTest(t, m)
		if _, err := p.List(); err != nil {
			t.Fatalf("List: %v", err)
		}
		return m, p
	}

	t.Run("running without force is refused", func(t *testing.T) {
		m, p := newMock(t, "running")
		err := p.Delete("web", false)
		if err == nil {
			t.Fatal("Delete: want a refusal for a running VM without force")
		}
		if m.sawPath("/nodes/pve1/qemu/100") {
			t.Errorf("Delete issued the destructive call anyway; requests: %v", m.seen())
		}
	})

	t.Run("force stops then deletes", func(t *testing.T) {
		m, p := newMock(t, "running")
		if err := p.Delete("web", true); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if !m.sawPath("/nodes/pve1/qemu/100/status/stop") || !m.sawPath("/nodes/pve1/qemu/100") {
			t.Errorf("forced delete should stop then delete; requests: %v", m.seen())
		}
	})

	t.Run("stopped deletes directly and forgets the VM", func(t *testing.T) {
		m, p := newMock(t, "stopped")
		if err := p.Delete("web", false); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if m.sawPath("/nodes/pve1/qemu/100/status/stop") {
			t.Errorf("Delete stopped an already-stopped VM; requests: %v", m.seen())
		}
		if _, ok := p.cachedVMID("web"); ok {
			t.Error("Delete left the VMID cached; the id is now free for PVE to reuse")
		}
	})
}

// --- guest transport ------------------------------------------------------------

// recordSSH swaps the provider's ssh execution for a recorder, so the exact argv
// is the assertion target and no test ever needs a real ssh binary or a real VM
// (AGENTS.md's hard rule, extended to this transport).
func recordSSH(p *proxmoxProvider) *[][]string {
	var argvs [][]string
	p.runSSH = func(_ context.Context, argv []string, _ io.Reader, stdout, _ io.Writer) error {
		argvs = append(argvs, argv)
		if stdout != nil {
			_, _ = io.WriteString(stdout, "hello\n")
		}
		return nil
	}
	return &argvs
}

// withGuest builds a provider whose "web" VM is already resolved to an IP, the
// state every guest-transport call starts from.
func withGuest(t *testing.T) (*pveMock, *proxmoxProvider) {
	t.Helper()
	m := newPVEMock(t)
	m.data("/cluster/resources", clusterResources)
	p := newProxmoxForTest(t, m)
	if _, err := p.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	p.setGuestIP("web", "192.168.1.50")
	return m, p
}

func TestProxmoxShellRunsSSHToTheGuest(t *testing.T) {
	_, p := withGuest(t)
	argvs := recordSSH(p)

	var out bytes.Buffer
	if err := p.Shell(context.Background(), "web", nil, &out, "uname", "-a"); err != nil {
		t.Fatalf("Shell: %v", err)
	}
	if len(*argvs) != 1 {
		t.Fatalf("ran %d commands; want 1", len(*argvs))
	}
	argv := (*argvs)[0]
	if argv[0] != "ssh" {
		t.Fatalf("argv[0] = %q; want ssh: %v", argv[0], argv)
	}
	if !slices.Contains(argv, "dev@192.168.1.50") {
		t.Errorf("argv missing the guest target dev@192.168.1.50: %v", argv)
	}
	if !slices.Contains(argv, "/keys/id_ed25519") {
		t.Errorf("argv missing the configured identity: %v", argv)
	}
	if tail := argv[len(argv)-2:]; !slices.Equal(tail, []string{"uname", "-a"}) {
		t.Errorf("argv tail = %v; want the guest command last", tail)
	}
	if out.String() != "hello\n" {
		t.Errorf("Shell out = %q; want the command's stdout", out.String())
	}
}

func TestProxmoxShellOutReturnsStdout(t *testing.T) {
	_, p := withGuest(t)
	recordSSH(p)

	got, err := p.ShellOut(context.Background(), "web", "echo", "hi")
	if err != nil {
		t.Fatalf("ShellOut: %v", err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("ShellOut = %q; want the captured stdout", got)
	}
}

// TestProxmoxShellFoldsStderrIntoTheError proves the parseable-output contract:
// stderr must never reach the caller's writer, only the error.
func TestProxmoxShellOutFoldsStderrIntoError(t *testing.T) {
	_, p := withGuest(t)
	p.runSSH = func(_ context.Context, _ []string, _ io.Reader, stdout, stderr io.Writer) error {
		_, _ = io.WriteString(stdout, "partial")
		_, _ = io.WriteString(stderr, "permission denied")
		return errors.New("exit status 1")
	}

	_, err := p.ShellOut(context.Background(), "web", "cat", "/etc/shadow")
	if err == nil {
		t.Fatal("ShellOut: want an error")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %q; want the guest's stderr folded in", err)
	}
}

// TestProxmoxGuestPathAndCopy pins the endpoint round trip: GuestPath is called
// on the TUI's Update goroutine, so it must never block on the network — it
// answers from the cache and otherwise defers to Copy, which has a context and
// an error to report with.
func TestProxmoxGuestPathAndCopy(t *testing.T) {
	m, p := withGuest(t)
	argvs := recordSSH(p)

	if got, want := p.GuestPath("web", "/home/dev"), "dev@192.168.1.50:/home/dev"; got != want {
		t.Fatalf("GuestPath with a known IP = %q; want %q", got, want)
	}

	// An unresolved VM yields the deferred form and, crucially, no network I/O.
	m.reset()
	deferred := p.GuestPath("api", "/tmp")
	if deferred != "api:/tmp" {
		t.Fatalf("GuestPath with an unknown IP = %q; want the deferred %q", deferred, "api:/tmp")
	}
	if got := m.seen(); len(got) != 0 {
		t.Fatalf("GuestPath made %d request(s): %v; it runs on the UI goroutine and must not block", len(got), got)
	}

	// Copy resolves whichever endpoint still needs it, and passes host paths through.
	p.setGuestIP("api", "10.0.0.7")
	if err := p.Copy(context.Background(), io.Discard, true, "/local/dir", deferred); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	argv := (*argvs)[len(*argvs)-1]
	if argv[0] != "scp" {
		t.Fatalf("argv[0] = %q; want scp: %v", argv[0], argv)
	}
	if !slices.Contains(argv, "-r") {
		t.Errorf("recursive copy missing -r: %v", argv)
	}
	if tail := argv[len(argv)-2:]; !slices.Equal(tail, []string{"/local/dir", "dev@10.0.0.7:/tmp"}) {
		t.Errorf("scp endpoints = %v; want the host path and the resolved guest endpoint", tail)
	}
}

// TestProxmoxAttachArgvWrapsTheGuestTmuxExpression proves `sand shell` and the
// TUI's S verb get an ssh -t wrapper around the SAME guest expression Lima uses.
// The expression is compared against lima's own so a future edit to either
// cannot drift them apart — a copy that set destroy-unattached on `main` would
// silently destroy the user's work on detach.
func TestProxmoxAttachArgvWrapsTheGuestTmuxExpression(t *testing.T) {
	_, p := withGuest(t)
	t.Setenv("COLORTERM", "truecolor")

	got := p.AttachArgv(vm.VM{Name: "web"})
	if len(got) < 2 || got[0] != "ssh" || got[1] != "-t" {
		t.Fatalf("AttachArgv must start `ssh -t …`: %v", got)
	}
	idx := slices.Index(got, "dev@192.168.1.50")
	if idx < 0 {
		t.Fatalf("AttachArgv missing the guest target: %v", got)
	}
	want := lima.GuestAttachArgv("truecolor")
	if tail := got[idx+1:]; len(tail) != 3 || tail[0] != want[0] || tail[1] != want[1] {
		t.Fatalf("AttachArgv tail = %v; want %v", tail, want)
	}
	// The expression is shell-quoted for the remote shell, so compare the
	// unquoted payload rather than the literal token.
	if !strings.Contains(got[len(got)-1], "destroy-unattached") {
		t.Fatalf("guest expression lost the tmux semantics: %q", got[len(got)-1])
	}
}

// TestProxmoxAttachArgvUnresolvableFailsLoudly proves an unreachable guest never
// produces an argv that could connect to SOMETHING ELSE. Returning `ssh …web…`
// with an unresolved name could reach an unrelated host of that name on the
// LAN, and returning nil would panic the caller, which indexes argv[0].
func TestProxmoxAttachArgvUnresolvableFailsLoudly(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", `[]`)
	p := newProxmoxForTest(t, m)

	got := p.AttachArgv(vm.VM{Name: "ghost"})
	if len(got) == 0 {
		t.Fatal("AttachArgv returned nothing; the caller indexes argv[0] and would panic")
	}
	if slices.Contains(got, "ssh") {
		t.Fatalf("AttachArgv built an ssh command for an unresolvable guest: %v", got)
	}
	out, err := runArgvForTest(t, got)
	if err == nil {
		t.Fatalf("the fallback argv exited 0; it must fail: %v", got)
	}
	if !strings.Contains(out, "ghost") {
		t.Errorf("fallback argv output = %q; want it to name the VM", out)
	}
}

// --- preflight ------------------------------------------------------------------

// preflightHappyPath registers every endpoint a good preflight touches.
func preflightHappyPath(m *pveMock) {
	m.data("/nodes/pve1/status", `{"pveversion":"pve-manager/8.2.4/abcdef","cpuinfo":{"cpus":16}}`)
	m.data("/pools", `[{"poolid":"sandbar"},{"poolid":"other"}]`)
	m.data("/nodes/pve1/storage/local-lvm/status", `{"total":1000,"avail":900,"active":1,"enabled":1,"content":"images,rootdir"}`)
}

func TestProxmoxPreflightHappyPath(t *testing.T) {
	m := newPVEMock(t)
	preflightHappyPath(m)
	p := newProxmoxForTest(t, m)

	if err := p.Preflight(); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
}

func TestProxmoxPreflightNamesTheSpecificFailure(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*pveMock)
		want  []string
	}{
		{
			name: "token rejected",
			setup: func(m *pveMock) {
				preflightHappyPath(m)
				m.fail("/nodes/pve1/status", http.StatusUnauthorized, "")
			},
			want: []string{"token"},
		},
		{
			name: "node unknown",
			setup: func(m *pveMock) {
				preflightHappyPath(m)
				m.fail("/nodes/pve1/status", http.StatusNotFound, "no such node")
			},
			want: []string{"node", "pve1"},
		},
		{
			name: "version too old",
			setup: func(m *pveMock) {
				preflightHappyPath(m)
				m.data("/nodes/pve1/status", `{"pveversion":"pve-manager/8.0.9/aaaa"}`)
			},
			want: []string{"8.1", "8.0.9"},
		},
		{
			name: "pool missing",
			setup: func(m *pveMock) {
				preflightHappyPath(m)
				m.data("/pools", `[{"poolid":"other"}]`)
			},
			want: []string{"sandbar", "pool"},
		},
		{
			name: "storage missing",
			setup: func(m *pveMock) {
				preflightHappyPath(m)
				m.fail("/nodes/pve1/storage/local-lvm/status", http.StatusInternalServerError, "storage 'local-lvm' does not exist")
			},
			want: []string{"local-lvm", "storage"},
		},
		{
			name: "storage without images content",
			setup: func(m *pveMock) {
				preflightHappyPath(m)
				m.data("/nodes/pve1/storage/local-lvm/status", `{"total":10,"avail":9,"active":1,"enabled":1,"content":"iso,vztmpl"}`)
			},
			want: []string{"local-lvm", "images"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newPVEMock(t)
			tc.setup(m)
			p := newProxmoxForTest(t, m)

			err := p.Preflight()
			if err == nil {
				t.Fatalf("Preflight: want an error for %s", tc.name)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Preflight error = %q; want it to name %q", err, want)
				}
			}
		})
	}
}

// TestProxmoxPreflightToleratesAnUnparseableVersion proves a version string this
// code cannot read is not treated as evidence of an old one: PVE has changed the
// format before, and locking a working host out over a cosmetic change would be
// worse than skipping a check the other failures already cover.
func TestProxmoxPreflightToleratesAnUnparseableVersion(t *testing.T) {
	m := newPVEMock(t)
	preflightHappyPath(m)
	m.data("/nodes/pve1/status", `{"pveversion":"something-unexpected"}`)
	p := newProxmoxForTest(t, m)

	if err := p.Preflight(); err != nil {
		t.Fatalf("Preflight with an unreadable version = %v; want it tolerated", err)
	}
}

func TestPVEVersionAtLeast(t *testing.T) {
	cases := []struct {
		in         string
		major, min int
		want, ok   bool
	}{
		{"pve-manager/8.2.4/abcdef", 8, 1, true, true},
		{"pve-manager/8.1.0/abcdef", 8, 1, true, true},
		{"pve-manager/8.0.9/abcdef", 8, 1, false, true},
		{"pve-manager/9.0.0/abcdef", 8, 1, true, true},
		{"pve-manager/7.4.17/abcdef", 8, 1, false, true},
		{"8.1.4", 8, 1, true, true},
		{"garbage", 8, 1, false, false},
		{"", 8, 1, false, false},
	}
	for _, tc := range cases {
		got, ok := pveVersionAtLeast(tc.in, tc.major, tc.min)
		if got != tc.want || ok != tc.ok {
			t.Errorf("pveVersionAtLeast(%q, %d, %d) = (%v, %v); want (%v, %v)", tc.in, tc.major, tc.min, got, ok, tc.want, tc.ok)
		}
	}
}

// runArgvForTest runs an argv and returns its combined output. Used only for the
// deliberately-failing fallback attach command, which must be a real executable
// that exits non-zero with a readable message.
func runArgvForTest(t *testing.T, argv []string) (string, error) {
	t.Helper()
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	return string(out), err
}
