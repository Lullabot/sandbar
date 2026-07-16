package ui

// fleet_integration_test.go is task 12's cross-cutting gate: the plan's
// success criteria that only make sense with the WHOLE fleet assembled (board
// + header + profile management + registry + secrets + heartbeats together),
// which is why they cannot live inside any single implementation task.
// Everything here is driven with providerfake (or a hand-built fixture on
// disk) — NO real limactl/ssh target.
//
//   - Zero-config parity: no profiles.yaml -> one seeded Local profile,
//     board/header/create-form behave exactly like the pre-profiles path.
//   - Two-profile aggregation: both members' tiles render, each labelled,
//     and the header grows a stats band per connected profile.
//   - Same-name coexistence & secrets isolation: the plan's HIGH-severity
//     regression, now exercised through the real profile-delete mutation
//     rather than a lower-level registry/secrets/heartbeat unit test.
//   - A pre-fleet (v2) secrets file loads intact as local-scoped, through
//     the model's own boot path (New), not just secrets.LoadFrom directly.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// TestZeroConfigParityBoardHeaderFormMatchPreProfiles pins the plan's
// backward-compatibility promise: booting with no profiles.yaml on disk
// seeds exactly one enabled Local profile, and the board/create-form behave
// bit-identically to the pre-profiles single-provider model (the goldens in
// teatest_test.go already pin the RENDERED board pixel-for-pixel; this test
// pins the underlying STATE that makes that possible — one profile, one
// member, a selector that never shows a cycle affordance).
func TestZeroConfigParityBoardHeaderFormMatchPreProfiles(t *testing.T) {
	isolateHostState(t) // XDG_CONFIG_HOME points at an empty temp dir: no profiles.yaml exists yet

	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	m = resized(m, 100, 30)

	list := m.profileList()
	if len(list) != 1 {
		t.Fatalf("zero-config boot should seed exactly one profile, got %d: %+v", len(list), list)
	}
	if list[0].ID != profiles.LocalProfileID || list[0].Type != profiles.TypeLocal || !list[0].Enabled {
		t.Fatalf("seeded profile = %+v, want the enabled permanent Local profile", list[0])
	}
	if len(m.members) != 1 || m.members[0].scope != registry.LocalScope {
		t.Fatalf("zero-config fleet should have exactly one member on LocalScope, got %+v", m.members)
	}

	// The board behaves exactly like the pre-profiles model: a managed VM
	// gets a tile.
	m = loadManaged(t, m, vm.VM{Name: "claude", Status: "Running"})
	if got := boardNames(m); len(got) != 1 || got[0] != "claude" {
		t.Fatalf("board = %v, want [claude] — zero-config board must behave like the pre-profiles path", got)
	}

	// The create form's profile selector has nothing to cycle: with a single
	// profile it must render the bare name, never the "< name >" cycle
	// affordance that would confuse a user who never asked for connection
	// profiles.
	m.openForm()
	if got := len(m.formProfiles()); got != 1 {
		t.Fatalf("formProfiles() = %d, want exactly 1 (Local only)", got)
	}
	if row := m.profileSelectorRow(); strings.Contains(row, "<") || strings.Contains(row, ">") {
		t.Fatalf("profileSelectorRow() = %q, must not show a cycle affordance with only one profile", row)
	}
	if m.formScope != registry.LocalScope {
		t.Fatalf("formScope = %v, want LocalScope", m.formScope)
	}

	// The management screen shows exactly the one seeded profile.
	m.openProfiles()
	view := m.profilesView()
	if !strings.Contains(view, "local") || !strings.Contains(view, "enabled") {
		t.Fatalf("profilesView() should show the seeded local profile as enabled:\n%s", view)
	}
}

// TestTwoProfileAggregationBothConnectedTilesAndBands proves acceptance
// criterion 2: with a Local + a RemoteSSH (fake) profile both CONNECTED,
// tiles from both appear (a real same-screen union, not just "the board
// doesn't wait" — fleet_test.go's own focus), each carrying its owning
// profile's [label], and the header grants one host-stats band per
// connected member.
func TestTwoProfileAggregationBothConnectedTilesAndBands(t *testing.T) {
	isolateHostState(t)
	pinHostCapacity(t, 16<<30, 100<<30)
	seedManagedScoped(t, registry.LocalScope, "claude")
	seedManagedScoped(t, remoteScope, "remote-web")

	m := New(twoMemberFleet(&providerfake.Provider{}, &providerfake.Provider{})).(model)
	m = resized(m, 160, 40) // wide: room for both bands and the full tile grid

	next, _ := m.Update(vmsLoadedMsg{
		scope: registry.LocalScope,
		vms:   []vm.VM{{Name: "claude", Status: "Running", CPUs: 4, Memory: "4294967296", Disk: "107374182400"}},
	})
	m = next.(model)
	next, _ = m.Update(vmsLoadedMsg{
		scope: remoteScope,
		vms:   []vm.VM{{Name: "remote-web", Status: "Running", CPUs: 2, Memory: "2147483648", Disk: "53687091200"}},
	})
	m = next.(model)

	names := boardNames(m)
	if len(names) != 2 || indexOf(names, "claude") < 0 || indexOf(names, "remote-web") < 0 {
		t.Fatalf("board = %v, want tiles from BOTH connected members [claude remote-web]", names)
	}
	if m.members[0].state != connConnected || m.members[1].state != connConnected {
		t.Fatalf("both members should be connected, got %v / %v", m.members[0].state, m.members[1].state)
	}

	// Each tile is labelled by its OWNING profile.
	content := m.View().Content
	if !strings.Contains(content, "[local]") {
		t.Fatalf("the local member's tile should carry the [local] label:\n%s", content)
	}
	if !strings.Contains(content, "[build-host]") {
		t.Fatalf("the remote member's tile should carry the [build-host] label:\n%s", content)
	}

	// The header grew ONE band per connected profile: two members, two bands.
	if got := m.layout.HeaderBandLines; got != 2 {
		t.Fatalf("HeaderBandLines = %d, want 2 (one stats band per connected profile)", got)
	}
}

// TestSameNameCoexistenceAcrossProfilesSecretsHeartbeatsSurviveProfileDelete
// is the plan's HIGH-severity regression, exercised at the level this task
// owns: through the ACTUAL profile-delete mutation (deleteProfile,
// profilesview.go), not just the lower-level registry/secrets/heartbeat unit
// tests scope_safety_test.go already covers. Two enabled profiles each run a
// same-NAMED VM with distinct secrets and a live heartbeat; deleting one
// profile must leave the OTHER's registry entry, secrets and heartbeat
// completely untouched — and must not silently reach into the deleted
// profile's own dormant state either (deleteProfile's documented contract:
// no reconcile, no prune of registry/secrets on delete).
func TestSameNameCoexistenceAcrossProfilesSecretsHeartbeatsSurviveProfileDelete(t *testing.T) {
	isolateHostState(t)

	remoteProf := seedRemoteProfile(t, "build-host", "example.com", "dev", 22)
	_, scope, err := buildProfileProvider(remoteProf)
	if err != nil {
		t.Fatalf("buildProfileProvider: %v", err)
	}
	localProf := profiles.Profile{ID: profiles.LocalProfileID, Name: profiles.DefaultLocalName, Type: profiles.TypeLocal, Enabled: true}
	fleet := provider.Fleet{
		{Profile: localProf, Prov: &providerfake.Provider{}, Scope: registry.LocalScope},
		{Profile: remoteProf, Prov: &providerfake.Provider{}, Scope: scope},
	}
	m := New(fleet).(model)
	m = resized(m, 100, 30)

	const name = "shared-vm"

	// Same VM name, managed under BOTH connection scopes, with distinct
	// secrets — the exact shape the plan's risk log warns about.
	if err := m.reg.AddScoped(vm.CreateConfig{Name: name, BaseName: "sandbar-base"}, registry.LocalScope); err != nil {
		t.Fatalf("seed local %s: %v", name, err)
	}
	if err := m.reg.AddScoped(vm.CreateConfig{Name: name, BaseName: "sandbar-base"}, scope); err != nil {
		t.Fatalf("seed remote %s: %v", name, err)
	}
	if err := m.sec.Set(name, registry.LocalScope, map[string]string{"GH_TOKEN": "local-secret"}); err != nil {
		t.Fatalf("set local secret: %v", err)
	}
	if err := m.sec.Set(name, scope, map[string]string{"GH_TOKEN": "remote-secret"}); err != nil {
		t.Fatalf("set remote secret: %v", err)
	}

	// A live heartbeat on each scope, seeded directly (as scope_safety_test.go
	// does) — no real guest shell involved.
	localHandle := vmHandle{Scope: registry.LocalScope, Name: name}
	remoteHandle := vmHandle{Scope: scope, Name: name}
	m.heartbeats.beats[localHandle] = &heartbeat{epoch: 1, cancel: func() {}, ch: make(chan guestSample, 1), last: guestSample{MemTotal: 111, MemUsed: 11}, seen: true}
	m.heartbeats.beats[remoteHandle] = &heartbeat{epoch: 1, cancel: func() {}, ch: make(chan guestSample, 1), last: guestSample{MemTotal: 222, MemUsed: 22}, seen: true}

	// Both render: the board carries TWO tiles named "shared-vm", one per scope.
	next, _ := m.Update(vmsLoadedMsg{scope: registry.LocalScope, vms: []vm.VM{{Name: name, Status: "Running"}}})
	m = next.(model)
	next, _ = m.Update(vmsLoadedMsg{scope: scope, vms: []vm.VM{{Name: name, Status: "Running"}}})
	m = next.(model)

	count := 0
	for _, n := range boardNames(m) {
		if n == name {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("both same-named VMs should render as independent tiles, got %d occurrences of %q in %v", count, name, boardNames(m))
	}

	// Delete the REMOTE profile.
	m.deleteProfile(remoteProf.ID)
	if m.profileMsg == "" || !strings.Contains(m.profileMsg, remoteProf.Name) {
		t.Fatalf("deleteProfile should report success naming the deleted profile, got %q", m.profileMsg)
	}
	if _, ok := m.memberIndexByProfileID(remoteProf.ID); ok {
		t.Fatal("the deleted profile's member should be gone from the fleet")
	}

	// THE GUARD: the SURVIVING local profile's registry entry, secret and
	// heartbeat are completely untouched.
	if !m.reg.IsManagedInScope(name, registry.LocalScope) {
		t.Fatal("the surviving local profile's registry entry must survive the other profile's delete")
	}
	if got := m.sec.Get(name, registry.LocalScope); got["GH_TOKEN"] != "local-secret" {
		t.Fatalf("the surviving local profile's secret must survive, got %+v", got)
	}
	if _, ok := m.heartbeats.latest(registry.LocalScope, name); !ok {
		t.Fatal("the surviving local profile's heartbeat must survive the other profile's delete")
	}

	// The DELETED profile's own registry/secrets stay dormant on disk (no
	// reconcile, no prune — deleteProfile's documented contract), so they can
	// reappear untouched if the profile is re-added later.
	if !m.reg.IsManagedInScope(name, scope) {
		t.Fatal("the deleted profile's own registry entry must stay dormant, not be pruned")
	}
	if got := m.sec.Get(name, scope); got["GH_TOKEN"] != "remote-secret" {
		t.Fatalf("the deleted profile's own secret must stay dormant, not be pruned, got %+v", got)
	}
	// Its heartbeat, however, IS explicitly stopped by deleteProfile — an open
	// guest shell must never be left running for a member no longer in the
	// fleet (heartbeat.go's syncHeartbeats only reconciles members still
	// present).
	if _, ok := m.heartbeats.latest(scope, name); ok {
		t.Fatal("the deleted profile's own heartbeat must be stopped, not left running")
	}
}

// TestBootLoadsPreFleetV2SecretsFileAsLocalScoped drives the v2->v3 secrets
// migration through the model's OWN boot path (New), not just
// secrets.LoadFrom directly (internal/secrets/secrets_test.go's
// TestLoadFrom_V2FixtureMigratesToLocalScope already pins the package-level
// contract) — proving a real pre-fleet secrets.json, written by a sand build
// that predates connection profiles entirely, is readable through the fleet
// model exactly where it lived before: under LocalScope.
func TestBootLoadsPreFleetV2SecretsFileAsLocalScoped(t *testing.T) {
	isolateHostState(t)

	dataHome := os.Getenv("XDG_DATA_HOME")
	secPath := filepath.Join(dataHome, "sandbar", "secrets.json")
	if err := os.MkdirAll(filepath.Dir(secPath), 0o700); err != nil {
		t.Fatalf("mkdir secrets dir: %v", err)
	}
	// The pre-connection-scope (v2) on-disk shape: vms[name][dirscope] = KEY->VALUE.
	v2 := `{"version":2,"vms":{"web":{"":{"TOKEN":"abc123"}}}}`
	if err := os.WriteFile(secPath, []byte(v2), 0o600); err != nil {
		t.Fatalf("seed v2 secrets fixture: %v", err)
	}

	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)

	if got := m.sec.Get("web", registry.LocalScope); got["TOKEN"] != "abc123" {
		t.Fatalf("a pre-fleet v2 secrets file should load intact under LocalScope, got %+v", got)
	}
	if got := m.sec.Get("web", remoteScope); len(got) != 0 {
		t.Fatalf("a v2-migrated secret must not leak into any other connection scope, got %+v", got)
	}
}
