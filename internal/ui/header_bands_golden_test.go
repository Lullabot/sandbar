package ui

// header_bands_golden_test.go locks task 10's rendering: the per-tile
// profile label, and the header's growth from one host-capacity band to one
// band per connected profile plus a banner row per disabled/errored one — at
// the plan's narrowest supported terminal (80 columns) and a wide one, for
// two profiles and for several (mixing connected, disabled and errored).
//
// The single-profile case is already covered by TestTUIBoardGolden80x24 /
// TestTUIBoardGoldenWide (teatest_test.go): this task only added the tile's
// [local] label there, which those goldens now pin.
//
// Multi-member states are driven the same way fleet_test.go's async tests
// already do — real vmsLoadedMsg values through Update, over providerfake —
// rather than the full teatest event loop, since a DISABLED member has no
// live trigger yet (task 8 owns that mutation): this is the deterministic,
// synchronous equivalent the plan's own note allows ("construct deterministic
// member states"), snapshotting m.View() directly against the same golden
// mechanism (golden.RequireEqual) teatest.RequireEqualOutput wraps.

import (
	"errors"
	"testing"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
)

// severalScope is a third profile's scope, distinct from remoteScope
// (fleet_test.go), for the "several profiles" goldens.
var severalScope = registry.Scope{Provider: "lima-remote", RemoteTarget: "user@ci-host:22"}

// disabledScope is a fourth profile's scope, for the disabled-member golden.
var disabledScope = registry.Scope{Provider: "lima-remote", RemoteTarget: "user@archived-host:22"}

// severalMemberFleet builds a four-member fleet: local, an errored remote, a
// connected remote, and a to-be-disabled remote — enough to exercise every
// band/banner kind at once and force the "+K more" degradation at 80 columns.
func severalMemberFleet() provider.Fleet {
	return provider.Fleet{
		{Profile: profiles.Profile{ID: profiles.LocalProfileID, Name: "local", Type: profiles.TypeLocal, Enabled: true},
			Prov: &providerfake.Provider{}, Scope: registry.LocalScope},
		{Profile: profiles.Profile{ID: "remote", Name: "build-host", Type: profiles.TypeRemoteSSH, Enabled: true},
			Prov: &providerfake.Provider{}, Scope: remoteScope},
		{Profile: profiles.Profile{ID: "several", Name: "ci-host", Type: profiles.TypeRemoteSSH, Enabled: true},
			Prov: &providerfake.Provider{}, Scope: severalScope},
		{Profile: profiles.Profile{ID: "disabled", Name: "archived-host", Type: profiles.TypeRemoteSSH, Enabled: true},
			Prov: &providerfake.Provider{}, Scope: disabledScope},
	}
}

// renderModel snapshots m's current View() the same way finalScreen snapshots
// a teatest program's final render: ANSI-stripped, trailing newline, ready for
// golden.RequireEqual.
func renderModel(m model) []byte {
	return []byte(ansi.Strip(m.View().Content) + "\n")
}

// TWO PROFILES, 80 COLUMNS: a connected local member with one VM alongside a
// remote member whose connection failed — the local gets a stats band, the
// remote gets a banner naming its error, and the tile grid below is intact.
func TestTUIHeaderBandsTwoProfiles80x24(t *testing.T) {
	isolateHostState(t)
	pinHostCapacity(t, 16<<30, 100<<30)
	pinVersion(t, "v1.2.3")
	seedManagedScoped(t, registry.LocalScope, "claude")

	m := New(twoMemberFleet(&providerfake.Provider{}, &providerfake.Provider{})).(model)
	m = resized(m, 80, 24)

	next, _ := m.Update(vmsLoadedMsg{
		scope: registry.LocalScope,
		vms:   []vm.VM{{Name: "claude", Status: "Running", CPUs: 4, Memory: "4294967296", Disk: "107374182400"}},
	})
	m = next.(model)
	next, _ = m.Update(vmsLoadedMsg{scope: remoteScope, err: errors.New("ssh: connection refused")})
	m = next.(model)

	golden.RequireEqual(t, renderModel(m))
}

// TWO PROFILES, WIDE: the same fleet state at a size where classify grants
// multiple tile columns and the header stays in its full (title + counts)
// form.
func TestTUIHeaderBandsTwoProfilesWide(t *testing.T) {
	isolateHostState(t)
	pinHostCapacity(t, 16<<30, 100<<30)
	pinVersion(t, "v1.2.3")
	seedManagedScoped(t, registry.LocalScope, "claude")

	m := New(twoMemberFleet(&providerfake.Provider{}, &providerfake.Provider{})).(model)
	m = resized(m, 160, 40)

	next, _ := m.Update(vmsLoadedMsg{
		scope: registry.LocalScope,
		vms:   []vm.VM{{Name: "claude", Status: "Running", CPUs: 4, Memory: "4294967296", Disk: "107374182400"}},
	})
	m = next.(model)
	next, _ = m.Update(vmsLoadedMsg{scope: remoteScope, err: errors.New("ssh: connection refused")})
	m = next.(model)

	golden.RequireEqual(t, renderModel(m))
}

// SEVERAL PROFILES, 80 COLUMNS: four members — local (connected), build-host
// (errored), ci-host (connected), archived-host (disabled) — at the
// narrowest supported terminal, where the header cannot show all four lines
// in full and must degrade (compact/summarize) without breaking the tile
// grid below.
func TestTUIHeaderBandsSeveralProfiles80x24(t *testing.T) {
	isolateHostState(t)
	pinHostCapacity(t, 16<<30, 100<<30)
	pinVersion(t, "v1.2.3")
	seedManagedScoped(t, registry.LocalScope, "claude")
	seedManagedScoped(t, severalScope, "ci-web")

	m := New(severalMemberFleet()).(model)
	m = resized(m, 80, 24)

	next, _ := m.Update(vmsLoadedMsg{
		scope: registry.LocalScope,
		vms:   []vm.VM{{Name: "claude", Status: "Running", CPUs: 4, Memory: "4294967296", Disk: "107374182400"}},
	})
	m = next.(model)
	next, _ = m.Update(vmsLoadedMsg{scope: remoteScope, err: errors.New("ssh: connection refused")})
	m = next.(model)
	next, _ = m.Update(vmsLoadedMsg{
		scope:        severalScope,
		vms:          []vm.VM{{Name: "ci-web", Status: "Running", CPUs: 2, Memory: "2147483648", Disk: "53687091200"}},
		hostMem:      32 << 30,
		hostDiskFree: 200 << 30,
		hostCPUs:     8,
	})
	m = next.(model)
	// Disabling a profile has no live trigger yet (task 8 owns that mutation) —
	// this mirrors it directly, then re-runs the same layout budgeting a resize
	// or a real state transition would (see applySize's callers, model.go).
	m.members[3].state = connDisabled
	m.applySize(m.width, m.height)

	golden.RequireEqual(t, renderModel(m))
}

// SEVERAL PROFILES, SHORT TERMINAL: the same four-member fleet (four lines to
// say) squeezed into a terminal too short to grant all four — the explicit
// HEIGHT degradation rule (classifyWithHeaderBands, layout.go) grants what
// fits and headerBandLines folds the rest into a single "+K more" row,
// rather than the header (or the grid below it) breaking.
func TestTUIHeaderBandsSeveralProfilesDegradeShortTerminal(t *testing.T) {
	isolateHostState(t)
	pinHostCapacity(t, 16<<30, 100<<30)
	pinVersion(t, "v1.2.3")
	seedManagedScoped(t, registry.LocalScope, "claude")
	seedManagedScoped(t, severalScope, "ci-web")

	m := New(severalMemberFleet()).(model)
	m = resized(m, 80, 10)

	next, _ := m.Update(vmsLoadedMsg{
		scope: registry.LocalScope,
		vms:   []vm.VM{{Name: "claude", Status: "Running", CPUs: 4, Memory: "4294967296", Disk: "107374182400"}},
	})
	m = next.(model)
	next, _ = m.Update(vmsLoadedMsg{scope: remoteScope, err: errors.New("ssh: connection refused")})
	m = next.(model)
	next, _ = m.Update(vmsLoadedMsg{
		scope:        severalScope,
		vms:          []vm.VM{{Name: "ci-web", Status: "Running", CPUs: 2, Memory: "2147483648", Disk: "53687091200"}},
		hostMem:      32 << 30,
		hostDiskFree: 200 << 30,
		hostCPUs:     8,
	})
	m = next.(model)
	m.members[3].state = connDisabled
	m.applySize(m.width, m.height)

	if got := m.layout.HeaderBandLines; got >= 4 {
		t.Fatalf("HeaderBandLines = %d, want fewer than the fleet's 4 lines at this height — the test would not exercise degradation", got)
	}

	golden.RequireEqual(t, renderModel(m))
}

// SEVERAL PROFILES, WIDE: the same four-member fleet at a size with room to
// show every band/banner in full — no degradation needed, so this pins what
// "enough room" looks like alongside the 80-column golden's degraded form.
func TestTUIHeaderBandsSeveralProfilesWide(t *testing.T) {
	isolateHostState(t)
	pinHostCapacity(t, 16<<30, 100<<30)
	pinVersion(t, "v1.2.3")
	seedManagedScoped(t, registry.LocalScope, "claude")
	seedManagedScoped(t, severalScope, "ci-web")

	m := New(severalMemberFleet()).(model)
	m = resized(m, 160, 40)

	next, _ := m.Update(vmsLoadedMsg{
		scope: registry.LocalScope,
		vms:   []vm.VM{{Name: "claude", Status: "Running", CPUs: 4, Memory: "4294967296", Disk: "107374182400"}},
	})
	m = next.(model)
	next, _ = m.Update(vmsLoadedMsg{scope: remoteScope, err: errors.New("ssh: connection refused")})
	m = next.(model)
	next, _ = m.Update(vmsLoadedMsg{
		scope:        severalScope,
		vms:          []vm.VM{{Name: "ci-web", Status: "Running", CPUs: 2, Memory: "2147483648", Disk: "53687091200"}},
		hostMem:      32 << 30,
		hostDiskFree: 200 << 30,
		hostCPUs:     8,
	})
	m = next.(model)
	m.members[3].state = connDisabled
	m.applySize(m.width, m.height)

	golden.RequireEqual(t, renderModel(m))
}
