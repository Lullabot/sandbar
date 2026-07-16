package ui

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	"github.com/charmbracelet/x/ansi"
)

// seedSample plants a heartbeat reading for a VM, as if its guest had just
// reported one. The header and the tiles read live usage from exactly one place —
// the heartbeat registry — so this is the only thing a test has to fake to drive
// either of them.
func seedSample(m *model, name string, s guestSample) {
	if m.heartbeats.beats == nil {
		m.heartbeats.beats = map[vmHandle]*heartbeat{}
	}
	m.heartbeats.beats[vmHandle{Scope: registry.LocalScope, Name: name}] = &heartbeat{
		cancel: func() {},
		ch:     make(chan guestSample, 1),
		last:   s,
		seen:   true,
	}
}

// pinHostForHeader fixes the host probes so the header's text is exact and
// portable — real core counts, RAM and disk sizes are none of those things.
func pinHostForHeader(t *testing.T) {
	t.Helper()
	pinHostCapacity(t, 16<<30, 60<<30) // 16 cores (pinned in the helper), 16GiB RAM, 60GiB free
}

// THE HEADER REPORTS USE, NOT ALLOCATION. A VM is GIVEN 8 vCPUs and 8GiB and may
// be using almost none of it; summing what it was given answers a question nobody
// asked and reads as a crisis on an idle machine. The numbers here come from the
// guest heartbeat — the same source the tiles' gauges read — so the two surfaces
// cannot contradict each other.
func TestHeaderReportsLiveUseNotAllocation(t *testing.T) {
	pinHostForHeader(t)
	m := newTestModel(t)
	m = resized(m, 120, 40)
	// Allocated 8 vCPUs and 8GiB, but barely working: 25% of its own 8 cores is 2
	// host vCPUs busy, and it is holding 2GiB of the 8 it was handed.
	m = loadManaged(t, m, vm.VM{Name: "web", Status: "Running", CPUs: 8, Memory: "8589934592"})
	seedSample(&m, "web", guestSample{
		CPUPct: 25, HasCPU: true,
		MemUsed: 2 << 30, MemTotal: 8 << 30,
	})

	counts := m.headerCounts(m.layout.ContentWidth)
	// 25% of its own 8 vCPUs = 2.0 busy cores; on a 16-core host that is 13% of the
	// machine. The header's scale is the WHOLE HOST, unlike the tile's, which is a
	// share of that one VM's vCPUs.
	if !strings.Contains(counts, "cpu 12%") { // 12.5% rounds to 12
		t.Fatalf("header = %q, want the live cpu load as a share of the host (2.0 of 16 cores)", counts)
	}
	if !strings.Contains(counts, "mem 2 GiB/16 GiB") {
		t.Fatalf("header = %q, want the memory the guest is actually USING (2 GiB), not its 8 GiB allocation", counts)
	}
	// The allocation must not appear at all: "8 vCPU" was the old readout.
	if strings.Contains(counts, "8 vCPU") {
		t.Fatalf("header = %q, must not report the ALLOCATION", counts)
	}
	if !strings.Contains(counts, "mem 2 GiB/16 GiB") {
		t.Fatalf("header = %q, want the memory the guest is actually USING (2 GiB), not its 8 GiB allocation", counts)
	}
	if !strings.Contains(counts, "disk free") {
		t.Fatalf("header = %q, want free disk kept", counts)
	}
	if view := ansi.Strip(m.boardView()); !strings.Contains(view, "cpu 12%") {
		t.Fatalf("the live readout must reach the rendered board, got:\n%s", view)
	}
}

// A running VM with NO reading yet — the heartbeat has not reported, or the idle
// gate tore it down — must not be reported as using nothing. Zero is a claim; the
// header does not have one to make, and says so.
func TestHeaderRefusesToInventAZeroWhenNothingIsReporting(t *testing.T) {
	pinHostForHeader(t)
	m := newTestModel(t)
	m = resized(m, 120, 40)
	m = loadManaged(t, m, vm.VM{Name: "web", Status: "Running", CPUs: 8, Memory: "8589934592"})
	// Deliberately no seedSample: the VM is up, nothing has reported.

	counts := m.headerCounts(m.layout.ContentWidth)
	if !strings.Contains(counts, "cpu —") || !strings.Contains(counts, "mem —/16 GiB") {
		t.Fatalf("header = %q, want an em dash for an unread metric, not a fabricated 0", counts)
	}
	if strings.Contains(counts, "cpu 0%") || strings.Contains(counts, "mem 0 B/") {
		t.Fatalf("header = %q, must not claim an idle fleet when it simply has no reading", counts)
	}
}

// The hidden count is GONE — removed on request in favour of the live host
// readout. The board is still managed-clones-only, so this pins what that now
// costs: a base image and a foreign VM get no tile AND no mention anywhere. If
// that invisibility ever bites, the fix is to bring the count back.
func TestHiddenVMsGetNoTileAndAreNoLongerCounted(t *testing.T) {
	pinHostForHeader(t)
	m := newTestModel(t)
	m = resized(m, 120, 40)
	if err := m.reg.Add(vm.CreateConfig{Name: "web", BaseName: "sandbar-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "web", Status: "Running"},              // managed clone: gets a tile
		{Name: "sandbar-base", Status: "Stopped"},     // base image: hidden
		{Name: "someone-elses-vm", Status: "Running"}, // unrelated VM: hidden
	}})
	m = loaded.(model)

	view := ansi.Strip(m.boardView())
	if strings.Contains(view, "sandbar-base") || strings.Contains(view, "someone-elses-vm") {
		t.Fatalf("hidden VMs must get no tile, got:\n%s", view)
	}
	if strings.Contains(view, "hidden") {
		t.Fatalf("the hidden count was removed; the header must not still be claiming one:\n%s", view)
	}
	// The fleet count still describes the board itself.
	if !strings.Contains(view, "1 sandbox (1 running)") {
		t.Fatalf("the header must still count the fleet on the board, got:\n%s", view)
	}
}

// The live readout must survive the plan's narrowest supported terminal — it is
// the header's whole payload now, and headerCounts drops it only if it genuinely
// cannot fit.
func TestHeaderReadoutSurvivesAt80x24(t *testing.T) {
	pinHostForHeader(t)
	m := newTestModel(t)
	m = resized(m, 80, 24)
	m = loadManaged(t, m, vm.VM{Name: "web", Status: "Running", CPUs: 4, Memory: "4294967296"})
	seedSample(&m, "web", guestSample{CPUPct: 50, HasCPU: true, MemUsed: 1 << 30, MemTotal: 4 << 30})

	view := ansi.Strip(m.boardView())
	if !strings.Contains(view, "cpu 12%") { // 50% of 4 vCPUs = 2.0 busy of 16 cores
		t.Fatalf("80x24 must still carry the live host readout, got:\n%s", view)
	}
}

// The header and the tile are two renderings of ONE reading. They read the same
// registry, so a number shown on a tile and a number shown in the header can never
// come from different facts — this pins that they agree.
func TestHeaderAndTileAgreeOnTheSameSample(t *testing.T) {
	pinHostForHeader(t)
	m := newTestModel(t)
	m = resized(m, 120, 40)
	m = loadManaged(t, m, vm.VM{Name: "web", Status: "Running", CPUs: 4, Memory: "4294967296"})
	seedSample(&m, "web", guestSample{CPUPct: 75, HasCPU: true, MemUsed: 3 << 30, MemTotal: 4 << 30})

	view := ansi.Strip(m.boardView())
	// ONE sample, TWO honest scales: the tile says 75% (of this VM's 4 vCPUs), the
	// header says 19% (3.0 busy cores out of the host's 16). They must not be
	// "reconciled" into one number — they answer different questions.
	if !strings.Contains(view, "cpu 19%") {
		t.Fatalf("header should report 3.0 of 16 host cores busy = 19%%, got:\n%s", view)
	}
	if !strings.Contains(view, "75%") { // the tile's own cpu gauge, same sample
		t.Fatalf("the tile should show the same reading on ITS scale (75%%), got:\n%s", view)
	}
}
