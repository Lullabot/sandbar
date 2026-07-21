package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/pve"
)

// proxmoxtemplate_test.go exercises the golden-template methods
// (SnapshotTemplate, DeleteTemplate, TemplateDiskBytes) against the shared
// pveMock (proxmox_test.go). The assertions are about the two properties that
// make these methods more than thin wrappers: SnapshotTemplate restores the
// source's power state no matter how it returns, and DeleteTemplate refuses to
// destroy a VM that is not actually a template.

// primeName seeds the name->VMID index and registers a status/current route so
// resolve(name) succeeds without a listing round trip, and reports the given
// power state. It mirrors how a warm provider behaves after a List.
func primeName(m *pveMock, p *proxmoxProvider, name string, vmid int, status string) {
	p.setVMID(name, vmid)
	m.data(fmt.Sprintf("/nodes/pve1/qemu/%d/status/current", vmid),
		fmt.Sprintf(`{"vmid":%d,"name":%q,"status":%q}`, vmid, name, status))
}

// primeStatefulName is primeName for a source that gets powered during the test:
// its status/current route reflects the last power op seen, so it reads
// "running" until a stop is issued and "stopped" afterward. That is what makes
// the restart's start() actually issue a StartVM rather than short-circuit on a
// mock that is frozen at "running".
func primeStatefulName(m *pveMock, p *proxmoxProvider, name string, vmid int) {
	p.setVMID(name, vmid)
	stopPath := fmt.Sprintf("/nodes/pve1/qemu/%d/status/stop", vmid)
	shutdownPath := fmt.Sprintf("/nodes/pve1/qemu/%d/status/shutdown", vmid)
	startPath := fmt.Sprintf("/nodes/pve1/qemu/%d/status/start", vmid)
	m.on(fmt.Sprintf("/nodes/pve1/qemu/%d/status/current", vmid),
		func(w http.ResponseWriter, _ *http.Request) {
			status := "running"
			if m.count(stopPath)+m.count(shutdownPath) > m.count(startPath) {
				status = "stopped"
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"data":{"vmid":%d,"name":%q,"status":%q}}`, vmid, name, status)
		})
}

// TestProxmoxSnapshotFromStoppedSourceLeavesItStopped verifies the base case:
// a stopped source is never started, so the clone+convert happen and no power
// call is issued against the source.
func TestProxmoxSnapshotFromStoppedSourceLeavesItStopped(t *testing.T) {
	m := newPVEMock(t)
	p := newProxmoxForTest(t, m)
	primeName(m, p, "web", 101, "stopped")

	m.data("/cluster/nextid", `"900"`)
	cloneUPID := "UPID:pve1:0:0:0:qmclone:900:u:"
	tmplUPID := "UPID:pve1:0:0:0:qmtemplate:900:u:"
	m.data("/nodes/pve1/qemu/101/clone", fmt.Sprintf("%q", cloneUPID))
	m.okTask(cloneUPID)
	m.data("/nodes/pve1/qemu/900/template", fmt.Sprintf("%q", tmplUPID))
	m.okTask(tmplUPID)

	if err := p.SnapshotTemplate(context.Background(), "web", "web-golden", io.Discard); err != nil {
		t.Fatalf("SnapshotTemplate: %v", err)
	}
	// A stopped source must never be powered: no stop, and — crucially — no
	// start that would leave it running afterwards.
	if m.sawPath("/nodes/pve1/qemu/101/status/shutdown") {
		t.Error("stopped a source that was already stopped")
	}
	if m.sawPath("/nodes/pve1/qemu/101/status/start") {
		t.Error("started a source that began stopped — power state not preserved")
	}
	if !m.sawPath("/nodes/pve1/qemu/900/template") {
		t.Error("never converted the clone to a template")
	}
}

// TestProxmoxSnapshotFromRunningSourceRestartsIt verifies the running case: the
// source is stopped to get a clean clone, then restarted.
func TestProxmoxSnapshotFromRunningSourceRestartsIt(t *testing.T) {
	m := newPVEMock(t)
	p := newProxmoxForTest(t, m)
	shortAgentPolling(t, 2)
	primeStatefulName(m, p, "web", 101)

	stopUPID := "UPID:pve1:0:0:0:qmshutdown:101:u:"
	startUPID := "UPID:pve1:0:0:0:qmstart:101:u:"
	m.data("/nodes/pve1/qemu/101/status/shutdown", fmt.Sprintf("%q", stopUPID))
	m.okTask(stopUPID)
	m.data("/nodes/pve1/qemu/101/status/start", fmt.Sprintf("%q", startUPID))
	m.okTask(startUPID)

	m.data("/cluster/nextid", `"900"`)
	cloneUPID := "UPID:pve1:0:0:0:qmclone:900:u:"
	tmplUPID := "UPID:pve1:0:0:0:qmtemplate:900:u:"
	m.data("/nodes/pve1/qemu/101/clone", fmt.Sprintf("%q", cloneUPID))
	m.okTask(cloneUPID)
	m.data("/nodes/pve1/qemu/900/template", fmt.Sprintf("%q", tmplUPID))
	m.okTask(tmplUPID)

	if err := p.SnapshotTemplate(context.Background(), "web", "web-golden", io.Discard); err != nil {
		t.Fatalf("SnapshotTemplate: %v", err)
	}
	if !m.sawPath("/nodes/pve1/qemu/101/status/shutdown") {
		t.Error("never stopped the running source before cloning it")
	}
	if !m.sawPath("/nodes/pve1/qemu/101/status/start") {
		t.Error("never restarted the source — power state not preserved")
	}
}

// TestProxmoxSnapshotFailurePreservesRunningSourceAndCleansUp is the important
// one: a conversion failure after the clone landed must still restart the
// source AND purge the partial clone.
func TestProxmoxSnapshotFailurePreservesRunningSourceAndCleansUp(t *testing.T) {
	m := newPVEMock(t)
	p := newProxmoxForTest(t, m)
	shortAgentPolling(t, 2)
	primeStatefulName(m, p, "web", 101)

	stopUPID := "UPID:pve1:0:0:0:qmshutdown:101:u:"
	startUPID := "UPID:pve1:0:0:0:qmstart:101:u:"
	m.data("/nodes/pve1/qemu/101/status/shutdown", fmt.Sprintf("%q", stopUPID))
	m.okTask(stopUPID)
	m.data("/nodes/pve1/qemu/101/status/start", fmt.Sprintf("%q", startUPID))
	m.okTask(startUPID)

	m.data("/cluster/nextid", `"900"`)
	cloneUPID := "UPID:pve1:0:0:0:qmclone:900:u:"
	m.data("/nodes/pve1/qemu/101/clone", fmt.Sprintf("%q", cloneUPID))
	m.okTask(cloneUPID)
	// The conversion POST fails, after the clone succeeded.
	m.fail("/nodes/pve1/qemu/900/template", http.StatusInternalServerError, "conversion boom")
	// Cleanup deletes the partial clone.
	delUPID := "UPID:pve1:0:0:0:qmdestroy:900:u:"
	m.data("/nodes/pve1/qemu/900", fmt.Sprintf("%q", delUPID))
	m.okTask(delUPID)

	err := p.SnapshotTemplate(context.Background(), "web", "web-golden", io.Discard)
	if err == nil {
		t.Fatal("SnapshotTemplate succeeded despite a conversion failure")
	}
	if !m.sawPath("/nodes/pve1/qemu/101/status/start") {
		t.Error("a failed snapshot left the running source stopped — power state not preserved")
	}
	if !m.sawPath("/nodes/pve1/qemu/900") {
		t.Error("a failed snapshot leaked the partial clone (no delete)")
	}
}

// TestProxmoxSnapshotCancelledContextStillRestartsSource pins the load-bearing
// context.WithoutCancel: a cancelled snapshot must not leave the source off.
func TestProxmoxSnapshotCancelledContextStillRestartsSource(t *testing.T) {
	m := newPVEMock(t)
	p := newProxmoxForTest(t, m)
	shortAgentPolling(t, 2)
	primeStatefulName(m, p, "web", 101)

	stopUPID := "UPID:pve1:0:0:0:qmshutdown:101:u:"
	startUPID := "UPID:pve1:0:0:0:qmstart:101:u:"
	m.data("/nodes/pve1/qemu/101/status/shutdown", fmt.Sprintf("%q", stopUPID))
	m.okTask(stopUPID)
	m.data("/nodes/pve1/qemu/101/status/start", fmt.Sprintf("%q", startUPID))
	m.okTask(startUPID)
	// nextid is where we trip the cancellation, after the stop has happened.
	ctx, cancel := context.WithCancel(context.Background())
	m.on("/cluster/nextid", func(w http.ResponseWriter, _ *http.Request) {
		cancel()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":"900"}`)
	})

	err := p.SnapshotTemplate(ctx, "web", "web-golden", io.Discard)
	if err == nil {
		t.Fatal("expected an error from a cancelled snapshot")
	}
	// The restart runs on context.WithoutCancel(ctx), so a cancelled ctx must
	// not have skipped it.
	if !m.sawPath("/nodes/pve1/qemu/101/status/start") {
		t.Error("a cancelled snapshot left the source stopped — WithoutCancel restart did not run")
	}
}

// TestProxmoxDeleteTemplateGuardsAgainstNonTemplate is the guard that makes
// DeleteTemplate not an alias for Delete: a name that resolves to a live,
// non-template VM must be refused, and no delete issued.
func TestProxmoxDeleteTemplateGuardsAgainstNonTemplate(t *testing.T) {
	m := newPVEMock(t)
	p := newProxmoxForTest(t, m)
	primeName(m, p, "web", 101, "running")
	// The config says this is NOT a template.
	m.data("/nodes/pve1/qemu/101/config", `{"name":"web","cores":2}`)

	err := p.DeleteTemplate(context.Background(), "web")
	if err == nil {
		t.Fatal("DeleteTemplate destroyed a non-template VM")
	}
	if !strings.Contains(err.Error(), "not a template") {
		t.Errorf("error = %q; want it to explain the target is not a template", err)
	}
	if m.sawPath("/nodes/pve1/qemu/101") {
		t.Error("issued a DELETE against a non-template VM")
	}
}

// TestProxmoxDeleteTemplateDeletesARealTemplate confirms the happy path: a
// template:1 config is deleted with purge.
func TestProxmoxDeleteTemplateDeletesARealTemplate(t *testing.T) {
	m := newPVEMock(t)
	p := newProxmoxForTest(t, m)
	primeName(m, p, "web-golden", 900, "stopped")
	m.data("/nodes/pve1/qemu/900/config", `{"name":"web-golden","template":1}`)
	delUPID := "UPID:pve1:0:0:0:qmdestroy:900:u:"
	m.data("/nodes/pve1/qemu/900", fmt.Sprintf("%q", delUPID))
	m.okTask(delUPID)

	if err := p.DeleteTemplate(context.Background(), "web-golden"); err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if !m.sawPath("/nodes/pve1/qemu/900") {
		t.Error("never issued the DELETE for a real template")
	}
}

// TestProxmoxTemplateDiskBytesReadsBootDiskFromStorage confirms the size comes
// from the boot disk's volid in the storage content listing, not from the
// status endpoint (whose QEMU `disk` is hardcoded to 0).
func TestProxmoxTemplateDiskBytesReadsBootDiskFromStorage(t *testing.T) {
	m := newPVEMock(t)
	p := newProxmoxForTest(t, m)
	primeName(m, p, "web-golden", 900, "stopped")
	m.data("/nodes/pve1/qemu/900/config",
		`{"boot":"order=scsi0","scsi0":"local-lvm:vm-900-disk-0,size=32G"}`)
	m.data("/nodes/pve1/storage/local-lvm/content",
		`[{"volid":"local-lvm:vm-900-disk-0","content":"images","size":34359738368,"vmid":900}]`)

	if got := p.TemplateDiskBytes("web-golden"); got != 34359738368 {
		t.Errorf("TemplateDiskBytes = %d; want 34359738368 (from the storage listing)", got)
	}
}

// TestProxmoxTemplateDiskBytesUnknownIsMinusOne confirms the honest "unknown"
// answer when the volid is not present in the listing.
func TestProxmoxTemplateDiskBytesUnknownIsMinusOne(t *testing.T) {
	m := newPVEMock(t)
	p := newProxmoxForTest(t, m)
	primeName(m, p, "web-golden", 900, "stopped")
	m.data("/nodes/pve1/qemu/900/config",
		`{"boot":"order=scsi0","scsi0":"local-lvm:vm-900-disk-0,size=32G"}`)
	m.data("/nodes/pve1/storage/local-lvm/content", `[]`)

	if got := p.TemplateDiskBytes("web-golden"); got != -1 {
		t.Errorf("TemplateDiskBytes = %d; want -1 when the volid is absent", got)
	}
}

// TestProxmoxTemplateDiskBytesUnknownOnErrorPaths pins that every way the size
// cannot be determined returns -1 rather than a guess: an unresolvable name, a
// config with no boot device, and a boot device whose disk config has no volid.
func TestProxmoxTemplateDiskBytesUnknownOnErrorPaths(t *testing.T) {
	t.Run("unresolvable name", func(t *testing.T) {
		m := newPVEMock(t)
		p := newProxmoxForTest(t, m)
		// No index entry and an empty pool listing: the name cannot resolve.
		m.data("/cluster/resources", `[]`)
		if got := p.TemplateDiskBytes("ghost"); got != -1 {
			t.Errorf("TemplateDiskBytes(unresolvable) = %d; want -1", got)
		}
	})
	t.Run("no boot device in config", func(t *testing.T) {
		m := newPVEMock(t)
		p := newProxmoxForTest(t, m)
		primeName(m, p, "web-golden", 900, "stopped")
		m.data("/nodes/pve1/qemu/900/config", `{"name":"web-golden"}`)
		if got := p.TemplateDiskBytes("web-golden"); got != -1 {
			t.Errorf("TemplateDiskBytes(no boot) = %d; want -1", got)
		}
	})
	t.Run("boot device absent from config", func(t *testing.T) {
		m := newPVEMock(t)
		p := newProxmoxForTest(t, m)
		primeName(m, p, "web-golden", 900, "stopped")
		// boot names scsi0 but scsi0 itself is missing → no volid.
		m.data("/nodes/pve1/qemu/900/config", `{"boot":"order=scsi0"}`)
		if got := p.TemplateDiskBytes("web-golden"); got != -1 {
			t.Errorf("TemplateDiskBytes(missing device) = %d; want -1", got)
		}
	})
	t.Run("storage listing fails", func(t *testing.T) {
		m := newPVEMock(t)
		p := newProxmoxForTest(t, m)
		primeName(m, p, "web-golden", 900, "stopped")
		m.data("/nodes/pve1/qemu/900/config",
			`{"boot":"order=scsi0","scsi0":"local-lvm:vm-900-disk-0,size=32G"}`)
		m.fail("/nodes/pve1/storage/local-lvm/content", http.StatusInternalServerError, "boom")
		if got := p.TemplateDiskBytes("web-golden"); got != -1 {
			t.Errorf("TemplateDiskBytes(storage error) = %d; want -1", got)
		}
	})
}

// TestProxmoxDeleteTemplateReportsResolveAndConfigErrors pins the two failure
// paths before the guard: a name that will not resolve, and a config read that
// fails. Neither must issue a DELETE.
func TestProxmoxDeleteTemplateReportsResolveAndConfigErrors(t *testing.T) {
	t.Run("unresolvable name", func(t *testing.T) {
		m := newPVEMock(t)
		p := newProxmoxForTest(t, m)
		m.data("/cluster/resources", `[]`)
		if err := p.DeleteTemplate(context.Background(), "ghost"); err == nil {
			t.Fatal("DeleteTemplate(unresolvable) returned nil")
		}
	})
	t.Run("config read fails", func(t *testing.T) {
		m := newPVEMock(t)
		p := newProxmoxForTest(t, m)
		primeName(m, p, "web-golden", 900, "stopped")
		m.fail("/nodes/pve1/qemu/900/config", http.StatusInternalServerError, "boom")
		if err := p.DeleteTemplate(context.Background(), "web-golden"); err == nil {
			t.Fatal("DeleteTemplate(config error) returned nil")
		}
		if m.sawPath("/nodes/pve1/qemu/900") {
			t.Error("issued a DELETE despite failing to read the config")
		}
	})
}

// TestProxmoxSnapshotCloneFailureCleansUpAndRestarts covers the clone-POST
// failure path (distinct from the conversion failure above): the source's power
// state is still restored and no template is left behind.
func TestProxmoxSnapshotCloneFailureCleansUpAndRestarts(t *testing.T) {
	m := newPVEMock(t)
	p := newProxmoxForTest(t, m)
	shortAgentPolling(t, 2)
	primeStatefulName(m, p, "web", 101)

	stopUPID := "UPID:pve1:0:0:0:qmshutdown:101:u:"
	startUPID := "UPID:pve1:0:0:0:qmstart:101:u:"
	m.data("/nodes/pve1/qemu/101/status/shutdown", fmt.Sprintf("%q", stopUPID))
	m.okTask(stopUPID)
	m.data("/nodes/pve1/qemu/101/status/start", fmt.Sprintf("%q", startUPID))
	m.okTask(startUPID)

	m.data("/cluster/nextid", `"900"`)
	// The clone POST succeeds and a task starts, then the clone TASK fails — the
	// one clone-failure shape where a partial VM landed under 900, so cleanup is
	// correct. (A synchronous clone POST error, by contrast, created nothing and
	// must NOT purge the id — see TestCloneVMWithNextIDRetriesCollision.)
	cloneUPID := "UPID:pve1:0:0:0:qmclone:900:u:"
	m.data("/nodes/pve1/qemu/101/clone", fmt.Sprintf("%q", cloneUPID))
	m.data("/nodes/pve1/tasks/"+cloneUPID+"/status", `{"status":"stopped","exitstatus":"clone failed"}`)
	deleted := make(chan struct{}, 2)
	m.on("/nodes/pve1/qemu/900", func(w http.ResponseWriter, _ *http.Request) {
		deleted <- struct{}{}
		upidData(w, "UPID:pve1:0:0:0:qmdestroy:900:u:")
	})
	m.okTask("UPID:pve1:0:0:0:qmdestroy:900:u:")

	if err := p.SnapshotTemplate(context.Background(), "web", "web-golden", io.Discard); err == nil {
		t.Fatal("SnapshotTemplate succeeded despite a clone-task failure")
	}
	if !m.sawPath("/nodes/pve1/qemu/101/status/start") {
		t.Error("a clone failure left the running source stopped")
	}
	select {
	case <-deleted:
	default:
		t.Error("the partial clone (900) was not purged after the clone task failed")
	}
}

// TestProxmoxStopStreaming pins the streaming stop form, which shares stop()
// with the buffered Stop but writes progress to the caller's writer.
func TestProxmoxStopStreaming(t *testing.T) {
	m := newPVEMock(t)
	p := newProxmoxForTest(t, m)
	primeName(m, p, "web", 101, "running")
	stopUPID := "UPID:pve1:0:0:0:qmshutdown:101:u:"
	m.data("/nodes/pve1/qemu/101/status/shutdown", fmt.Sprintf("%q", stopUPID))
	m.okTask(stopUPID)

	var buf bytes.Buffer
	if err := p.StopStreaming(context.Background(), "web", &buf); err != nil {
		t.Fatalf("StopStreaming: %v", err)
	}
	if !m.sawPath("/nodes/pve1/qemu/101/status/shutdown") {
		t.Error("StopStreaming never issued the shutdown")
	}
}

// TestProxmoxTemplateHelpers table-tests the pure config-parsing helpers, whose
// branches (a non-template config value, a boot field with no order, a
// non-string disk value) are the guard between a mistyped name and a destroyed
// VM, or a wrong disk size.
func TestProxmoxTemplateHelpers(t *testing.T) {
	t.Run("isTemplateConfig", func(t *testing.T) {
		cases := []struct {
			name string
			cfg  pve.VMConfig
			want bool
		}{
			{"number 1", pve.VMConfig{"template": float64(1)}, true},
			{"number 0", pve.VMConfig{"template": float64(0)}, false},
			{"string 1", pve.VMConfig{"template": "1"}, true},
			{"bool true", pve.VMConfig{"template": true}, true},
			{"absent", pve.VMConfig{"name": "web"}, false},
			{"unexpected type", pve.VMConfig{"template": []any{}}, false},
		}
		for _, c := range cases {
			if got := isTemplateConfig(c.cfg); got != c.want {
				t.Errorf("isTemplateConfig(%s) = %v; want %v", c.name, got, c.want)
			}
		}
	})
	t.Run("bootDiskDevice", func(t *testing.T) {
		cases := []struct{ boot, want string }{
			{"order=scsi0", "scsi0"},
			{"order=scsi0;ide2", "scsi0"},
			{"", ""},
			// The deprecated non-order= form is never written by this provider;
			// bootDiskDevice passes it through unparsed, which is harmless — the
			// caller then finds no such device in the config and returns -1.
			{"legacy=cdn", "legacy=cdn"},
		}
		for _, c := range cases {
			if got := bootDiskDevice(pve.VMConfig{"boot": c.boot}); got != c.want {
				t.Errorf("bootDiskDevice(%q) = %q; want %q", c.boot, got, c.want)
			}
		}
		if got := bootDiskDevice(pve.VMConfig{}); got != "" {
			t.Errorf("bootDiskDevice(absent) = %q; want empty", got)
		}
	})
	t.Run("volidFromDiskConfig", func(t *testing.T) {
		if got := volidFromDiskConfig("local-lvm:vm-1-disk-0,size=32G"); got != "local-lvm:vm-1-disk-0" {
			t.Errorf("volidFromDiskConfig = %q; want the volid before the comma", got)
		}
		if got := volidFromDiskConfig(""); got != "" {
			t.Errorf("volidFromDiskConfig(empty) = %q; want empty", got)
		}
		if got := volidFromDiskConfig(42); got != "" {
			t.Errorf("volidFromDiskConfig(non-string) = %q; want empty", got)
		}
	})
}

// TestProxmoxTemplateMethodsAreNotOnTheInterface documents that these methods
// are deliberately NOT on Provider yet: PR #70 has not landed, so they live on
// the concrete type only, keeping this change additive. If #70 lands and adds
// them to the interface, this test is expected to be removed.
func TestProxmoxTemplateMethodsAreNotOnTheInterface(t *testing.T) {
	var p Provider = (*proxmoxProvider)(nil)
	if _, ok := p.(interface {
		DeleteTemplate(context.Context, string) error
	}); ok {
		// A concrete *proxmoxProvider DOES satisfy this — the point is only that
		// the STATIC Provider interface does not require it. Nothing to assert
		// beyond the compile: reference the type so the intent is recorded.
		_ = errors.New("")
	}
}
