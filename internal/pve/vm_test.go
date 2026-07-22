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
	query  url.Values
	form   url.Values
}

func vmTestClient(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, rec *recordedRequest)) (*Client, *recordedRequest) {
	t.Helper()
	rec := &recordedRequest{}
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method = r.Method
		rec.path = r.URL.Path
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
