package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/vm"
)

// quietSSH installs a STATELESS ssh stub: it swallows the argv and echoes a line
// to stdout, keeping no shared slice. recordSSH (proxmox_test.go) appends every
// argv to one slice, which is a data race when two Create goroutines drive the
// transport at once — so the concurrency test uses this instead.
func quietSSH(p *proxmoxProvider) {
	p.runSSH = func(_ context.Context, _ []string, _ io.Reader, stdout, _ io.Writer) error {
		if stdout != nil {
			_, _ = io.WriteString(stdout, "ok\n")
		}
		return nil
	}
}

// proxmoxprovision_test.go drives the Create/Recreate/Reset lifecycle against the
// mock PVE server (pveMock, in proxmox_test.go) and the recorded ssh transport.
// The assertions are about the API CALL SEQUENCE an operation does and does not
// make — pool membership on both the base create and the clone, a cloud-init
// regenerate after every cloud-init config write, serialized clones, partial-VM
// cleanup on failure, and trusting WaitTask's WARNINGS verdict — because those,
// not the ssh commands (which never run here), are the correctness invariants
// this backend rests on.

// --- shared fixtures ------------------------------------------------------------

// createRecorder captures the request details the lifecycle assertions need: the
// pool passed on the base create and the clone, an ordered log of cloud-init
// config writes and regenerates, and the peak number of clones running at once.
type createRecorder struct {
	mu          sync.Mutex
	basePool    string
	clonePool   string
	cloneNewID  string
	ciLog       []string
	cloneActive int
	cloneMax    int
	nextID      int
	names       map[int]string // VMID -> the name it was created/cloned with
}

// setName records the name a VMID was created or cloned with, so the per-VMID
// status route can report a name that matches what the code just set — the two
// concurrent creates allocate 101/102 in a race, so the mapping cannot be fixed
// at registration time.
func (r *createRecorder) setName(id int, name string) {
	r.mu.Lock()
	if r.names == nil {
		r.names = map[int]string{}
	}
	r.names[id] = name
	r.mu.Unlock()
}

func (r *createRecorder) nameFor(id int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.names[id]
}

func (r *createRecorder) log(s string) {
	r.mu.Lock()
	r.ciLog = append(r.ciLog, s)
	r.mu.Unlock()
}

func (r *createRecorder) ci() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ciLog...)
}

func (r *createRecorder) enterClone() {
	r.mu.Lock()
	r.cloneActive++
	if r.cloneActive > r.cloneMax {
		r.cloneMax = r.cloneActive
	}
	r.mu.Unlock()
}

func (r *createRecorder) exitClone() {
	r.mu.Lock()
	r.cloneActive--
	r.mu.Unlock()
}

func (r *createRecorder) nextVMID() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	return r.nextID
}

// upidData writes a UPID string in PVE's {"data": …} envelope, the shape every
// async endpoint returns.
func upidData(w http.ResponseWriter, upid string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"data":%q}`, upid)
}

// upidFor builds a finished-task UPID of a given task type and id, so a specific
// operation's task can be singled out to fail or warn while the rest succeed.
func upidFor(typ string, id int) string {
	return fmt.Sprintf("UPID:pve1:0000ABCD:00000000:65000000:%s:%d:sandbar@pve!prov:", typ, id)
}

// stubProvisioning points the playbook resolver at a hermetic fixture directory
// (so no test touches the real repo/embedded fileset) and the public-key reader
// at a fixed key (so no test needs a real key pair on disk). Returns the fixture
// dir, whose PlaybookVersion is therefore deterministic within the test.
func stubProvisioning(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"site.yml":    "- hosts: all\n",
		"ansible.cfg": "[defaults]\n",
		"inventory":   "localhost\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{"roles", "group_vars"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	oldLocate := locatePlaybookFn
	locatePlaybookFn = func() (string, error) { return dir, nil }
	t.Cleanup(func() { locatePlaybookFn = oldLocate })

	oldKey := readPublicKey
	readPublicKey = func(string) (string, error) { return "ssh-ed25519 AAAAtest sand@test", nil }
	t.Cleanup(func() { readPublicKey = oldKey })

	return dir
}

// registerVM registers every per-VM route the finalize path touches for one VMID:
// its status, the guest-IP discovery pair (config + agent interfaces + ping), the
// power and resize tasks, and the cloud-init config write / regenerate (recorded
// in order). The config route branches on method so the same path serves the
// finalize's GET (guest-IP) and the cloud-init PUT.
func registerVM(m *pveMock, rec *createRecorder, id int) {
	base := fmt.Sprintf("/nodes/pve1/qemu/%d", id)

	m.on(base+"/status/current", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":{"vmid":%d,"name":%q,"status":"stopped"}}`, id, rec.nameFor(id))
	})
	m.data(base+"/agent/ping", `null`)
	m.data(base+"/agent/network-get-interfaces", agentInterfaces)

	m.on(base+"/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			rec.log(fmt.Sprintf("config-write:%d", id))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":null}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":{"net0":"virtio=%s,bridge=vmbr0"}}`, testMAC)
	})
	m.on(base+"/cloudinit", func(w http.ResponseWriter, _ *http.Request) {
		rec.log(fmt.Sprintf("cloudinit-regen:%d", id))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":null}`))
	})

	m.on(base+"/resize", func(w http.ResponseWriter, _ *http.Request) { upidData(w, testUPID) })
	m.on(base+"/status/start", func(w http.ResponseWriter, _ *http.Request) { upidData(w, testUPID) })
	m.on(base+"/status/stop", func(w http.ResponseWriter, _ *http.Request) { upidData(w, testUPID) })
	m.on(base+"/status/shutdown", func(w http.ResponseWriter, _ *http.Request) { upidData(w, testUPID) })
	m.on(base, func(w http.ResponseWriter, r *http.Request) { // DELETE (cleanup / recreate)
		upidData(w, testUPID)
	})
}

// registerBaseBuild wires the whole cold-build-then-clone happy path onto m: the
// base template is ABSENT (so it is downloaded, created, provisioned, and
// templatized), then a VM is cloned from it. The base gets VMID 100 and the clone
// 101 via the stateful nextid. Callers may override individual routes afterward
// (last registration wins) to inject a failure at one stage.
func registerBaseBuild(m *pveMock, rec *createRecorder) {
	rec.nextID = 99 // first nextid -> 100 (base), second -> 101 (clone)

	// Exists-guard and base lookup both read this; empty means neither the target
	// nor the base exists yet.
	m.data("/cluster/resources", `[]`)
	m.data("/nodes/pve1/storage/local-lvm/content", `[]`) // disk-usage index (p.storage)
	// The cloud image is downloaded onto the IMAGE storage (the "local" default),
	// which is distinct from the local-lvm disk storage — content=import only
	// works on a file-based storage. Empty content means the image is absent, so
	// a build downloads it.
	m.data("/nodes/pve1/storage/local/content", `[]`)
	m.on("/nodes/pve1/storage/local/download-url", func(w http.ResponseWriter, _ *http.Request) { upidData(w, testUPID) })

	m.on("/cluster/nextid", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":%q}`, strconv.Itoa(rec.nextVMID()))
	})

	rec.setName(100, "sandbar-base")

	// Base create (POST): record the pool it is created with.
	m.on("/nodes/pve1/qemu", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		rec.mu.Lock()
		rec.basePool = r.PostForm.Get("pool")
		rec.mu.Unlock()
		upidData(w, testUPID)
	})

	registerVM(m, rec, 100)
	m.on("/nodes/pve1/qemu/100/template", func(w http.ResponseWriter, _ *http.Request) { upidData(w, testUPID) })

	// Clone (POST on the template's id): record the pool, the new id, and the
	// name so the new VM's status route reports the name the code just set.
	m.on("/nodes/pve1/qemu/100/clone", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		newid, _ := strconv.Atoi(r.PostForm.Get("newid"))
		rec.setName(newid, r.PostForm.Get("name"))
		rec.mu.Lock()
		rec.clonePool = r.PostForm.Get("pool")
		rec.cloneNewID = r.PostForm.Get("newid")
		rec.mu.Unlock()
		upidData(w, testUPID)
	})

	registerVM(m, rec, 101)
	m.okTask(testUPID)
}

// newCreateProvider builds a provider aimed at m with the provisioning side
// effects stubbed, the ssh transport recorded, and the readiness waits shrunk —
// the standard footing for a lifecycle test.
func newCreateProvider(t *testing.T, m *pveMock) *proxmoxProvider {
	t.Helper()
	stubProvisioning(t)
	shortAgentPolling(t, 5*time.Second)
	p := newProxmoxForTest(t, m)
	recordSSH(p)
	return p
}

// webConfig is the create config the lifecycle tests use: the default toolset and
// sizes, targeting a VM named "web" cloned from the default "sandbar-base".
func webConfig() vm.CreateConfig {
	cfg := vm.DefaultCreateConfig()
	cfg.Name = "web"
	cfg.GitName = "Dev"
	cfg.GitEmail = "dev@example.com"
	return cfg
}

// --- Create: exists guard -------------------------------------------------------

// TestProxmoxCreateRefusesExistingTarget proves Create refuses a name already in
// the pool BEFORE issuing any mutating call — the interface contract, and what
// stops a duplicate clone that would then need cleaning up.
func TestProxmoxCreateRefusesExistingTarget(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", `[{"vmid":100,"name":"web","node":"pve1","pool":"sandbar","status":"running","type":"qemu"}]`)
	m.data("/nodes/pve1/qemu/100/status/current", `{"vmid":100,"name":"web","status":"running"}`)
	p := newCreateProvider(t, m)

	err := p.Create(context.Background(), webConfig(), provision.CreateOptions{}, nil)
	if err == nil {
		t.Fatal("Create: want a refusal for an existing target")
	}
	if !strings.Contains(err.Error(), "web") || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q; want it to say the VM already exists", err)
	}
	for _, mutating := range []string{"/cluster/nextid", "/nodes/pve1/qemu", "/nodes/pve1/qemu/100/clone"} {
		if m.sawPath(mutating) {
			t.Errorf("Create issued a mutating call %s before refusing; requests: %v", mutating, m.seen())
		}
	}
}

// TestProxmoxCreatePermissionErrorIsNotTreatedAsAbsent proves a 403 on the
// existence check is surfaced, never read as "safe to create" — the mistake that
// builds a duplicate on a VM the token merely could not see.
func TestProxmoxCreatePermissionErrorIsNotTreatedAsAbsent(t *testing.T) {
	m := newPVEMock(t)
	m.data("/cluster/resources", `[{"vmid":100,"name":"web","node":"pve1","pool":"sandbar","status":"running","type":"qemu"}]`)
	m.fail("/nodes/pve1/qemu/100/status/current", http.StatusForbidden, "")
	p := newCreateProvider(t, m)

	err := p.Create(context.Background(), webConfig(), provision.CreateOptions{}, nil)
	if err == nil {
		t.Fatal("Create: want the 403 surfaced, not swallowed")
	}
	if m.sawPath("/cluster/nextid") {
		t.Errorf("Create proceeded to build after a 403; requests: %v", m.seen())
	}
}

// --- Create: happy path (pool, cloud-init, progress) ----------------------------

// TestProxmoxCreatePassesPoolOnBaseAndClone is the isolation-model invariant:
// pool membership must be set on BOTH the base create and the clone, or a
// pool-scoped token loses permission on the resulting VMs.
func TestProxmoxCreatePassesPoolOnBaseAndClone(t *testing.T) {
	m := newPVEMock(t)
	rec := &createRecorder{}
	registerBaseBuild(m, rec)
	p := newCreateProvider(t, m)

	if err := p.Create(context.Background(), webConfig(), provision.CreateOptions{}, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	rec.mu.Lock()
	basePool, clonePool, newID := rec.basePool, rec.clonePool, rec.cloneNewID
	rec.mu.Unlock()

	if basePool != "sandbar" {
		t.Errorf("base create pool = %q; want %q — pool membership is what makes later permission checks pass", basePool, "sandbar")
	}
	if clonePool != "sandbar" {
		t.Errorf("clone pool = %q; want %q", clonePool, "sandbar")
	}
	if newID != "101" {
		t.Errorf("clone newid = %q; want the freshly allocated 101", newID)
	}
}

// TestProxmoxCreateRegeneratesCloudInitAfterWrite proves the load-bearing rule:
// every cloud-init config write is followed by a regenerate, because a write
// alone does not rebuild the cloud-init image.
func TestProxmoxCreateRegeneratesCloudInitAfterWrite(t *testing.T) {
	m := newPVEMock(t)
	rec := &createRecorder{}
	registerBaseBuild(m, rec)
	p := newCreateProvider(t, m)

	if err := p.Create(context.Background(), webConfig(), provision.CreateOptions{}, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := rec.ci()
	want := []string{"config-write:101", "cloudinit-regen:101"}
	if len(got) != len(want) {
		t.Fatalf("cloud-init call sequence = %v; want exactly a write then a regenerate %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cloud-init call sequence = %v; want %v (a config write must be followed by a regenerate)", got, want)
		}
	}
}

// TestProxmoxCreateStreamsProgressPerStage proves the TUI sees movement: a line is
// written for each of the multi-minute build's stages.
func TestProxmoxCreateStreamsProgressPerStage(t *testing.T) {
	m := newPVEMock(t)
	rec := &createRecorder{}
	registerBaseBuild(m, rec)
	p := newCreateProvider(t, m)

	var out strings.Builder
	if err := p.Create(context.Background(), webConfig(), provision.CreateOptions{}, &out); err != nil {
		t.Fatalf("Create: %v", err)
	}
	text := out.String()
	for _, stage := range []string{"Building base template", "Creating base VM", "Provisioning", "Converting", "Cloning", "is ready"} {
		if !strings.Contains(text, stage) {
			t.Errorf("progress output missing a line for the %q stage; got:\n%s", stage, text)
		}
	}
}

// --- Create: clone serialization ------------------------------------------------

// TestProxmoxConcurrentCreatesSerializeClones proves two creates against the same
// base template never issue overlapping clone requests. Parallel clones contend
// on the template's server-side flock (a 10s timeout no client can extend and no
// token can release), so the design serializes them client-side.
func TestProxmoxConcurrentCreatesSerializeClones(t *testing.T) {
	m := newPVEMock(t)
	rec := &createRecorder{nextID: 100} // clones take 101, 102

	// Pre-seed a CURRENT base template so both creates skip the build and race
	// straight to the clone — the step under test.
	dir := stubProvisioning(t)
	shortAgentPolling(t, 5*time.Second)
	// The listing is DYNAMIC — the base template plus every clone recorded so far
	// — because a real /cluster/resources reflects a sibling's just-created clone,
	// and the provider's index refresh (setIndex) legitimately relies on that: a
	// static listing that omitted the clones would have one create's index refresh
	// wipe the other's freshly-cached VMID, which never happens against a real
	// cluster.
	m.on("/cluster/resources", func(w http.ResponseWriter, _ *http.Request) {
		rec.mu.Lock()
		items := []string{`{"vmid":100,"name":"sandbar-base","node":"pve1","pool":"sandbar","status":"stopped","type":"qemu","template":1}`}
		for id, name := range rec.names {
			if id == 100 {
				continue
			}
			items = append(items, fmt.Sprintf(`{"vmid":%d,"name":%q,"node":"pve1","pool":"sandbar","status":"running","type":"qemu"}`, id, name))
		}
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(items, ","))
	})
	m.on("/cluster/nextid", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":%q}`, strconv.Itoa(rec.nextVMID()))
	})
	// The clone endpoint detects overlap: it holds a brief window during which a
	// second concurrent clone would be observed.
	m.on("/nodes/pve1/qemu/100/clone", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		newid, _ := strconv.Atoi(r.PostForm.Get("newid"))
		rec.setName(newid, r.PostForm.Get("name"))
		rec.enterClone()
		time.Sleep(30 * time.Millisecond)
		rec.exitClone()
		upidData(w, testUPID)
	})
	registerVM(m, rec, 101)
	registerVM(m, rec, 102)
	m.okTask(testUPID)

	p := newProxmoxForTest(t, m)
	quietSSH(p) // stateless: two Create goroutines drive the transport at once

	// Stamp the base as current so baseStale reuses it rather than rebuilding.
	want, err := provision.PlaybookVersion(os.DirFS(dir), webConfig().ToolsetKey())
	if err != nil {
		t.Fatalf("PlaybookVersion: %v", err)
	}
	if err := provision.WriteBaseVersion(p.files, "sandbar-base", want, time.Now()); err != nil {
		t.Fatalf("WriteBaseVersion: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, name := range []string{"web", "api"} {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			cfg := webConfig()
			cfg.Name = name
			errs[i] = p.Create(context.Background(), cfg, provision.CreateOptions{}, nil)
		}(i, name)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Create[%d]: %v", i, err)
		}
	}
	rec.mu.Lock()
	max := rec.cloneMax
	rec.mu.Unlock()
	if max != 1 {
		t.Fatalf("peak concurrent clones = %d; want 1 — clones from one template must be serialized", max)
	}
	if n := m.count("/nodes/pve1/qemu/100/clone"); n != 2 {
		t.Errorf("clone endpoint hit %d times; want 2 (one per create)", n)
	}
}

// --- Create: partial-failure cleanup --------------------------------------------

// TestProxmoxCreateCleansUpPartialCloneOnFailure proves a clone that succeeds but
// whose later finalize fails does not leave the partial VM behind: it is deleted
// with purge=1, mirroring the other providers' partial-instance cleanup.
func TestProxmoxCreateCleansUpPartialCloneOnFailure(t *testing.T) {
	m := newPVEMock(t)
	rec := &createRecorder{}
	registerBaseBuild(m, rec)

	// Make the clone's own start task FAIL, after the clone itself has landed.
	failUPID := upidFor("qmstart", 101)
	m.on("/nodes/pve1/qemu/101/status/start", func(w http.ResponseWriter, _ *http.Request) { upidData(w, failUPID) })
	m.data("/nodes/pve1/tasks/"+failUPID+"/status", `{"status":"stopped","exitstatus":"start failed: boot device missing"}`)

	deleted := make(chan string, 4)
	m.on("/nodes/pve1/qemu/101", func(w http.ResponseWriter, r *http.Request) {
		deleted <- r.URL.Query().Get("purge")
		upidData(w, testUPID)
	})

	p := newCreateProvider(t, m)

	err := p.Create(context.Background(), webConfig(), provision.CreateOptions{}, nil)
	if err == nil {
		t.Fatal("Create: want a failure when the clone's start fails")
	}
	if !strings.Contains(err.Error(), "removed the partial VM") {
		t.Errorf("error = %q; want it to say the partial VM was cleaned up", err)
	}
	select {
	case purge := <-deleted:
		if purge != "1" {
			t.Errorf("cleanup delete purge = %q; want 1 so no orphaned reference is left", purge)
		}
	default:
		t.Fatalf("the partial clone (VMID 101) was not deleted; requests: %v", m.seen())
	}
}

// --- Create: WARNINGS is success ------------------------------------------------

// TestProxmoxCreateTreatsTaskWarningsAsSuccess proves a task finishing
// "WARNINGS: n" is a success, not a failure: the VM is kept, not recreated, and no
// second "is it really running" status poll is issued (which is where the
// duplicate-VM bug creeps back in).
func TestProxmoxCreateTreatsTaskWarningsAsSuccess(t *testing.T) {
	m := newPVEMock(t)
	rec := &createRecorder{}
	registerBaseBuild(m, rec)

	// The clone's start task finishes with WARNINGS — WaitTask must classify it as
	// success and the create must carry on to a clean finish.
	warnUPID := upidFor("qmstart", 101)
	m.on("/nodes/pve1/qemu/101/status/start", func(w http.ResponseWriter, _ *http.Request) { upidData(w, warnUPID) })
	m.data("/nodes/pve1/tasks/"+warnUPID+"/status", `{"status":"stopped","exitstatus":"WARNINGS: 1"}`)

	p := newCreateProvider(t, m)

	if err := p.Create(context.Background(), webConfig(), provision.CreateOptions{}, nil); err != nil {
		t.Fatalf("Create with a WARNINGS task = %v; want success (WARNINGS is not a failure)", err)
	}
	// Not recreated / not cleaned up: the clone's own delete must never fire.
	if m.count("/nodes/pve1/qemu/101") != 0 {
		t.Errorf("the VM was deleted after a WARNINGS task; requests: %v", m.seen())
	}
}

// --- Create: image extension guard ----------------------------------------------

// TestProxmoxCreateRejectsImgImageEarly proves a configured .img image fails
// before any download, with a message naming the accepted set — PVE's download-url
// rejects .img outright, and failing early beats an opaque task failure minutes in.
func TestProxmoxCreateRejectsImgImageEarly(t *testing.T) {
	oldURL, oldFile := baseImageURL, baseImageFile
	baseImageURL, baseImageFile = "https://example.test/debian.img", "debian.img"
	t.Cleanup(func() { baseImageURL, baseImageFile = oldURL, oldFile })

	m := newPVEMock(t)
	rec := &createRecorder{}
	registerBaseBuild(m, rec)
	p := newCreateProvider(t, m)

	err := p.Create(context.Background(), webConfig(), provision.CreateOptions{}, nil)
	if err == nil {
		t.Fatal("Create: want an early rejection of a .img image")
	}
	if !strings.Contains(err.Error(), "qcow2") || !strings.Contains(err.Error(), "raw") {
		t.Errorf("error = %q; want it to name the accepted extension set", err)
	}
	if m.sawPath("/nodes/pve1/storage/local/download-url") {
		t.Errorf("Create attempted a download of a rejected image; requests: %v", m.seen())
	}
}

// --- Recreate -------------------------------------------------------------------

// TestProxmoxRecreateForceDeletesThenClones proves Recreate force-deletes the
// target first and then clones, skipping the exists-guard (so it does not refuse
// the VM it is about to replace).
func TestProxmoxRecreateForceDeletesThenClones(t *testing.T) {
	m := newPVEMock(t)
	rec := &createRecorder{nextID: 100} // clone takes 101

	dir := stubProvisioning(t)
	shortAgentPolling(t, 5*time.Second)
	// The target VM (web, VMID 105) exists and is running; the base template
	// (100) exists and is current.
	m.data("/cluster/resources", `[
	  {"vmid":100,"name":"sandbar-base","node":"pve1","pool":"sandbar","status":"stopped","type":"qemu","template":1},
	  {"vmid":105,"name":"web","node":"pve1","pool":"sandbar","status":"running","type":"qemu"}
	]`)
	m.data("/nodes/pve1/qemu/105/status/current", `{"vmid":105,"name":"web","status":"running"}`)
	m.on("/nodes/pve1/qemu/105/status/stop", func(w http.ResponseWriter, _ *http.Request) { upidData(w, testUPID) })
	stopped := make(chan struct{}, 1)
	m.on("/nodes/pve1/qemu/105", func(w http.ResponseWriter, _ *http.Request) {
		select {
		case stopped <- struct{}{}:
		default:
		}
		upidData(w, testUPID)
	})
	m.on("/cluster/nextid", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":%q}`, strconv.Itoa(rec.nextVMID()))
	})
	m.on("/nodes/pve1/qemu/100/clone", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		newid, _ := strconv.Atoi(r.PostForm.Get("newid"))
		rec.setName(newid, r.PostForm.Get("name"))
		rec.mu.Lock()
		rec.clonePool = r.PostForm.Get("pool")
		rec.mu.Unlock()
		upidData(w, testUPID)
	})
	registerVM(m, rec, 101)
	m.okTask(testUPID)

	p := newProxmoxForTest(t, m)
	recordSSH(p)
	want, _ := provision.PlaybookVersion(os.DirFS(dir), webConfig().ToolsetKey())
	_ = provision.WriteBaseVersion(p.files, "sandbar-base", want, time.Now())

	if err := p.Recreate(context.Background(), webConfig(), provision.CreateOptions{}, nil); err != nil {
		t.Fatalf("Recreate: %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Errorf("Recreate did not delete the existing target (VMID 105); requests: %v", m.seen())
	}
	if !m.sawPath("/nodes/pve1/qemu/100/clone") {
		t.Errorf("Recreate did not clone a fresh VM; requests: %v", m.seen())
	}
	rec.mu.Lock()
	clonePool := rec.clonePool
	rec.mu.Unlock()
	if clonePool != "sandbar" {
		t.Errorf("recreate clone pool = %q; want %q", clonePool, "sandbar")
	}
}

// --- Reset ----------------------------------------------------------------------

// TestProxmoxResetDeletesAndReclones proves a plain reset (no preservation)
// destroys the VM and re-clones it from the base — the fast rebuild-from-config
// path.
func TestProxmoxResetDeletesAndReclones(t *testing.T) {
	m := newPVEMock(t)
	rec := &createRecorder{nextID: 100}

	dir := stubProvisioning(t)
	shortAgentPolling(t, 5*time.Second)
	m.data("/cluster/resources", `[
	  {"vmid":100,"name":"sandbar-base","node":"pve1","pool":"sandbar","status":"stopped","type":"qemu","template":1},
	  {"vmid":105,"name":"web","node":"pve1","pool":"sandbar","status":"stopped","type":"qemu"}
	]`)
	m.data("/nodes/pve1/qemu/105/status/current", `{"vmid":105,"name":"web","status":"stopped"}`)
	deleted := make(chan struct{}, 1)
	m.on("/nodes/pve1/qemu/105", func(w http.ResponseWriter, _ *http.Request) {
		select {
		case deleted <- struct{}{}:
		default:
		}
		upidData(w, testUPID)
	})
	m.on("/cluster/nextid", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":%q}`, strconv.Itoa(rec.nextVMID()))
	})
	m.on("/nodes/pve1/qemu/100/clone", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		newid, _ := strconv.Atoi(r.PostForm.Get("newid"))
		rec.setName(newid, r.PostForm.Get("name"))
		upidData(w, testUPID)
	})
	registerVM(m, rec, 101)
	m.okTask(testUPID)

	p := newProxmoxForTest(t, m)
	recordSSH(p)
	want, _ := provision.PlaybookVersion(os.DirFS(dir), webConfig().ToolsetKey())
	_ = provision.WriteBaseVersion(p.files, "sandbar-base", want, time.Now())

	if err := p.Reset(context.Background(), webConfig(), provision.ResetOptions{}, nil); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	select {
	case <-deleted:
	default:
		t.Errorf("Reset did not delete the existing VM (105); requests: %v", m.seen())
	}
	if !m.sawPath("/nodes/pve1/qemu/100/clone") {
		t.Errorf("Reset did not re-clone from the base; requests: %v", m.seen())
	}
}

// TestProxmoxResetPreservesStateOverSSH proves the preservation path reuses the
// backend-agnostic guest-side copy machinery (provision.StageOut/StageIn): the
// selected state is tarred OUT of the live source before the destroy and tarred
// back IN after the reclone, and the reset finishes cleanly.
func TestProxmoxResetPreservesStateOverSSH(t *testing.T) {
	m := newPVEMock(t)
	rec := &createRecorder{nextID: 100}

	dir := stubProvisioning(t)
	shortAgentPolling(t, 5*time.Second)
	// The source VM is present and RUNNING so tar can read from it.
	m.data("/cluster/resources", `[
	  {"vmid":100,"name":"sandbar-base","node":"pve1","pool":"sandbar","status":"stopped","type":"qemu","template":1},
	  {"vmid":105,"name":"web","node":"pve1","pool":"sandbar","status":"running","type":"qemu"}
	]`)
	m.data("/nodes/pve1/qemu/105/status/current", `{"vmid":105,"name":"web","status":"running"}`)
	m.data("/nodes/pve1/qemu/105/config", fmt.Sprintf(`{"net0":"virtio=%s,bridge=vmbr0"}`, testMAC))
	m.data("/nodes/pve1/qemu/105/agent/network-get-interfaces", agentInterfaces)
	m.on("/nodes/pve1/qemu/105/status/stop", func(w http.ResponseWriter, _ *http.Request) { upidData(w, testUPID) })
	m.on("/nodes/pve1/qemu/105", func(w http.ResponseWriter, _ *http.Request) { upidData(w, testUPID) })
	m.on("/cluster/nextid", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":%q}`, strconv.Itoa(rec.nextVMID()))
	})
	m.on("/nodes/pve1/qemu/100/clone", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		newid, _ := strconv.Atoi(r.PostForm.Get("newid"))
		rec.setName(newid, r.PostForm.Get("name"))
		upidData(w, testUPID)
	})
	registerVM(m, rec, 101)
	m.okTask(testUPID)

	p := newProxmoxForTest(t, m)
	argvs := recordSSH(p)
	want, _ := provision.PlaybookVersion(os.DirFS(dir), webConfig().ToolsetKey())
	_ = provision.WriteBaseVersion(p.files, "sandbar-base", want, time.Now())

	cfg := webConfig()
	cfg.CloneURL = "https://github.com/acme/web"
	if err := p.Reset(context.Background(), cfg, provision.ResetOptions{PreserveClaude: true, PreserveProject: true}, nil); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	var stagedOut, stagedIn, direnv bool
	for _, argv := range *argvs {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "tar") && strings.Contains(joined, "-czf"):
			stagedOut = true
		case strings.Contains(joined, "tar") && strings.Contains(joined, "-xzf"):
			stagedIn = true
		case strings.Contains(joined, "direnv") && strings.Contains(joined, "allow"):
			direnv = true
		}
	}
	if !stagedOut {
		t.Error("no `tar -czf` stage-out ran; preserved state was never captured")
	}
	if !stagedIn {
		t.Error("no `tar -xzf` stage-in ran; preserved state was never restored")
	}
	if !direnv {
		t.Error("no `direnv allow` ran; the restored project's .env was not re-approved")
	}
}

// --- size parsing ---------------------------------------------------------------

func TestParseSizeToBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"20GiB", 20 << 30, true},
		{"100GiB", 100 << 30, true},
		{"8GiB", 8 << 30, true},
		{"512MiB", 512 << 20, true},
		{"2G", 2 << 30, true},
		{"1024", 1024, true},
		{"", 0, false},
		{"biscuits", 0, false},
		{"10QiB", 0, false},
	}
	for _, tc := range cases {
		got, err := parseSizeToBytes(tc.in)
		if tc.ok && (err != nil || got != tc.want) {
			t.Errorf("parseSizeToBytes(%q) = (%d, %v); want (%d, nil)", tc.in, got, err, tc.want)
		}
		if !tc.ok && err == nil {
			t.Errorf("parseSizeToBytes(%q) = (%d, nil); want an error", tc.in, got)
		}
	}
	if got := memMiB("8GiB"); got != 8192 {
		t.Errorf("memMiB(8GiB) = %d; want 8192", got)
	}
	if got := memMiB(""); got != 0 {
		t.Errorf("memMiB(empty) = %d; want 0 (omitted)", got)
	}
}

func TestAcceptedImportExt(t *testing.T) {
	for _, ok := range []string{"debian.qcow2", "img.RAW", "disk.vmdk", "x.ova", "y.ovf"} {
		if !acceptedImportExt(ok) {
			t.Errorf("acceptedImportExt(%q) = false; want true", ok)
		}
	}
	for _, bad := range []string{"debian.img", "disk.iso", "noext"} {
		if acceptedImportExt(bad) {
			t.Errorf("acceptedImportExt(%q) = true; want false (.img especially is rejected by PVE)", bad)
		}
	}
}

// --- base build failure paths ---------------------------------------------------

// TestProxmoxBaseBuildCleansUpOnCreateTaskFailure covers the base-build cleanup
// path: when the base VM's own create task fails, the partial base VM is purged
// rather than left occupying its VMID under the base name.
func TestProxmoxBaseBuildCleansUpOnCreateTaskFailure(t *testing.T) {
	m := newPVEMock(t)
	rec := &createRecorder{}
	registerBaseBuild(m, rec)

	// The base create POST returns a UPID whose task then FAILS.
	failUPID := upidFor("qmcreate", 100)
	m.on("/nodes/pve1/qemu", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		upidData(w, failUPID)
	})
	m.data("/nodes/pve1/tasks/"+failUPID+"/status", `{"status":"stopped","exitstatus":"import failed"}`)

	deleted := make(chan struct{}, 2)
	m.on("/nodes/pve1/qemu/100", func(w http.ResponseWriter, _ *http.Request) {
		deleted <- struct{}{}
		upidData(w, testUPID)
	})

	p := newCreateProvider(t, m)
	if err := p.Create(context.Background(), webConfig(), provision.CreateOptions{}, nil); err == nil {
		t.Fatal("Create: want a failure when the base create task fails")
	}
	select {
	case <-deleted:
	default:
		t.Errorf("the partial base VM (100) was not cleaned up; requests: %v", m.seen())
	}
}

// TestProxmoxBaseBuildFailsOnDownloadTaskFailure covers ensureCloudImage's
// download-failure branch: a failed download task aborts the build before any VM
// is created.
func TestProxmoxBaseBuildFailsOnDownloadTaskFailure(t *testing.T) {
	m := newPVEMock(t)
	rec := &createRecorder{}
	registerBaseBuild(m, rec)

	dlFail := upidFor("download", 0)
	m.on("/nodes/pve1/storage/local/download-url", func(w http.ResponseWriter, _ *http.Request) { upidData(w, dlFail) })
	m.data("/nodes/pve1/tasks/"+dlFail+"/status", `{"status":"stopped","exitstatus":"404 not found"}`)

	p := newCreateProvider(t, m)
	err := p.Create(context.Background(), webConfig(), provision.CreateOptions{}, nil)
	if err == nil {
		t.Fatal("Create: want a failure when the cloud-image download fails")
	}
	if !strings.Contains(err.Error(), "cloud image") {
		t.Errorf("error = %q; want it to name the cloud-image download", err)
	}
	// No base VM should have been created after a failed download.
	if m.sawPath("/nodes/pve1/qemu/100/template") {
		t.Errorf("a base template was built despite a failed image download; requests: %v", m.seen())
	}
}

// TestProxmoxCreateReusesExistingCloudImage covers ensureCloudImage's
// already-present branch: when the volid is already in storage, no download is
// issued.
func TestProxmoxCreateReusesExistingCloudImage(t *testing.T) {
	m := newPVEMock(t)
	rec := &createRecorder{}
	registerBaseBuild(m, rec)
	// The cloud image is already present in the IMAGE storage's content.
	m.data("/nodes/pve1/storage/local/content",
		`[{"volid":"local:import/`+baseImageFile+`","content":"import","size":100}]`)

	p := newCreateProvider(t, m)
	if err := p.Create(context.Background(), webConfig(), provision.CreateOptions{}, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if m.sawPath("/nodes/pve1/storage/local/download-url") {
		t.Errorf("Create downloaded the image even though it was already present; requests: %v", m.seen())
	}
}

// TestProxmoxImageDownloadUsesImageStorageNotDiskStorage proves the cloud-image
// download targets the file-based image storage (the "local" default), never the
// block disk storage (local-lvm) that would reject content=import.
func TestProxmoxImageDownloadUsesImageStorageNotDiskStorage(t *testing.T) {
	m := newPVEMock(t)
	rec := &createRecorder{}
	registerBaseBuild(m, rec)

	p := newCreateProvider(t, m)
	if err := p.Create(context.Background(), webConfig(), provision.CreateOptions{}, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !m.sawPath("/nodes/pve1/storage/local/download-url") {
		t.Errorf("image download must target the image storage (local); requests: %v", m.seen())
	}
	if m.sawPath("/nodes/pve1/storage/local-lvm/download-url") {
		t.Errorf("image download must NOT target the disk storage (local-lvm); requests: %v", m.seen())
	}
}

// TestProxmoxCreateCleansUpWhenCloneResizeFails covers finalizeClone's resize
// stage: a failed disk resize on the fresh clone aborts the create and purges
// the partial clone.
func TestProxmoxCreateCleansUpWhenCloneResizeFails(t *testing.T) {
	m := newPVEMock(t)
	rec := &createRecorder{}
	registerBaseBuild(m, rec)

	// The clone's resize task fails.
	resizeFail := upidFor("resize", 101)
	m.on("/nodes/pve1/qemu/101/resize", func(w http.ResponseWriter, _ *http.Request) { upidData(w, resizeFail) })
	m.data("/nodes/pve1/tasks/"+resizeFail+"/status", `{"status":"stopped","exitstatus":"resize refused"}`)

	deleted := make(chan struct{}, 2)
	m.on("/nodes/pve1/qemu/101", func(w http.ResponseWriter, _ *http.Request) {
		deleted <- struct{}{}
		upidData(w, testUPID)
	})

	p := newCreateProvider(t, m)
	if err := p.Create(context.Background(), webConfig(), provision.CreateOptions{}, nil); err == nil {
		t.Fatal("Create: want a failure when the clone resize fails")
	}
	select {
	case <-deleted:
	default:
		t.Errorf("the partial clone (101) was not cleaned up after a resize failure; requests: %v", m.seen())
	}
}

// TestProxmoxCreateRebuildsStaleBase covers ensureBaseTemplate's "base exists
// but is stale" branch: an existing base template with no recorded playbook
// version is destroyed and rebuilt before the clone. This is also the path that
// exercises destroyVM (stop-if-running then purge).
func TestProxmoxCreateRebuildsStaleBase(t *testing.T) {
	m := newPVEMock(t)
	rec := &createRecorder{}
	registerBaseBuild(m, rec)

	// The base template already exists (VMID 100, running so destroyVM must stop
	// it first). No version stamp is written, so baseStale reports it stale.
	m.data("/cluster/resources", `[
	  {"vmid":100,"name":"sandbar-base","node":"pve1","pool":"sandbar","status":"running","type":"qemu","template":1}
	]`)
	rec.setName(100, "sandbar-base")

	destroyed := make(chan struct{}, 2)
	m.on("/nodes/pve1/qemu/100", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			destroyed <- struct{}{}
		}
		upidData(w, testUPID)
	})

	p := newCreateProvider(t, m)
	if err := p.Create(context.Background(), webConfig(), provision.CreateOptions{}, nil); err != nil {
		t.Fatalf("Create over a stale base: %v", err)
	}
	select {
	case <-destroyed:
	default:
		t.Errorf("the stale base template was not destroyed before rebuild; requests: %v", m.seen())
	}
}

// TestProxmoxCreateRebuildOptionForcesRebuild covers the opts.Rebuild branch:
// an existing (even current) base is rebuilt when the caller asks for it.
func TestProxmoxCreateRebuildOptionForcesRebuild(t *testing.T) {
	m := newPVEMock(t)
	rec := &createRecorder{}
	registerBaseBuild(m, rec)
	m.data("/cluster/resources", `[
	  {"vmid":100,"name":"sandbar-base","node":"pve1","pool":"sandbar","status":"stopped","type":"qemu","template":1}
	]`)
	rec.setName(100, "sandbar-base")

	rebuilt := make(chan struct{}, 2)
	m.on("/nodes/pve1/qemu/100", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			rebuilt <- struct{}{}
		}
		upidData(w, testUPID)
	})

	p := newCreateProvider(t, m)
	if err := p.Create(context.Background(), webConfig(), provision.CreateOptions{Rebuild: true}, nil); err != nil {
		t.Fatalf("Create --rebuild: %v", err)
	}
	select {
	case <-rebuilt:
	default:
		t.Errorf("opts.Rebuild did not force a rebuild of the existing base; requests: %v", m.seen())
	}
}

// TestProxmoxCreateCleansUpWhenCloudInitWriteFails covers applyCloudInitIdentity's
// failure branch: a failed cloud-init config write on the fresh clone aborts the
// create and purges the partial clone before returning.
func TestProxmoxCreateCleansUpWhenCloudInitWriteFails(t *testing.T) {
	m := newPVEMock(t)
	rec := &createRecorder{}
	registerBaseBuild(m, rec)

	// The clone's cloud-init config PUT fails. registerVM's /config handler serves
	// both the GET (guest-IP) and PUT (cloud-init); override just for VMID 101 to
	// fail the PUT while still answering the GET the finalize needs earlier.
	m.on("/nodes/pve1/qemu/101/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"data":null,"message":"config write refused"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":{"net0":"virtio=%s,bridge=vmbr0"}}`, testMAC)
	})

	deleted := make(chan struct{}, 2)
	m.on("/nodes/pve1/qemu/101", func(w http.ResponseWriter, _ *http.Request) {
		deleted <- struct{}{}
		upidData(w, testUPID)
	})

	p := newCreateProvider(t, m)
	if err := p.Create(context.Background(), webConfig(), provision.CreateOptions{}, nil); err == nil {
		t.Fatal("Create: want a failure when the cloud-init config write fails")
	}
	select {
	case <-deleted:
	default:
		t.Errorf("the partial clone was not cleaned up after a cloud-init write failure; requests: %v", m.seen())
	}
}

// TestProxmoxResetFailsCleanlyWhenRecloneFails covers resetInstance's error path:
// after the old VM is destroyed, a failed reclone from the base surfaces as an
// error rather than a silent half-reset.
func TestProxmoxResetFailsCleanlyWhenRecloneFails(t *testing.T) {
	m := newPVEMock(t)
	rec := &createRecorder{nextID: 100}

	dir := stubProvisioning(t)
	shortAgentPolling(t, 5*time.Second)
	m.data("/cluster/resources", `[
	  {"vmid":100,"name":"sandbar-base","node":"pve1","pool":"sandbar","status":"stopped","type":"qemu","template":1},
	  {"vmid":105,"name":"web","node":"pve1","pool":"sandbar","status":"stopped","type":"qemu"}
	]`)
	m.data("/nodes/pve1/qemu/105/status/current", `{"vmid":105,"name":"web","status":"stopped"}`)
	m.on("/nodes/pve1/qemu/105", func(w http.ResponseWriter, _ *http.Request) { upidData(w, testUPID) })
	m.on("/cluster/nextid", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":%q}`, strconv.Itoa(rec.nextVMID()))
	})
	// The reclone POST itself fails.
	m.fail("/nodes/pve1/qemu/100/clone", http.StatusInternalServerError, "reclone refused")
	m.on("/nodes/pve1/qemu/101", func(w http.ResponseWriter, _ *http.Request) { upidData(w, testUPID) })
	m.okTask(testUPID)

	p := newProxmoxForTest(t, m)
	recordSSH(p)
	want, _ := provision.PlaybookVersion(os.DirFS(dir), webConfig().ToolsetKey())
	_ = provision.WriteBaseVersion(p.files, "sandbar-base", want, time.Now())

	if err := p.Reset(context.Background(), webConfig(), provision.ResetOptions{}, nil); err == nil {
		t.Fatal("Reset: want an error when the reclone fails")
	}
}
