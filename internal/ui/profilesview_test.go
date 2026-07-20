package ui

// profilesview_test.go covers live fleet mutation: enabling a profile spins
// up its binding and starts its async connect/refresh,
// disabling tears it down and hides its tiles, disable/delete are refused
// while a job is in flight on that profile's scope, and a pure rename is
// live but NOT gated. Everything here is driven with providerfake — NO real
// backend.

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// TestProfileEnableRefreshDisableCycle drives the full live-mutation cycle
// through the actual management-screen flow: creating an enabled RemoteSSH
// profile spins up a connecting member and kicks its connect cmd; a
// simulated successful list brings its tile onto the board; disabling it
// live then hides that tile again, with no restart.
func TestProfileEnableRefreshDisableCycle(t *testing.T) {
	isolateHostState(t)
	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	m = resized(m, 100, 30)

	m.openProfiles()
	cmd := m.openProfileCreateForm()
	if cmd == nil {
		t.Fatal("opening the create form should focus its first field")
	}
	m.profileInputs[pfName].SetValue("build-host")
	m.profileInputs[pfHost].SetValue("example.com")
	m.profileInputs[pfUser].SetValue("dev")
	m.profileInputs[pfPort].SetValue("2222")

	next, cmd := m.submitProfileForm()
	m = next.(model)
	if cmd == nil {
		t.Fatal("creating an enabled profile should kick its connect/list cmd")
	}
	if len(m.members) != 2 {
		t.Fatalf("want 2 members after creating a profile, got %d", len(m.members))
	}
	mem := m.members[1]
	if mem.state != connConnecting {
		t.Fatalf("a freshly-enabled member should start connecting, got %v", mem.state)
	}
	if mem.scope.RemoteTarget != "dev@example.com:2222" {
		t.Fatalf("scope = %q, want dev@example.com:2222", mem.scope.RemoteTarget)
	}
	if m.view != viewProfiles {
		t.Fatalf("submitting the create form should return to the profile list, got view %v", m.view)
	}

	// Simulate the member's first successful list by hand (never executing
	// the real cmd, which would try a real SSH round-trip — no real backend
	// in this test). Seeded directly on the model's OWN in-memory registry
	// (m.reg), not via a fresh registry.Load() (seedManagedScoped's usual
	// shape): New() already loaded its own registry before this profile
	// existed, so a second, independent Load() here would write the file but
	// leave m.reg's in-memory copy none the wiser.
	if err := m.reg.AddScoped(vm.CreateConfig{Name: "remote-thing", BaseName: "sandbar-base"}, mem.scope); err != nil {
		t.Fatalf("seed remote-thing as managed under %v: %v", mem.scope, err)
	}
	updated, _ := m.Update(vmsLoadedMsg{scope: mem.scope, vms: []vm.VM{{Name: "remote-thing", Status: "Running"}}})
	m = updated.(model)
	if got := boardNames(m); len(got) != 1 || got[0] != "remote-thing" {
		t.Fatalf("after a successful list the new member's tile should appear on the board, got %v", got)
	}
	if m.members[1].state != connConnected {
		t.Fatalf("the member should be connected after its list, got %v", m.members[1].state)
	}

	// Disable it live: its tile must vanish immediately, no restart.
	p, ok := m.profileStore.GetByName("build-host")
	if !ok {
		t.Fatal("the new profile should be present in the store")
	}
	m.disableProfile(p.ID)
	if m.profileMsg != "" {
		t.Fatalf("disabling an idle profile should not be refused, got message %q", m.profileMsg)
	}
	if m.members[1].state != connDisabled {
		t.Fatalf("disabling should mark the member connDisabled, got %v", m.members[1].state)
	}
	if got := boardNames(m); len(got) != 0 {
		t.Fatalf("a disabled member's tiles must vanish immediately, got %v", got)
	}

	// A stale in-flight result delivered AFTER the disable must not resurrect
	// the member (model.go's vmsLoadedMsg guard).
	resurrected, _ := m.Update(vmsLoadedMsg{scope: mem.scope, vms: []vm.VM{{Name: "remote-thing", Status: "Running"}}})
	m = resurrected.(model)
	if m.members[1].state != connDisabled {
		t.Fatalf("a stale in-flight result must not undo a disable, got state %v", m.members[1].state)
	}
	if got := boardNames(m); len(got) != 0 {
		t.Fatalf("the disabled member's tiles must stay hidden after a stale result, got %v", got)
	}
}

// TestHeartbeatResolverReflectsLiveEnabledProfile pins a stale-resolver bug:
// the heartbeat registry's scope->shell resolver used to
// be a SNAPSHOT of m.members captured once in New() (fleetShellResolver). A
// profile enabled — or created — AFTER startup could never be resolved to a
// shell, because start()'s resolve(scope) call kept consulting that frozen
// snapshot: its VMs would carry em-dash cpu/mem gauges forever, with no way
// to recover short of restarting sand. The fix rebuilds the resolver at every
// fleet mutation (rebuildMember, called from here by the create-form's submit
// path exactly like TestProfileEnableRefreshDisableCycle above).
//
// This checks the resolver directly (m.heartbeats.shell, a pure in-memory
// lookup) rather than going through start(), which would spawn a background
// goroutine attempting a REAL ssh round trip against the newly-created
// profile's REAL RemoteLima provider (buildProfileProvider never touches the
// network at construction, but ShellStreamOut does) — exactly the kind of
// live backend call this test suite otherwise avoids. Resolving is pure and
// synchronous, so it exercises precisely the bug/fix with no network
// involved.
func TestHeartbeatResolverReflectsLiveEnabledProfile(t *testing.T) {
	isolateHostState(t)
	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	m = resized(m, 100, 30)

	// Baseline: the resolver New() built already knows the Local member's
	// scope.
	if m.heartbeats.shell(registry.LocalScope) == nil {
		t.Fatal("precondition: the resolver should know the Local member's scope from New()")
	}

	// Live-add a second, enabled profile through the REAL create-form submit
	// path (submitProfileForm -> rebuildMember) — the same path
	// TestProfileEnableRefreshDisableCycle drives above.
	m.openProfiles()
	m.openProfileCreateForm()
	m.profileInputs[pfName].SetValue("build-host")
	m.profileInputs[pfHost].SetValue("example.com")
	m.profileInputs[pfUser].SetValue("dev")
	m.profileInputs[pfPort].SetValue("2222")
	next, cmd := m.submitProfileForm()
	m = next.(model)
	if cmd == nil {
		t.Fatal("creating an enabled profile should kick its connect/list cmd")
	}
	if len(m.members) != 2 {
		t.Fatalf("want 2 members after creating a profile, got %d", len(m.members))
	}
	newScope := m.members[1].scope

	// THE FIX: a profile enabled after New() must be resolvable by the
	// heartbeat registry — the resolver must reflect the CURRENT fleet, not a
	// snapshot frozen at startup.
	if m.heartbeats.shell(newScope) == nil {
		t.Fatal("a profile enabled after New() should be resolvable by the heartbeat registry")
	}
}

// TestProfileDisableRefusedWhileJobInFlight proves the idle gate: a
// disable/delete is refused, naming the blocking job, while a build or file
// transfer is running anywhere under that profile's scope — the
// profile-level generalization of the existing per-VM Delete gate.
func TestProfileDisableRefusedWhileJobInFlight(t *testing.T) {
	isolateHostState(t)

	p := seedRemoteProfile(t, "build-host", "example.com", "dev", 22)
	_, scope, err := buildProfileProvider(p)
	if err != nil {
		t.Fatalf("buildProfileProvider: %v", err)
	}

	fleet := provider.Fleet{
		{Profile: profiles.Profile{ID: profiles.LocalProfileID, Type: profiles.TypeLocal, Enabled: true}, Prov: &providerfake.Provider{}, Scope: registry.LocalScope},
		{Profile: p, Prov: &providerfake.Provider{}, Scope: scope},
	}
	m := New(fleet).(model)
	m = resized(m, 100, 30)

	if !m.jobs.begin(&job{key: provisionKey(scope, "building-vm"), state: jobRunning, cancel: func() {}}) {
		t.Fatal("seed a running job")
	}

	m.disableProfile(p.ID)
	if !strings.Contains(m.profileMsg, "building-vm") {
		t.Fatalf("refusal message = %q, want it to name the blocking job (building-vm)", m.profileMsg)
	}
	i, ok := m.memberIndexByProfileID(p.ID)
	if !ok || m.members[i].state == connDisabled {
		t.Fatal("disable must be refused while a job is in flight — the member must stay live")
	}

	// Delete is gated the same way.
	m.profileMsg = ""
	m.deleteProfile(p.ID)
	if !strings.Contains(m.profileMsg, "building-vm") {
		t.Fatalf("delete refusal message = %q, want it to name the blocking job (building-vm)", m.profileMsg)
	}
	if _, ok := m.memberIndexByProfileID(p.ID); !ok {
		t.Fatal("delete must be refused while a job is in flight — the member must not be removed")
	}
	if _, ok := m.profileStore.Get(p.ID); !ok {
		t.Fatal("delete must be refused while a job is in flight — the profile must stay in the store")
	}

	// Once the job finishes, the same disable succeeds.
	if _, ok := m.jobs.finish(provisionKey(scope, "building-vm"), nil); !ok {
		t.Fatal("finish the seeded job")
	}
	m.profileMsg = ""
	m.disableProfile(p.ID)
	if m.profileMsg != "" {
		t.Fatalf("disable should succeed once the job is no longer in flight, got message %q", m.profileMsg)
	}
	if m.members[i].state != connDisabled {
		t.Fatalf("disable should now take effect, got state %v", m.members[i].state)
	}
}

// TestProfileRenameIsLiveAndNotGated proves a pure rename is NOT idle-gated
// (a job in flight on the profile must not block it) and is live with no
// rebuild: the member's scope, its tiles and the store's last-used pointer
// (tracked by the profile's immutable id) all survive the rename untouched.
func TestProfileRenameIsLiveAndNotGated(t *testing.T) {
	isolateHostState(t)
	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	m = resized(m, 100, 30)
	m = putOnBoard(t, m, vm.VM{Name: "web", Status: "Running"})

	// A job in flight would gate a CONNECTION-field edit, but must NOT gate a
	// pure rename.
	if !m.jobs.begin(&job{key: provisionKey(registry.LocalScope, "web"), state: jobRunning, cancel: func() {}}) {
		t.Fatal("seed a running job")
	}

	local, ok := m.profileStore.Get(profiles.LocalProfileID)
	if !ok {
		t.Fatal("expected the seeded local profile")
	}
	if err := m.profileStore.SetLastUsed(local.ID); err != nil {
		t.Fatalf("set last used: %v", err)
	}

	renamed := local
	renamed.Name = "my-laptop"
	cmd := m.submitProfileEdit(renamed)
	if cmd != nil {
		t.Fatal("a pure rename must not rebuild/reconnect the member — no cmd expected")
	}
	if m.profileFormErr != nil {
		t.Fatalf("a pure rename must not be gated by an in-flight job, got error: %v", m.profileFormErr)
	}

	got, ok := m.profileStore.Get(profiles.LocalProfileID)
	if !ok || got.Name != "my-laptop" {
		t.Fatalf("profile name = %+v, want Name=my-laptop", got)
	}
	if m.profileStore.LastUsed() != local.ID {
		t.Fatal("the last-used pointer must survive the rename (tracked by id, not name)")
	}
	if m.members[0].scope != registry.LocalScope {
		t.Fatal("a rename must never change the derived scope")
	}
	if got := boardNames(m); len(got) != 1 || got[0] != "web" {
		t.Fatalf("the VM's tile must survive the rename untouched, got %v", got)
	}
}

// seedRemoteProfile persists a RemoteSSH profile into the SAME profiles.yaml
// New() will independently load (XDG_CONFIG_HOME must already be isolated —
// see isolateHostState), and returns it with its real store-assigned id, so a
// test can build a provider.Fleet binding that shares that id/scope with what
// New's own profileStore holds — exactly how a live profile and its fleet
// binding stay in sync outside tests too (both trace back to one
// profiles.yaml).
func seedRemoteProfile(t *testing.T, name, host, user string, port int) profiles.Profile {
	t.Helper()
	store, err := profiles.Load()
	if err != nil {
		t.Fatalf("load profiles store: %v", err)
	}
	added, err := store.Add(profiles.Profile{
		Name: name, Type: profiles.TypeRemoteSSH, Enabled: true,
		Host: host, User: user, Port: port,
	})
	if err != nil {
		t.Fatalf("add profile: %v", err)
	}
	return added
}
