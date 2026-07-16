package ui

// formprofile_test.go covers task 9: the create form's profile selector —
// defaulting to the last-used profile (else Local), retargeting a create at
// the SELECTED profile's provider/scope/host sample, and persisting the
// selection as last-used on a successful create. Driven with providerfake —
// NO real backend.

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"

	tea "charm.land/bubbletea/v2"
)

// buildTwoProfileFleet seeds a REAL remote profile into the on-disk profiles
// store (isolateHostState's XDG_CONFIG_HOME) — so profileStore.List/LastUsed/
// SetLastUsed all see it exactly as New's own profiles.Load() will — and
// returns a local+remote fleet with matching profile IDs, both over
// providerfake, so a create started against either member never touches a
// real backend.
func buildTwoProfileFleet(t *testing.T) (provider.Fleet, profiles.Profile) {
	t.Helper()
	remoteProf := seedRemoteProfile(t, "build-host", "example.com", "dev", 22)
	_, scope, err := buildProfileProvider(remoteProf)
	if err != nil {
		t.Fatalf("buildProfileProvider: %v", err)
	}
	// seedRemoteProfile's first profiles.Load() already seeded the permanent
	// Local profile on disk (profiles.LocalProfileID) — mirror it here so the
	// fleet's local member's profile.ID matches what the store itself holds.
	localProf := profiles.Profile{ID: profiles.LocalProfileID, Name: profiles.DefaultLocalName, Type: profiles.TypeLocal, Enabled: true}
	fleet := provider.Fleet{
		{Profile: localProf, Prov: &providerfake.Provider{}, Scope: registry.LocalScope},
		{Profile: remoteProf, Prov: &providerfake.Provider{}, Scope: scope},
	}
	return fleet, remoteProf
}

// TestCreateFormProfileSelectorDefaultsToLastUsed pins the selector's primary
// default: a profile recorded as last-used (by a prior session, persisted to
// the same on-disk store New reads) is what a freshly opened create form
// highlights — not Local, not whatever happens to be first.
func TestCreateFormProfileSelectorDefaultsToLastUsed(t *testing.T) {
	isolateHostState(t)
	fleet, remoteProf := buildTwoProfileFleet(t)

	store, err := profiles.Load()
	if err != nil {
		t.Fatalf("load profiles store: %v", err)
	}
	if err := store.SetLastUsed(remoteProf.ID); err != nil {
		t.Fatalf("SetLastUsed: %v", err)
	}

	m := New(fleet).(model)
	m.openForm()

	list := m.formProfiles()
	if m.formProfileIdx < 0 || m.formProfileIdx >= len(list) || list[m.formProfileIdx].ID != remoteProf.ID {
		t.Fatalf("selector should default to the last-used profile %q, got index %d of %v", remoteProf.ID, m.formProfileIdx, list)
	}
	mem, ok := m.memberIndexByProfileIDValue(remoteProf.ID)
	if !ok || m.formScope != mem.scope {
		t.Fatalf("formScope should target the last-used profile's member scope %v, got %v", mem.scope, m.formScope)
	}
	if !strings.Contains(m.formView(), remoteProf.Name) {
		t.Fatalf("formView should render the selected profile's name %q:\n%s", remoteProf.Name, m.formView())
	}
}

// TestCreateFormProfileSelectorDefaultsToLocalWithNoLastUsed pins the
// fallback: with no last-used recorded yet, the selector defaults to Local.
func TestCreateFormProfileSelectorDefaultsToLocalWithNoLastUsed(t *testing.T) {
	isolateHostState(t)
	fleet, _ := buildTwoProfileFleet(t)

	m := New(fleet).(model)
	m.openForm()

	list := m.formProfiles()
	if m.formProfileIdx < 0 || m.formProfileIdx >= len(list) || list[m.formProfileIdx].ID != profiles.LocalProfileID {
		t.Fatalf("with no last-used, selector should default to Local, got index %d of %v", m.formProfileIdx, list)
	}
	if m.formScope != registry.LocalScope {
		t.Fatalf("formScope should default to LocalScope, got %v", m.formScope)
	}
}

// TestCreateFormProfileCycleRetargetsScope pins that moving the selector
// (right arrow, while it has focus) retargets formScope to the newly
// highlighted profile's own member — the seam formProvider/formHostSample/
// beginJob all resolve through.
func TestCreateFormProfileCycleRetargetsScope(t *testing.T) {
	isolateHostState(t)
	fleet, remoteProf := buildTwoProfileFleet(t)
	m := New(fleet).(model)
	m.openForm() // no last-used yet: defaults to Local

	if m.formScope != registry.LocalScope {
		t.Fatalf("precondition: should open on Local, got %v", m.formScope)
	}

	m.focusIdx = fProfileSelector
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = next.(model)

	mem, ok := m.memberIndexByProfileIDValue(remoteProf.ID)
	if !ok || m.formScope != mem.scope {
		t.Fatalf("right-arrow on the selector should retarget formScope to the remote profile %v, got %v", mem.scope, m.formScope)
	}

	// One more step wraps back to Local (only two enabled profiles).
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = next.(model)
	if m.formScope != registry.LocalScope {
		t.Fatalf("cycling past the last profile should wrap back to Local, got %v", m.formScope)
	}
}

// TestCreateFormProfileSwitchReseedsHostScaledDefaults pins acceptance
// criterion 3: cpu/memory/user are sampled from the SELECTED member's host,
// and switching profiles mid-form re-seeds them from the newly picked one's
// host sample, not the one the form opened with.
func TestCreateFormProfileSwitchReseedsHostScaledDefaults(t *testing.T) {
	isolateHostState(t)
	fleet, remoteProf := buildTwoProfileFleet(t)
	m := New(fleet).(model)

	// Give the remote member a distinctive, already-sampled host profile so its
	// seeded defaults are unmistakably different from whatever the local
	// machine running this test happens to report.
	idx, ok := m.memberIndexByProfileID(remoteProf.ID)
	if !ok {
		t.Fatal("remote member not found")
	}
	m.members[idx].host = hostSample{cpus: 64, mem: 256 << 30, user: "remoteuser"}

	m.openForm() // defaults to Local (no last-used yet)

	m.focusIdx = fProfileSelector
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = next.(model)

	if got, want := m.inputs[fCPUs].Value(), "32"; got != want { // defaultCPUs(64) == 32
		t.Errorf("CPUs = %q after switching to the remote profile, want %q (its host sample)", got, want)
	}
	if got, want := m.inputs[fMemory].Value(), "8GiB"; got != want { // defaultMemory(256GiB) capped at 8GiB
		t.Errorf("Memory = %q, want %q", got, want)
	}
	if got, want := m.inputs[fUser].Value(), "remoteuser"; got != want {
		t.Errorf("User = %q, want %q", got, want)
	}
}

// TestCreateRoutesToSelectedProfileScopeAndPersistsLastUsed is the end-to-end
// integration test the task calls for: submitting the form after switching
// the selector to the remote profile starts the build under the REMOTE
// member's scope (never Local), records the finished VM as managed under
// that same scope, and persists the remote profile as last-used.
func TestCreateRoutesToSelectedProfileScopeAndPersistsLastUsed(t *testing.T) {
	isolateHostState(t)
	fleet, remoteProf := buildTwoProfileFleet(t)
	m := New(fleet).(model)
	m = resized(m, 100, 30)

	opened, _ := m.Update(runeKey('n'))
	m = opened.(model)
	if m.view != viewForm {
		t.Fatalf("'n' should open the create form, got view %v", m.view)
	}

	// Switch the selector onto the remote profile.
	m.focusIdx = fProfileSelector
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = next.(model)

	mem, ok := m.memberIndexByProfileIDValue(remoteProf.ID)
	if !ok {
		t.Fatal("remote profile should have a live member")
	}
	if m.formScope != mem.scope {
		t.Fatalf("selector should have retargeted formScope to %v, got %v", mem.scope, m.formScope)
	}

	m.inputs[fName].SetValue("remote-web")
	m.inputs[fGitName].SetValue("Ada Lovelace")
	m.inputs[fGitEmail].SetValue("ada@example.com")

	l := newTeaLoop(t, m)
	l.send(ctrlKey('s'))

	if !l.m.jobs.isRunning(mem.scope, "remote-web") {
		t.Fatalf("submitting should start the build under the SELECTED profile's scope %v", mem.scope)
	}
	if l.m.jobs.isRunning(registry.LocalScope, "remote-web") {
		t.Fatal("the build must not be tracked under the local scope")
	}

	l.pump("remote-web to finish", func(m model) bool { return !m.jobs.isRunning(mem.scope, "remote-web") })

	if !l.m.reg.IsManagedInScope("remote-web", mem.scope) {
		t.Fatalf("the new VM should be recorded as managed under the selected profile's scope %v", mem.scope)
	}
	if l.m.reg.IsManagedInScope("remote-web", registry.LocalScope) {
		t.Fatal("the new VM must not be recorded under the local scope")
	}
	if got := l.m.profileStore.LastUsed(); got != remoteProf.ID {
		t.Fatalf("last-used should be updated to the selected profile %q, got %q", remoteProf.ID, got)
	}
}
