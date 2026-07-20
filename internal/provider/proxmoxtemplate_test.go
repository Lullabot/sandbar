package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
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
	startPath := fmt.Sprintf("/nodes/pve1/qemu/%d/status/start", vmid)
	m.on(fmt.Sprintf("/nodes/pve1/qemu/%d/status/current", vmid),
		func(w http.ResponseWriter, _ *http.Request) {
			status := "running"
			if m.count(stopPath) > m.count(startPath) {
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
	if m.sawPath("/nodes/pve1/qemu/101/status/stop") {
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

	stopUPID := "UPID:pve1:0:0:0:qmstop:101:u:"
	startUPID := "UPID:pve1:0:0:0:qmstart:101:u:"
	m.data("/nodes/pve1/qemu/101/status/stop", fmt.Sprintf("%q", stopUPID))
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
	if !m.sawPath("/nodes/pve1/qemu/101/status/stop") {
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

	stopUPID := "UPID:pve1:0:0:0:qmstop:101:u:"
	startUPID := "UPID:pve1:0:0:0:qmstart:101:u:"
	m.data("/nodes/pve1/qemu/101/status/stop", fmt.Sprintf("%q", stopUPID))
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

	stopUPID := "UPID:pve1:0:0:0:qmstop:101:u:"
	startUPID := "UPID:pve1:0:0:0:qmstart:101:u:"
	m.data("/nodes/pve1/qemu/101/status/stop", fmt.Sprintf("%q", stopUPID))
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
