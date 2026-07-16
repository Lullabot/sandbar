package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/providerfake"
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

// --- Task 10: per-profile header bands and banners ---

// The zero-config single-member fleet must want ZERO extra header bands —
// its one capacity clause stays folded into headerCounts (hostCapacityText),
// exactly as before this task. This is the single-profile parity the task
// promises: nothing about the header's shape changes for the common case.
func TestDesiredHeaderBandsSingleMemberIsZero(t *testing.T) {
	m := newTestModel(t)
	if got := m.desiredHeaderBands(); got != 0 {
		t.Fatalf("desiredHeaderBands() = %d, want 0 for a single-member fleet", got)
	}
	if lines := m.headerBandLines(); lines != nil {
		t.Fatalf("headerBandLines() = %v, want nil for a single-member fleet", lines)
	}
}

// A multi-member fleet wants one band/banner per connected, disabled, or
// errored member — never one for a member still connecting (that span is
// covered by fleetConnectingBanner while the board is otherwise empty).
func TestDesiredHeaderBandsCountsConnectedDisabledErrored(t *testing.T) {
	isolateHostState(t)
	m := New(twoMemberFleet(&providerfake.Provider{}, &providerfake.Provider{})).(model)
	m = resized(m, 120, 40)

	m.members[0].state = connConnected
	m.members[1].state = connConnecting
	if got := m.desiredHeaderBands(); got != 1 {
		t.Fatalf("desiredHeaderBands() = %d, want 1 (one connected, one still connecting)", got)
	}

	m.members[1].state = connDisabled
	if got := m.desiredHeaderBands(); got != 2 {
		t.Fatalf("desiredHeaderBands() = %d, want 2 (connected + disabled)", got)
	}

	m.members[1].state = connErrored
	if got := m.desiredHeaderBands(); got != 2 {
		t.Fatalf("desiredHeaderBands() = %d, want 2 (connected + errored)", got)
	}
}

// A connected member's line is "<profile>: <its own host cpu/mem/disk>" —
// the same use-not-allocation text hostCapacityText renders for the active
// member, just addressed at this one.
func TestMemberStatusLineConnected(t *testing.T) {
	pinHostForHeader(t)
	isolateHostState(t)
	m := New(twoMemberFleet(&providerfake.Provider{}, &providerfake.Provider{})).(model)
	m = resized(m, 120, 40)

	mem := m.members[1]
	mem.state = connConnected
	mem.host.mem = 16 << 30
	mem.host.diskFree = 60 << 30
	mem.host.cpus = 16

	line, ok := m.memberStatusLine(mem)
	if !ok {
		t.Fatalf("memberStatusLine(connected) reported nothing")
	}
	if !strings.HasPrefix(line, mem.profile.Name+": ") {
		t.Fatalf("line = %q, want it to start with the profile's name", line)
	}
	if !strings.Contains(line, "disk free") {
		t.Fatalf("line = %q, want the host capacity text folded in", line)
	}
}

// A disabled member contributes a BANNER, not a stats band: it names the
// profile and says why its tiles are missing.
func TestMemberStatusLineDisabled(t *testing.T) {
	isolateHostState(t)
	m := New(twoMemberFleet(&providerfake.Provider{}, &providerfake.Provider{})).(model)
	mem := m.members[1]
	mem.state = connDisabled

	line, ok := m.memberStatusLine(mem)
	if !ok {
		t.Fatalf("memberStatusLine(disabled) reported nothing")
	}
	if !strings.Contains(line, "disabled") {
		t.Fatalf("line = %q, want it to say the profile is disabled", line)
	}
}

// An errored member's banner names the connection error, so the user
// understands why its VMs are absent instead of just seeing an empty board.
func TestMemberStatusLineErrored(t *testing.T) {
	isolateHostState(t)
	m := New(twoMemberFleet(&providerfake.Provider{}, &providerfake.Provider{})).(model)
	mem := m.members[1]
	mem.state = connErrored
	mem.lastErr = errors.New("ssh: connection refused")

	line, ok := m.memberStatusLine(mem)
	if !ok {
		t.Fatalf("memberStatusLine(errored) reported nothing")
	}
	if !strings.Contains(line, "connection refused") {
		t.Fatalf("line = %q, want the connection error named", line)
	}
}

// A member still connecting has nothing to say in the header yet.
func TestMemberStatusLineConnecting(t *testing.T) {
	isolateHostState(t)
	m := New(twoMemberFleet(&providerfake.Provider{}, &providerfake.Provider{})).(model)
	mem := m.members[1]
	mem.state = connConnecting

	if _, ok := m.memberStatusLine(mem); ok {
		t.Fatalf("memberStatusLine(connecting) should report nothing yet")
	}
}

// When the fleet has more lines to say than the layout granted rows for,
// headerBandLines summarizes the overflow into a single "+K more" row rather
// than silently dropping members off the bottom.
func TestHeaderBandLinesSummarizesOverflow(t *testing.T) {
	isolateHostState(t)
	m := New(twoMemberFleet(&providerfake.Provider{}, &providerfake.Provider{})).(model)
	m = resized(m, 120, 40)
	m.members[0].state = connConnected
	m.members[1].state = connErrored
	m.members[1].lastErr = errors.New("unreachable")

	// Force a granted budget smaller than the fleet's two lines, exactly as a
	// short terminal would (classifyWithHeaderBands, layout.go) — this test
	// pins headerBandLines' OWN summarizing behaviour independent of that
	// negotiation.
	m.layout.HeaderBandLines = 1
	lines := m.headerBandLines()
	if len(lines) != 1 {
		t.Fatalf("headerBandLines() = %v, want exactly 1 line (the granted budget)", lines)
	}
	// budget=1 with 2 lines to say leaves no room for any individual line
	// alongside the summary (shown = budget-1 = 0): the single granted row
	// summarizes both.
	if !strings.Contains(lines[0], "+2 more") {
		t.Fatalf("headerBandLines()[0] = %q, want a \"+2 more\" summary", lines[0])
	}
}

// When per-profile bands are actually rendered, the counts line must NOT also
// carry the active member's host readout — that would print the local stats
// twice (once up top, once in its band). And when a short terminal sheds the
// bands (HeaderBandLines granted 0 despite a multi-member fleet), the combined
// line is the fallback and the readout returns: stats appear exactly once
// either way.
func TestHeaderCountsDropsCapacityWhenBandsRender(t *testing.T) {
	pinHostForHeader(t)
	isolateHostState(t)
	m := New(twoMemberFleet(&providerfake.Provider{}, &providerfake.Provider{})).(model)
	m = resized(m, 120, 40)
	m.members[0].state = connConnected
	m.members[1].state = connConnected

	m.layout.HeaderBandLines = 2 // bands granted: readout lives in the bands
	if line := m.headerCounts(120); strings.Contains(line, "cpu") {
		t.Fatalf("headerCounts with rendered bands still embeds the host readout: %q", line)
	}

	m.layout.HeaderBandLines = 0 // bands shed: combined line is the only home
	if line := m.headerCounts(120); !strings.Contains(line, "cpu") {
		t.Fatalf("headerCounts with bands shed lost the host readout entirely: %q", line)
	}
}
