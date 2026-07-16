package ui

// hostwarn_integration_test.go drives the FULL pipeline a real refresh takes:
// a vmsLoadedMsg landing on Update (model.go), adopting its host sample onto
// the member and invoking checkHostCapacityWarn on the success path — rather
// than calling checkHostMemWarn/checkHostDiskWarn directly (hostwarn_test.go
// already covers the latch logic in isolation). This is what proves the
// wiring itself: msg.hostMemAvail/hostDiskTotal actually reach mem.host, and
// the check actually runs once per successful list of a CONNECTED member.

import (
	"errors"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// A CONNECTED member's list crossing below 5% free (either resource) logs a
// warning; a subsequent steady-state refresh below the same line must not
// repeat it (the 5s loop calling this every tick is exactly the spam this
// guards against).
func TestVmsLoadedMsgLogsHostWarningOnceThenLatches(t *testing.T) {
	isolateHostState(t)
	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	m = resized(m, 120, 40)

	const memTotal = int64(32) << 30
	nm, _ := m.Update(vmsLoadedMsg{
		scope: registry.LocalScope, vms: []vm.VM{},
		hostMem: memTotal, hostMemAvail: memTotal * 4 / 100, // 4% free: below the line
	})
	m = nm.(model)
	if log := messageLog(m); !strings.Contains(log, "memory low") {
		t.Fatalf("crossing below 5%% free on a successful list must log a warning:\n%s", log)
	}

	before := len(m.messages)
	nm, _ = m.Update(vmsLoadedMsg{
		scope: registry.LocalScope, vms: []vm.VM{},
		hostMem: memTotal, hostMemAvail: memTotal * 4 / 100,
	})
	m = nm.(model)
	if len(m.messages) != before {
		t.Fatalf("a steady-state refresh still below 5%% must not log again, got %d new: %v", len(m.messages)-before, m.messages[before:])
	}
}

// The disk twin, through the same Update path.
func TestVmsLoadedMsgLogsDiskWarningViaUpdate(t *testing.T) {
	isolateHostState(t)
	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	m = resized(m, 120, 40)

	const diskTotal = int64(500) << 30
	nm, _ := m.Update(vmsLoadedMsg{
		scope: registry.LocalScope, vms: []vm.VM{},
		hostDiskFree: diskTotal * 2 / 100, hostDiskTotal: diskTotal, // 2% free
	})
	m = nm.(model)
	if log := messageLog(m); !strings.Contains(log, "disk low") {
		t.Fatalf("crossing below 5%% disk free on a successful list must log a warning:\n%s", log)
	}
}

// A member that is NOT connected (its list failed) must never warn, even if
// the host sample it adopted before the error branch happens to be below the
// line — rule 1/2 apply to CONNECTED members only.
func TestVmsLoadedMsgNeverWarnsForAnErroredMember(t *testing.T) {
	isolateHostState(t)
	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	m = resized(m, 120, 40)

	const memTotal = int64(32) << 30
	nm, _ := m.Update(vmsLoadedMsg{
		scope:   registry.LocalScope,
		hostMem: memTotal, hostMemAvail: memTotal * 4 / 100,
		err: errors.New("list failed"),
	})
	m = nm.(model)
	if log := messageLog(m); strings.Contains(log, "memory low") {
		t.Fatalf("an errored (not connected) member must never log a host capacity warning:\n%s", log)
	}
}
