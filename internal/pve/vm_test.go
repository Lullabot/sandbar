package pve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// vmTestClient builds a Client pointed at an httptest.NewTLSServer running
// handler, recording the last request's method, path, query, and parsed form
// for assertions.
type recordedRequest struct {
	method string
	path   string
	// rawPath is the still-escaped request path, kept alongside the decoded
	// path so a test can tell "one segment containing a slash" apart from
	// "two segments" — a distinction the decoded form erases.
	rawPath string
	query   url.Values
	form    url.Values
}

func vmTestClient(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, rec *recordedRequest)) (*Client, *recordedRequest) {
	t.Helper()
	rec := &recordedRequest{}
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method = r.Method
		rec.path = r.URL.Path
		rec.rawPath = r.URL.EscapedPath()
		rec.query = r.URL.Query()
		if err := r.ParseForm(); err == nil {
			rec.form = r.PostForm
		}
		handler(w, r, rec)
	}))
	t.Cleanup(ts.Close)

	c, err := New(Config{
		Host:               strings.TrimPrefix(ts.URL, "https://"),
		Node:               "node1",
		TokenID:            "user@pve!token=11111111-2222-3333-4444-555555555555",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, rec
}

func writeUPID(w http.ResponseWriter, upid string) {
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(map[string]any{"data": upid})
	_, _ = w.Write(b)
}

const testUPID = "UPID:node1:00001234:1A2B3C4D:5E6F7A8B:qmcreate:100:user@pve!token:"

// --- CloneVM: full vs linked clone param handling ---

func TestCloneVMFullSendsStorage(t *testing.T) {
	c, rec := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		writeUPID(w, testUPID)
	})

	_, err := c.CloneVM(context.Background(), 100, CloneVMOptions{
		NewID:   101,
		Full:    true,
		Storage: "local-zfs",
	})
	if err != nil {
		t.Fatalf("CloneVM: %v", err)
	}
	if rec.method != http.MethodPost {
		t.Errorf("method = %q; want POST", rec.method)
	}
	if rec.path != "/api2/json/nodes/node1/qemu/100/clone" {
		t.Errorf("path = %q", rec.path)
	}
	if got := rec.form.Get("full"); got != "1" {
		t.Errorf("full = %q; want 1", got)
	}
	if got := rec.form.Get("storage"); got != "local-zfs" {
		t.Errorf("storage = %q; want local-zfs", got)
	}
}

func TestCloneVMLinkedSendsNeitherStorageNorFormat(t *testing.T) {
	c, rec := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		writeUPID(w, testUPID)
	})

	_, err := c.CloneVM(context.Background(), 100, CloneVMOptions{
		NewID:   101,
		Full:    false,
		Storage: "local-zfs", // must be dropped: linked clone rejects it
		Format:  "qcow2",     // must be dropped: linked clone rejects it
	})
	if err != nil {
		t.Fatalf("CloneVM: %v", err)
	}
	if rec.form.Has("full") {
		t.Errorf("full = %q; linked clone must not set it", rec.form.Get("full"))
	}
	if rec.form.Has("storage") {
		t.Errorf("storage = %q; linked clone must send neither storage nor format", rec.form.Get("storage"))
	}
	if rec.form.Has("format") {
		t.Errorf("format = %q; linked clone must send neither storage nor format", rec.form.Get("format"))
	}
}

// --- ResizeDisk: absolute size with explicit unit, never a bare number ---

func TestResizeDiskSendsExplicitUnitSuffix(t *testing.T) {
	c, rec := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		writeUPID(w, testUPID)
	})

	_, err := c.ResizeDisk(context.Background(), 100, "scsi0", 20<<30)
	if err != nil {
		t.Fatalf("ResizeDisk: %v", err)
	}
	if rec.method != http.MethodPut {
		t.Errorf("method = %q; want PUT", rec.method)
	}
	got := rec.form.Get("size")
	if got != "20G" {
		t.Errorf("size = %q; want 20G (explicit unit suffix)", got)
	}
	// A bare number would be read by PVE as bytes, not the intended unit.
	if _, err := strconv.Atoi(got); err == nil {
		t.Errorf("size = %q parsed as a bare number; PVE would read this as bytes", got)
	}
}

// --- GetConfig: always sends current=1 ---

func TestGetConfigSendsCurrent1(t *testing.T) {
	c, rec := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"cores":2,"digest":"abc123"}}`))
	})

	cfg, err := c.GetConfig(context.Background(), 100)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if rec.method != http.MethodGet {
		t.Errorf("method = %q; want GET", rec.method)
	}
	if got := rec.query.Get("current"); got != "1" {
		t.Errorf("current = %q; want 1", got)
	}
	if cfg["digest"] != "abc123" {
		t.Errorf("cfg[digest] = %v; want abc123", cfg["digest"])
	}
}

// --- SetConfigSync (PUT, sync) vs SetConfigAsync (POST, returns UPID) ---

func TestSetConfigSyncUsesPUTAndReturnsNothing(t *testing.T) {
	c, rec := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":null}`))
	})

	err := c.SetConfigSync(context.Background(), 100, url.Values{"cores": {"4"}})
	if err != nil {
		t.Fatalf("SetConfigSync: %v", err)
	}
	if rec.method != http.MethodPut {
		t.Errorf("method = %q; want PUT", rec.method)
	}
	if rec.path != "/api2/json/nodes/node1/qemu/100/config" {
		t.Errorf("path = %q", rec.path)
	}
}

func TestSetConfigAsyncUsesPOSTAndReturnsUPID(t *testing.T) {
	c, rec := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		writeUPID(w, testUPID)
	})

	upid, err := c.SetConfigAsync(context.Background(), 100, url.Values{"cores": {"4"}})
	if err != nil {
		t.Fatalf("SetConfigAsync: %v", err)
	}
	if rec.method != http.MethodPost {
		t.Errorf("method = %q; want POST", rec.method)
	}
	if upid.Raw != testUPID {
		t.Errorf("upid.Raw = %q; want %q", upid.Raw, testUPID)
	}
}

// --- CreateVM: sshkeys percent-encoding ---

func TestCreateVMEncodesSSHKeysWithPercent20NeverPlus(t *testing.T) {
	c, rec := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		writeUPID(w, testUPID)
	})

	_, err := c.CreateVM(context.Background(), CreateVMOptions{
		VMID:    100,
		Storage: "local-zfs",
		Bridge:  "vmbr0",
		Pool:    "sandbar",
		SSHKeys: []string{"ssh-rsa AAAA... user@host"},
	})
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	// httptest's ParseForm performs exactly the ONE generic transport-level
	// decode ("the server's transport decodes once", per the task's
	// implementation notes) — it is PVE's OWN sshkeys-specific code that
	// performs the second, application-level uri_unescape. So after this
	// one decode the value must STILL be percent-encoded text containing a
	// literal "%20" for each space, never a real space and never a '+'.
	got := rec.form.Get("sshkeys")
	if strings.Contains(got, "+") {
		t.Errorf("sshkeys after one decode = %q; must never contain '+' standing in for a space", got)
	}
	if !strings.Contains(got, "%20") {
		t.Errorf("sshkeys after one decode = %q; want it to still contain a literal %%20 (only PVE's own second decode removes it)", got)
	}
}

func TestCreateVMSSHKeysNeverUsesPlusForSpace(t *testing.T) {
	var rawBody string
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		rawBody = string(b)
		writeUPID(w, testUPID)
	}))
	defer ts.Close()

	c, err := New(Config{
		Host:               strings.TrimPrefix(ts.URL, "https://"),
		Node:               "node1",
		TokenID:            "user@pve!token=11111111-2222-3333-4444-555555555555",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = c.CreateVM(context.Background(), CreateVMOptions{
		VMID:    100,
		Storage: "local-zfs",
		Bridge:  "vmbr0",
		Pool:    "sandbar",
		SSHKeys: []string{"ssh-rsa AAAA"},
	})
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	// encodeSSHKeys itself must never leave the space as a raw '+':
	// double-encoding "%20" is what must reach the wire in place of the
	// space, and a bare '+' character must never appear where the space
	// was — a literal '+' would be decoded to a space on the SERVER's
	// single unescape, landing as a raw space rather than surviving as
	// "%20" text that a subsequent guest-side unescape needs.
	if strings.Contains(rawBody, "AAAA+user") {
		t.Errorf("raw wire body used '+' in place of the ssh key's space: %q", rawBody)
	}
	if !strings.Contains(rawBody, "sshkeys=ssh-rsa%2520AAAA") {
		t.Errorf("raw wire body = %q; want a re-escaped %%20 (\"%%2520\") in sshkeys, proving the deliberate double-encode", rawBody)
	}
}

func TestEncodeSSHKeysDirectly(t *testing.T) {
	got := encodeSSHKeys([]string{"ssh-rsa AAAA", "ssh-ed25519 BBBB"})
	if strings.Contains(got, "+") {
		t.Errorf("encodeSSHKeys result contains '+': %q", got)
	}
	if !strings.Contains(got, "%20") {
		t.Errorf("encodeSSHKeys result missing %%20 for spaces: %q", got)
	}
	if !strings.Contains(got, "%0A") {
		t.Errorf("encodeSSHKeys result missing %%0A between keys: %q", got)
	}
}

func TestCreateVMRequiresBridgeAndPool(t *testing.T) {
	c, _ := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		t.Fatal("server should not be contacted when required options are missing")
	})

	if _, err := c.CreateVM(context.Background(), CreateVMOptions{VMID: 100, Storage: "local-zfs", Pool: "sandbar"}); err == nil {
		t.Error("CreateVM: expected an error when Bridge is missing")
	}
	if _, err := c.CreateVM(context.Background(), CreateVMOptions{VMID: 100, Storage: "local-zfs", Bridge: "vmbr0"}); err == nil {
		t.Error("CreateVM: expected an error when Pool is missing")
	}
}

func TestCreateVMFormValuesCloudInitDefaultsAndImport(t *testing.T) {
	c, rec := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		writeUPID(w, testUPID)
	})

	_, err := c.CreateVM(context.Background(), CreateVMOptions{
		VMID:       100,
		Storage:    "local-zfs",
		Bridge:     "vmbr0",
		Pool:       "sandbar",
		ImportFrom: "local:import/debian-13.qcow2",
	})
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	if got := rec.form.Get("scsihw"); got != "virtio-scsi-pci" {
		t.Errorf("scsihw = %q; want virtio-scsi-pci (PVE defaults to lsi)", got)
	}
	if got := rec.form.Get("scsi0"); got != "local-zfs:0,import-from=local:import/debian-13.qcow2" {
		t.Errorf("scsi0 = %q; want the :0 import form", got)
	}
	if got := rec.form.Get("net0"); got != "virtio,bridge=vmbr0" {
		t.Errorf("net0 = %q", got)
	}
	if got := rec.form.Get("ide2"); got != "local-zfs:cloudinit" {
		t.Errorf("ide2 = %q", got)
	}
	if got := rec.form.Get("ipconfig0"); got != "ip=dhcp" {
		t.Errorf("ipconfig0 = %q", got)
	}
	if got := rec.form.Get("pool"); got != "sandbar" {
		t.Errorf("pool = %q", got)
	}
	if got := rec.form.Get("boot"); got != "order=scsi0" {
		t.Errorf("boot = %q", got)
	}
	// cpu MUST default to host passthrough, never PVE's generic kvm64: that
	// model hides SSE4.2/POPCNT/AVX2 and makes Claude Code >= 2.1.205 livelock
	// at 100% CPU during `claude install`. See anthropics/claude-code#77208.
	if got := rec.form.Get("cpu"); got != "host" {
		t.Errorf("cpu = %q; want host (never the kvm64 default — see anthropics/claude-code#77208)", got)
	}
}

// A caller may override the CPU model, but the default must never silently
// become kvm64; this pins that an explicit value wins while empty stays "host".
func TestCreateVMCPUModelDefaultsToHostAndIsOverridable(t *testing.T) {
	c, rec := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		writeUPID(w, testUPID)
	})

	_, err := c.CreateVM(context.Background(), CreateVMOptions{
		VMID:    100,
		Storage: "local-zfs",
		Bridge:  "vmbr0",
		Pool:    "sandbar",
		Cpu:     "x86-64-v2-AES",
	})
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	if got := rec.form.Get("cpu"); got != "x86-64-v2-AES" {
		t.Errorf("cpu = %q; want the explicit override x86-64-v2-AES", got)
	}
}

func TestCreateVMDiskGBBareNumberMeansGiB(t *testing.T) {
	c, rec := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		writeUPID(w, testUPID)
	})

	_, err := c.CreateVM(context.Background(), CreateVMOptions{
		VMID:    100,
		Storage: "local-zfs",
		Bridge:  "vmbr0",
		Pool:    "sandbar",
		DiskGB:  32,
	})
	if err != nil {
		t.Fatalf("CreateVM: %v", err)
	}
	if got := rec.form.Get("scsi0"); got != "local-zfs:32" {
		t.Errorf("scsi0 = %q; want local-zfs:32 (bare number means GiB for disk creation)", got)
	}
}

// --- NextID collision handling ---

func TestCreateVMWithNextIDRetriesOnCollisionWithFreshID(t *testing.T) {
	var nextIDCalls, createCalls atomic.Int32
	ids := []string{"100", "101"} // two DIFFERENT server-provided ids

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/cluster/nextid"):
			i := nextIDCalls.Add(1) - 1
			w.Header().Set("Content-Type", "application/json")
			b, _ := json.Marshal(map[string]any{"data": ids[i]})
			_, _ = w.Write(b)
		case strings.HasSuffix(r.URL.Path, "/qemu"):
			n := createCalls.Add(1)
			if n == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				b, _ := json.Marshal(map[string]any{
					"data":    nil,
					"message": "unable to create VM 100 - VM 100 already exists",
				})
				_, _ = w.Write(b)
				return
			}
			writeUPID(w, testUPID)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer ts.Close()

	c, err := New(Config{
		Host:               strings.TrimPrefix(ts.URL, "https://"),
		Node:               "node1",
		TokenID:            "user@pve!token=11111111-2222-3333-4444-555555555555",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	vmid, _, err := c.CreateVMWithNextID(context.Background(), CreateVMOptions{
		Storage: "local-zfs",
		Bridge:  "vmbr0",
		Pool:    "sandbar",
	})
	if err != nil {
		t.Fatalf("CreateVMWithNextID: %v", err)
	}
	if vmid != 101 {
		t.Errorf("vmid = %d; want 101 (the SECOND, freshly-fetched id — not 100+1=101 by local increment coincidence)", vmid)
	}
	if got := nextIDCalls.Load(); got != 2 {
		t.Errorf("NextID called %d times; want exactly 2 (a fresh call per collision, not a local increment)", got)
	}
	if got := createCalls.Load(); got != 2 {
		t.Errorf("create called %d times; want exactly 2", got)
	}
}

func TestCreateVMWithNextIDPropagatesNonCollisionError(t *testing.T) {
	var nextIDCalls, createCalls atomic.Int32
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/cluster/nextid"):
			nextIDCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			b, _ := json.Marshal(map[string]any{"data": "100"})
			_, _ = w.Write(b)
		case strings.HasSuffix(r.URL.Path, "/qemu"):
			createCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			b, _ := json.Marshal(map[string]any{
				"data":    nil,
				"message": "unrelated internal failure",
			})
			_, _ = w.Write(b)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer ts.Close()

	c, err := New(Config{
		Host:               strings.TrimPrefix(ts.URL, "https://"),
		Node:               "node1",
		TokenID:            "user@pve!token=11111111-2222-3333-4444-555555555555",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, _, err = c.CreateVMWithNextID(context.Background(), CreateVMOptions{
		Storage: "local-zfs",
		Bridge:  "vmbr0",
		Pool:    "sandbar",
	})
	if err == nil {
		t.Fatal("CreateVMWithNextID: expected an error for a non-collision failure")
	}
	if got := nextIDCalls.Load(); got != 1 {
		t.Errorf("NextID called %d times; want exactly 1 (a non-collision error must not be retried)", got)
	}
	if got := createCalls.Load(); got != 1 {
		t.Errorf("create called %d times; want exactly 1", got)
	}
}

// --- ListVMs: type enum handling and client-side qemu/lxc filtering ---

func TestListVMsSendsTypeVMAndFiltersToQemu(t *testing.T) {
	c, rec := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"vmid":100,"type":"qemu","pool":"sandbar"},
			{"vmid":200,"type":"lxc","pool":"sandbar"},
			{"vmid":300,"type":"qemu","pool":"other"}
		]}`))
	})

	vms, err := c.ListVMs(context.Background(), "sandbar")
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	if got := rec.query.Get("type"); got != "vm" {
		t.Errorf("type query = %q; want vm (type=qemu is invalid and 400s)", got)
	}
	if len(vms) != 1 || vms[0].VMID != 100 {
		t.Errorf("vms = %+v; want exactly the qemu VM in pool sandbar (vmid 100)", vms)
	}
}

// --- NextID: tolerates the JSON-string response shape ---

func TestNextIDParsesStringData(t *testing.T) {
	c, _ := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"142"}`))
	})

	id, err := c.NextID(context.Background())
	if err != nil {
		t.Fatalf("NextID: %v", err)
	}
	if id != 142 {
		t.Errorf("NextID = %d; want 142", id)
	}
}

// TestNextIDParsesBareNumberData covers the tolerated second shape. PVE renders
// nextid as a JSON string today, but the value is semantically a number and the
// client accepts either — a bare number must not fall through to the string
// branch's Atoi and fail the whole create.
func TestNextIDParsesBareNumberData(t *testing.T) {
	c, _ := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":142}`))
	})

	id, err := c.NextID(context.Background())
	if err != nil {
		t.Fatalf("NextID: %v", err)
	}
	if id != 142 {
		t.Errorf("NextID = %d; want 142", id)
	}
}

// TestNextIDRejectsUnparseableData pins that a nextid the client cannot turn
// into an integer is an error rather than a silent 0 — creating a VM at vmid 0
// would be rejected by PVE with a message about the id, sending the reader after
// the wrong problem.
func TestNextIDRejectsUnparseableData(t *testing.T) {
	for _, body := range []string{`{"data":"not-a-number"}`, `{"data":{"id":100}}`} {
		c, _ := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		})

		id, err := c.NextID(context.Background())
		if err == nil {
			t.Errorf("NextID with data %s = %d, nil; want an error", body, id)
		}
		if id != 0 {
			t.Errorf("NextID with data %s returned id %d alongside its error; want 0", body, id)
		}
	}
}

// TestCreateVMWithNextIDGivesUpAfterBoundedRetries pins the retry bound. A
// cluster whose nextid is permanently colliding (a stale reservation, another
// tool holding the range) would otherwise spin forever inside a create; the loop
// must terminate and surface the last collision so the operator sees the cause.
func TestCreateVMWithNextIDGivesUpAfterBoundedRetries(t *testing.T) {
	var nextIDCalls, createCalls atomic.Int32
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/cluster/nextid"):
			w.Header().Set("Content-Type", "application/json")
			b, _ := json.Marshal(map[string]any{"data": strconv.Itoa(100 + int(nextIDCalls.Add(1)))})
			_, _ = w.Write(b)
		default:
			createCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"data":null,"message":"unable to create VM - config file already exists"}`))
		}
	}))
	defer ts.Close()

	c, err := New(Config{Host: strings.TrimPrefix(ts.URL, "https://"), Node: "node1", TokenID: "user@pve!token=1", InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, _, err = c.CreateVMWithNextID(context.Background(), CreateVMOptions{Storage: "local-zfs", Bridge: "vmbr0", Pool: "sandbar"})
	if err == nil {
		t.Fatal("CreateVMWithNextID: expected an error once the attempt budget is exhausted")
	}
	if !strings.Contains(err.Error(), "config file already exists") {
		t.Errorf("err = %q; want it to carry the last collision so the cause is visible", err)
	}
	if got := createCalls.Load(); got != maxNextIDAttempts {
		t.Errorf("create attempted %d times; want exactly maxNextIDAttempts (%d)", got, maxNextIDAttempts)
	}
}

// TestCloneVMWithNextIDGivesUpAfterBoundedRetries is the clone-path sibling of
// the bound above; the two loops are separate code and only one of them being
// bounded is exactly the kind of drift that goes unnoticed.
func TestCloneVMWithNextIDGivesUpAfterBoundedRetries(t *testing.T) {
	var nextIDCalls, cloneCalls, deleteCalls atomic.Int32
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			deleteCalls.Add(1)
			writeUPID(w, testUPID)
		case strings.HasSuffix(r.URL.Path, "/cluster/nextid"):
			w.Header().Set("Content-Type", "application/json")
			b, _ := json.Marshal(map[string]any{"data": strconv.Itoa(100 + int(nextIDCalls.Add(1)))})
			_, _ = w.Write(b)
		default:
			cloneCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"data":null,"message":"VM 101 already exists"}`))
		}
	}))
	defer ts.Close()

	c, err := New(Config{Host: strings.TrimPrefix(ts.URL, "https://"), Node: "node1", TokenID: "user@pve!token=1", InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, _, err := c.CloneVMWithNextID(context.Background(), 9000, CloneVMOptions{Pool: "sandbar"}); err == nil {
		t.Fatal("CloneVMWithNextID: expected an error once the attempt budget is exhausted")
	}
	if got := cloneCalls.Load(); got != maxNextIDAttempts {
		t.Errorf("clone attempted %d times; want exactly maxNextIDAttempts (%d)", got, maxNextIDAttempts)
	}
	// Even after giving up, none of the colliding ids may be touched: each one
	// holds another creator's VM, which a pool-scoped token is able to delete.
	if got := deleteCalls.Load(); got != 0 {
		t.Errorf("issued %d DELETE(s) after exhausting retries; want 0", got)
	}
}

// TestNextIDFailurePropagatesWithoutCreating pins that a nextid outage aborts
// before any create is attempted. The alternative — carrying on with the
// zero-valued vmid — would ask PVE to create VM 0.
func TestNextIDFailurePropagatesWithoutCreating(t *testing.T) {
	var createCalls atomic.Int32
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cluster/nextid") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"data":null}`))
			return
		}
		createCalls.Add(1)
		writeUPID(w, testUPID)
	}))
	defer ts.Close()

	c, err := New(Config{Host: strings.TrimPrefix(ts.URL, "https://"), Node: "node1", TokenID: "user@pve!token=1", InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, _, err := c.CreateVMWithNextID(context.Background(), CreateVMOptions{Storage: "local-zfs", Bridge: "vmbr0", Pool: "sandbar"}); !IsPermission(err) {
		t.Fatalf("CreateVMWithNextID err = %v; want the 403 propagated unchanged", err)
	}
	if _, _, err := c.CloneVMWithNextID(context.Background(), 9000, CloneVMOptions{Pool: "sandbar"}); !IsPermission(err) {
		t.Fatalf("CloneVMWithNextID err = %v; want the 403 propagated unchanged", err)
	}
	if got := createCalls.Load(); got != 0 {
		t.Errorf("attempted %d create/clone call(s) after nextid failed; want 0", got)
	}
}

// TestCreateVMRequiresStorage completes the required-options set: Storage backs
// both scsi0 and the cloud-init drive, so an omitted one produces a form PVE
// rejects with a message about scsi0 rather than about the missing setting.
func TestCreateVMRequiresStorage(t *testing.T) {
	c, _ := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		t.Fatal("server should not be contacted when Storage is missing")
	})

	if _, err := c.CreateVM(context.Background(), CreateVMOptions{VMID: 100, Bridge: "vmbr0", Pool: "sandbar"}); err == nil {
		t.Error("CreateVM: expected an error when Storage is missing")
	}
}

// TestCloneVMFullSendsFormat covers the other half of the full-clone parameter
// pair. Storage and Format are gated by the same `if opts.Full`, and a test that
// only ever passes Storage would not notice Format being dropped.
func TestCloneVMFullSendsFormat(t *testing.T) {
	c, rec := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		writeUPID(w, testUPID)
	})

	_, err := c.CloneVM(context.Background(), 100, CloneVMOptions{NewID: 101, Name: "web", Pool: "sandbar", Full: true, Format: "qcow2"})
	if err != nil {
		t.Fatalf("CloneVM: %v", err)
	}
	if got := rec.form.Get("format"); got != "qcow2" {
		t.Errorf("format = %q; want qcow2", got)
	}
	if got := rec.form.Get("name"); got != "web" {
		t.Errorf("name = %q; want web", got)
	}
	if got := rec.form.Get("pool"); got != "sandbar" {
		t.Errorf("pool = %q; want sandbar", got)
	}
}

// TestResizeDiskNoUPIDResponseIsNotAnError pins the resize no-op. Resizing to
// the size a disk already has succeeds with an empty data field and no task, and
// treating that missing UPID as a malformed-UPID error would fail an otherwise
// idempotent, correct resize.
func TestResizeDiskNoUPIDResponseIsNotAnError(t *testing.T) {
	c, _ := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":null}`))
	})

	upid, err := c.ResizeDisk(context.Background(), 100, "scsi0", 20<<30)
	if err != nil {
		t.Fatalf("ResizeDisk: %v", err)
	}
	if upid.Raw != "" {
		t.Errorf("upid = %+v; want the zero UPID when PVE returns no task", upid)
	}
}

// TestVMCallErrorsPropagate sweeps the API-error arm of the VM calls that have
// one, so a swallowed error cannot leave a caller acting on a zero value — a
// zero UPID would be waited on as if the operation had been dispatched, and a
// nil VM list reads as "no VMs" rather than "could not ask".
func TestVMCallErrorsPropagate(t *testing.T) {
	c, _ := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"data":null}`))
	})
	ctx := context.Background()

	calls := map[string]func() error{
		"ListVMs":        func() error { _, err := c.ListVMs(ctx, "sandbar"); return err },
		"SetConfigAsync": func() error { _, err := c.SetConfigAsync(ctx, 100, url.Values{"cores": {"4"}}); return err },
		"ResizeDisk":     func() error { _, err := c.ResizeDisk(ctx, 100, "scsi0", 20<<30); return err },
		"DeleteVM":       func() error { _, err := c.DeleteVM(ctx, 100, true); return err },
		"ListSnapshots":  func() error { _, err := c.ListSnapshots(ctx, 100); return err },
		"CreateSnapshot": func() error { _, err := c.CreateSnapshot(ctx, 100, "snap", "", false); return err },
		"DeleteSnapshot": func() error { _, err := c.DeleteSnapshot(ctx, 100, "snap"); return err },
		"RebootVM":       func() error { _, err := c.RebootVM(ctx, 100); return err },
	}
	for name, call := range calls {
		if err := call(); !IsPermission(err) {
			t.Errorf("%s err = %v; want the 403 classified as a permission error", name, err)
		}
	}
}

// TestRebootVMUsesTheRebootStatusAction pins reboot to its own status action.
// A reboot expressed as stop+start would drop the "reboot" semantics PVE
// implements (it applies pending config on the way through) and, for a VM under
// a lock, behave quite differently.
func TestRebootVMUsesTheRebootStatusAction(t *testing.T) {
	c, rec := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		writeUPID(w, testUPID)
	})

	upid, err := c.RebootVM(context.Background(), 100)
	if err != nil {
		t.Fatalf("RebootVM: %v", err)
	}
	if rec.method != http.MethodPost {
		t.Errorf("method = %q; want POST", rec.method)
	}
	if rec.path != "/api2/json/nodes/node1/qemu/100/status/reboot" {
		t.Errorf("path = %q; want the reboot status action", rec.path)
	}
	if upid.Raw != testUPID {
		t.Errorf("upid.Raw = %q; want the task returned by PVE", upid.Raw)
	}
}

// --- snapshots ---

func TestListSnapshotsDecodesEntries(t *testing.T) {
	c, rec := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"name":"current","description":"You are here!"},
			{"name":"clean","description":"before the build","parent":"current","snaptime":1700000000}
		]}`))
	})

	snaps, err := c.ListSnapshots(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if rec.method != http.MethodGet {
		t.Errorf("method = %q; want GET", rec.method)
	}
	if rec.path != "/api2/json/nodes/node1/qemu/100/snapshot" {
		t.Errorf("path = %q", rec.path)
	}
	// PVE synthesises a pseudo-snapshot named "current" into this listing; it is
	// not a real snapshot, and callers filtering it out need it decoded, not
	// dropped here.
	if len(snaps) != 2 || snaps[0].Name != "current" || snaps[1].Parent != "current" {
		t.Fatalf("snaps = %+v; want both entries including PVE's synthetic \"current\"", snaps)
	}
	if snaps[1].SnapTime != 1700000000 {
		t.Errorf("snaptime = %d; want 1700000000", snaps[1].SnapTime)
	}
}

// TestCreateSnapshotOmitsOptionalFields pins that description and vmstate are
// sent only when asked for. vmstate in particular is not a harmless default: it
// dumps the VM's entire RAM to storage, turning a metadata-cheap snapshot into a
// multi-gigabyte write.
func TestCreateSnapshotOmitsOptionalFields(t *testing.T) {
	c, rec := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		writeUPID(w, testUPID)
	})

	if _, err := c.CreateSnapshot(context.Background(), 100, "clean", "", false); err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if rec.method != http.MethodPost {
		t.Errorf("method = %q; want POST", rec.method)
	}
	if got := rec.form.Get("snapname"); got != "clean" {
		t.Errorf("snapname = %q; want clean", got)
	}
	if rec.form.Has("description") {
		t.Errorf("description = %q; an empty description must be omitted", rec.form.Get("description"))
	}
	if rec.form.Has("vmstate") {
		t.Errorf("vmstate = %q; it must be sent only when RAM state was asked for", rec.form.Get("vmstate"))
	}
}

func TestCreateSnapshotSendsDescriptionAndVMState(t *testing.T) {
	c, rec := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		writeUPID(w, testUPID)
	})

	upid, err := c.CreateSnapshot(context.Background(), 100, "clean", "before the build", true)
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	if got := rec.form.Get("description"); got != "before the build" {
		t.Errorf("description = %q", got)
	}
	if got := rec.form.Get("vmstate"); got != "1" {
		t.Errorf("vmstate = %q; want 1", got)
	}
	if upid.Raw != testUPID {
		t.Errorf("upid.Raw = %q; want the task returned by PVE", upid.Raw)
	}
}

// TestDeleteSnapshotEscapesTheNameInThePath guards against a snapshot name
// reaching the URL unescaped. The name is the only caller-supplied string that
// lands in a path segment here, so an unescaped '/' would silently retarget the
// DELETE at a different endpoint entirely.
func TestDeleteSnapshotEscapesTheNameInThePath(t *testing.T) {
	c, rec := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		writeUPID(w, testUPID)
	})

	if _, err := c.DeleteSnapshot(context.Background(), 100, "a/b c"); err != nil {
		t.Fatalf("DeleteSnapshot: %v", err)
	}
	if rec.method != http.MethodDelete {
		t.Errorf("method = %q; want DELETE", rec.method)
	}
	// r.URL.Path is the decoded form, so the assertion is that the server saw
	// ONE path segment carrying the whole name — not two.
	if rec.path != "/api2/json/nodes/node1/qemu/100/snapshot/a/b c" {
		t.Errorf("decoded path = %q", rec.path)
	}
	if rec.rawPath != "/api2/json/nodes/node1/qemu/100/snapshot/a%2Fb%20c" {
		t.Errorf("raw path = %q; want the snapshot name percent-escaped into a single segment", rec.rawPath)
	}
}

// --- DeleteVM: purge query param ---

func TestDeleteVMPurgeQueryParam(t *testing.T) {
	c, rec := vmTestClient(t, func(w http.ResponseWriter, r *http.Request, rec *recordedRequest) {
		writeUPID(w, testUPID)
	})

	if _, err := c.DeleteVM(context.Background(), 100, true); err != nil {
		t.Fatalf("DeleteVM: %v", err)
	}
	if rec.method != http.MethodDelete {
		t.Errorf("method = %q; want DELETE", rec.method)
	}
	if got := rec.query.Get("purge"); got != "1" {
		t.Errorf("purge = %q; want 1", got)
	}
}

// TestCloneVMWithNextIDRetriesCollision is the regression guard for a data-loss
// bug: a bare NextID+CloneVM with no retry, whose caller then "cleaned up" the
// colliding id, could purge the VM another creator just placed at that id in the
// same pool. CloneVMWithNextID must instead retry with a FRESH id, and must
// NEVER issue a DELETE against the colliding id (it is not ours).
func TestCloneVMWithNextIDRetriesCollision(t *testing.T) {
	var nextIDCalls, cloneCalls, deleteCalls atomic.Int32
	ids := []string{"100", "101"}

	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			deleteCalls.Add(1) // MUST NOT happen — the colliding id belongs to someone else
			writeUPID(w, testUPID)
		case strings.HasSuffix(r.URL.Path, "/cluster/nextid"):
			i := nextIDCalls.Add(1) - 1
			w.Header().Set("Content-Type", "application/json")
			b, _ := json.Marshal(map[string]any{"data": ids[i]})
			_, _ = w.Write(b)
		case strings.HasSuffix(r.URL.Path, "/clone"):
			n := cloneCalls.Add(1)
			if n == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				b, _ := json.Marshal(map[string]any{"data": nil, "message": "config file already exists"})
				_, _ = w.Write(b)
				return
			}
			writeUPID(w, testUPID)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer ts.Close()

	c, err := New(Config{Host: strings.TrimPrefix(ts.URL, "https://"), Node: "node1", TokenID: "user@pve!token=1", InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	newid, _, err := c.CloneVMWithNextID(context.Background(), 9000, CloneVMOptions{Name: "web", Pool: "sandbar", Full: true, Storage: "local-zfs"})
	if err != nil {
		t.Fatalf("CloneVMWithNextID: %v", err)
	}
	if newid != 101 {
		t.Errorf("newid = %d; want 101 (the fresh id after the collision)", newid)
	}
	if got := deleteCalls.Load(); got != 0 {
		t.Errorf("issued %d DELETE(s); want 0 — a collision must never purge the id, which is another creator's VM", got)
	}
	if got := nextIDCalls.Load(); got != 2 {
		t.Errorf("NextID called %d times; want 2 (a fresh id per collision)", got)
	}
}

// TestResizeDiskFractionalSizeIsExactBytes pins the precision fix: a size that
// is not a whole GiB must be sent as exact bytes, never truncated to "0G" or a
// smaller "<n>G" that would under-size the disk or no-op the resize.
func TestResizeDiskFractionalSizeIsExactBytes(t *testing.T) {
	var got string
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm.Get("size")
		writeUPID(w, testUPID)
	}))
	defer ts.Close()
	c, err := New(Config{Host: strings.TrimPrefix(ts.URL, "https://"), Node: "node1", TokenID: "user@pve!token=1", InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// 1.5 GiB — the truncating "%dG" form would have sent "1G" (a silent
	// under-size); a sub-GiB size would have sent "0G".
	oneAndAHalfGiB := int64(3) * (1 << 30) / 2
	if _, err := c.ResizeDisk(context.Background(), 100, "scsi0", oneAndAHalfGiB); err != nil {
		t.Fatalf("ResizeDisk: %v", err)
	}
	if want := strconv.FormatInt(oneAndAHalfGiB, 10); got != want {
		t.Errorf("size = %q; want the exact byte count %q (bare number = bytes to PVE)", got, want)
	}
}
