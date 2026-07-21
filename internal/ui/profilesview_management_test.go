package ui

// profilesview_management_test.go drives the profile management screen
// through the REAL key dispatch path — updateProfiles and
// updateProfileForm, exactly as a user's keystrokes travel — rather than
// calling its handlers directly. profilesview_test.go already pins the
// live-mutation BEHAVIOUR at the method level (enable/disable/delete/rename);
// this file closes the coverage gap in the key-dispatch layer itself
// (updateProfiles, profileFormFocusNext/Prev, currentProfile,
// openProfileEditForm, submitProfileForm's validation branches) and extends
// the block-until-idle gate to the actual keys a user presses ('d', 't')
// rather than only the model methods they call. Driven entirely with
// providerfake — no real backend.

import (
	"errors"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/providerfake"
	"github.com/lullabot/sandbar/internal/registry"

	tea "charm.land/bubbletea/v2"
)

// TestManagementScreenKeyDrivenCreateEditToggleDelete drives the ENTIRE
// profile management lifecycle through real keys, start to finish: opening
// the screen, a boundary-clamped cursor, refusing to delete the permanent
// Local profile, creating a RemoteSSH profile (with the validation errors a
// user would actually hit along the way), editing it (cancel, then a real
// connection-field change that rebuilds its live binding), toggling it
// disabled/enabled, and deleting it with a confirm/cancel round trip before
// the real confirm.
func TestManagementScreenKeyDrivenCreateEditToggleDelete(t *testing.T) {
	isolateHostState(t)
	m := New(singleFleet(&providerfake.Provider{}, registry.LocalScope)).(model)
	m = resized(m, 100, 30)

	next, _ := m.Update(runeKey('p'))
	m = next.(model)
	if m.view != viewProfiles {
		t.Fatalf("'p' should open the profile management screen, got view %v", m.view)
	}
	if len(m.profileList()) != 1 {
		t.Fatalf("zero-config: expected exactly 1 seeded profile, got %d", len(m.profileList()))
	}

	// The cursor clamps at both boundaries with only one profile.
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = next.(model)
	if m.profileCursor != 0 {
		t.Fatalf("cursor should clamp at 0 on Up, got %d", m.profileCursor)
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = next.(model)
	if m.profileCursor != 0 {
		t.Fatalf("cursor should clamp at 0 on Down with only one profile, got %d", m.profileCursor)
	}

	// The permanent Local profile refuses deletion even via the real key.
	next, _ = m.Update(runeKey('d'))
	m = next.(model)
	if !strings.Contains(m.profileMsg, "permanent") {
		t.Fatalf("deleting Local via 'd' should refuse, got message %q", m.profileMsg)
	}
	if m.profileConfirmDeleteID != "" {
		t.Fatal("no delete confirmation should be pending for the permanent Local profile")
	}

	// 'n' opens the create form.
	next, _ = m.Update(runeKey('n'))
	m = next.(model)
	if m.view != viewProfileForm || m.profileFormID != "" {
		t.Fatalf("'n' should open a blank create form, view=%v id=%q", m.view, m.profileFormID)
	}

	// Validation: empty name.
	next, _ = m.Update(ctrlKey('s'))
	m = next.(model)
	if m.view != viewProfileForm {
		t.Fatal("an invalid submit must not leave the form")
	}
	if m.profileFormErr == nil || !strings.Contains(m.profileFormErr.Error(), "name is required") {
		t.Fatalf("empty name error = %v, want it to mention name is required", m.profileFormErr)
	}

	// Type the name via real keys, tab to Host, submit with no host/user yet.
	m = typeInto(m, "build-host")
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = next.(model)
	if m.profileFormFocus != pfHost {
		t.Fatalf("tab should advance focus to Host, got %d", m.profileFormFocus)
	}
	next, _ = m.Update(ctrlKey('s'))
	m = next.(model)
	if m.profileFormErr == nil || !strings.Contains(m.profileFormErr.Error(), "host and user are required") {
		t.Fatalf("missing host/user error = %v", m.profileFormErr)
	}

	m = typeInto(m, "example.com")
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = next.(model)
	if m.profileFormFocus != pfUser {
		t.Fatalf("tab should advance focus to User, got %d", m.profileFormFocus)
	}
	m = typeInto(m, "dev")

	// shift+tab walks focus BACKWARD.
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	m = next.(model)
	if m.profileFormFocus != pfHost {
		t.Fatalf("shift+tab should move focus back to Host, got %d", m.profileFormFocus)
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = next.(model) // User
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = next.(model) // Port
	if m.profileFormFocus != pfPort {
		t.Fatalf("tab should now be on Port, got %d", m.profileFormFocus)
	}

	// An invalid port is rejected.
	m = typeInto(m, "not-a-number")
	next, _ = m.Update(ctrlKey('s'))
	m = next.(model)
	if m.profileFormErr == nil || !strings.Contains(m.profileFormErr.Error(), "port must be a positive number") {
		t.Fatalf("invalid port error = %v", m.profileFormErr)
	}

	// Clear the bad port and enter a valid one.
	m.profileInputs[pfPort].SetValue("")
	m = typeInto(m, "2222")

	next, _ = m.Update(ctrlKey('s'))
	m = next.(model)
	if m.view != viewProfiles {
		t.Fatalf("a valid create should return to the profile list, got view %v", m.view)
	}
	if len(m.profileList()) != 2 {
		t.Fatalf("expected 2 profiles after create, got %d", len(m.profileList()))
	}

	created, ok := m.profileStore.GetByName("build-host")
	if !ok {
		t.Fatal("the created profile should be findable by name")
	}
	if created.Host != "example.com" || created.User != "dev" || created.Port != 2222 {
		t.Fatalf("created profile = %+v, want host=example.com user=dev port=2222", created)
	}
	if idx, ok := m.memberIndexByProfileID(created.ID); !ok || m.members[idx].state != connConnecting {
		t.Fatalf("creating an enabled profile should spin up a connecting member, ok=%v", ok)
	}

	// Move the cursor onto the new profile and open it for EDIT.
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = next.(model)
	if m.profileCursor != 1 {
		t.Fatalf("cursor should be on the new profile, got %d", m.profileCursor)
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	if m.view != viewProfileForm || m.profileFormID != created.ID {
		t.Fatalf("enter should open the edit form for the selected profile, view=%v id=%q", m.view, m.profileFormID)
	}
	if got := m.profileInputs[pfName].Value(); got != "build-host" {
		t.Fatalf("edit form should be pre-filled with the profile's name, got %q", got)
	}
	if got := m.profileInputs[pfHost].Value(); got != "example.com" {
		t.Fatalf("edit form should be pre-filled with the profile's host, got %q", got)
	}

	// Cancel the edit with esc: no changes persisted, back to the list.
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = next.(model)
	if m.view != viewProfiles {
		t.Fatalf("esc should return to the profile list, got view %v", m.view)
	}
	if got, _ := m.profileStore.Get(created.ID); got.Host != "example.com" {
		t.Fatalf("cancelling the edit must not change anything, got host %q", got.Host)
	}

	// Re-open the edit and this time actually change a CONNECTION field and
	// save it — submitProfileForm's EDIT branch, and submitProfileEdit's
	// "connection changed on an enabled profile" rebuild branch.
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(model)
	m.profileInputs[pfHost].SetValue("otherhost.example.com")
	next, _ = m.Update(ctrlKey('s'))
	m = next.(model)
	if m.view != viewProfiles {
		t.Fatalf("a valid edit-save should return to the profile list, got view %v", m.view)
	}
	got, ok := m.profileStore.Get(created.ID)
	if !ok || got.Host != "otherhost.example.com" {
		t.Fatalf("the connection-field edit should have persisted, got %+v", got)
	}

	// TOGGLE (disable) via 't'.
	next, _ = m.Update(runeKey('t'))
	m = next.(model)
	if m.profileMsg != "" {
		t.Fatalf("disabling an idle profile should not be refused, got %q", m.profileMsg)
	}
	idx, ok := m.memberIndexByProfileID(created.ID)
	if !ok || m.members[idx].state != connDisabled {
		t.Fatalf("'t' should disable the connected profile, ok=%v state=%v", ok, m.members[idx].state)
	}

	// TOGGLE again (re-enable).
	next, _ = m.Update(runeKey('t'))
	m = next.(model)
	idx, ok = m.memberIndexByProfileID(created.ID)
	if !ok || m.members[idx].state != connConnecting {
		t.Fatalf("'t' should re-enable and start reconnecting, ok=%v state=%v", ok, m.members[idx].state)
	}

	// DELETE with confirmation: 'd' raises the prompt, 'n' cancels it.
	next, _ = m.Update(runeKey('d'))
	m = next.(model)
	if m.profileConfirmDeleteID != created.ID {
		t.Fatalf("'d' should raise a delete confirmation for %q, got %q", created.ID, m.profileConfirmDeleteID)
	}
	// The list view itself renders the confirmation prompt and its own
	// (distinct) footer while a delete is pending.
	if view := m.profilesView(); !strings.Contains(view, "Delete profile") || !strings.Contains(view, "build-host") {
		t.Fatalf("profilesView() should render the pending delete confirmation:\n%s", view)
	}
	next, _ = m.Update(runeKey('n'))
	m = next.(model)
	if m.profileConfirmDeleteID != "" {
		t.Fatal("'n' should cancel the pending delete confirmation")
	}
	if _, ok := m.profileStore.Get(created.ID); !ok {
		t.Fatal("cancelling the confirm must leave the profile in place")
	}

	// 'd' then 'y' actually deletes.
	next, _ = m.Update(runeKey('d'))
	m = next.(model)
	next, _ = m.Update(runeKey('y'))
	m = next.(model)
	if _, ok := m.profileStore.Get(created.ID); ok {
		t.Fatal("'y' should confirm the delete")
	}
	if _, ok := m.memberIndexByProfileID(created.ID); ok {
		t.Fatal("the deleted profile's member should be gone")
	}
	if len(m.profileList()) != 1 {
		t.Fatalf("expected back to 1 profile after delete, got %d", len(m.profileList()))
	}

	// Back to the board.
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = next.(model)
	if m.view != viewBoard {
		t.Fatalf("esc from the profile list should return to the board, got view %v", m.view)
	}
}

// TestManagementScreenDeleteAndToggleViaKeyRefusedWhileJobInFlight extends
// the block-until-idle gate (profilesview_test.go's
// TestProfileDisableRefusedWhileJobInFlight, which drives disableProfile/
// deleteProfile directly) to the actual KEYS a user presses on the
// management list — 'd' and 't' — confirming the gate holds at the real
// dispatch layer, not just in the underlying methods.
func TestManagementScreenDeleteAndToggleViaKeyRefusedWhileJobInFlight(t *testing.T) {
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

	next, _ := m.Update(runeKey('p'))
	m = next.(model)
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // cursor onto the remote profile
	m = next.(model)
	if m.profileCursor != 1 {
		t.Fatalf("cursor should be on the remote profile, got %d", m.profileCursor)
	}

	next, _ = m.Update(runeKey('d'))
	m = next.(model)
	if !strings.Contains(m.profileMsg, "building-vm") {
		t.Fatalf("'d' while a job is in flight should refuse naming it, got %q", m.profileMsg)
	}
	if m.profileConfirmDeleteID != "" {
		t.Fatal("no confirmation should be raised while the delete is refused")
	}

	next, _ = m.Update(runeKey('t'))
	m = next.(model)
	if !strings.Contains(m.profileMsg, "building-vm") {
		t.Fatalf("'t' while a job is in flight should refuse naming it, got %q", m.profileMsg)
	}
	idx, ok := m.memberIndexByProfileID(p.ID)
	if !ok || m.members[idx].state == connDisabled {
		t.Fatal("the profile must still be live — disable via 't' was refused")
	}

	// Once the job clears, the same keys succeed.
	if _, ok := m.jobs.finish(provisionKey(scope, "building-vm"), nil); !ok {
		t.Fatal("finish the seeded job")
	}
	next, _ = m.Update(runeKey('t'))
	m = next.(model)
	if m.profileMsg != "" {
		t.Fatalf("'t' should succeed once the job is no longer in flight, got %q", m.profileMsg)
	}
	idx, ok = m.memberIndexByProfileID(p.ID)
	if !ok || m.members[idx].state != connDisabled {
		t.Fatalf("'t' should now disable the profile, ok=%v state=%v", ok, m.members[idx].state)
	}
}

// TestSubmitProfileEditRefusedWhileJobInFlightOnConnectionChange pins
// submitProfileEdit's own idle-gate branch directly: a CONNECTION-field edit
// (as opposed to a pure rename, which is never gated — see
// TestProfileRenameIsLiveAndNotGated in profilesview_test.go) is refused,
// naming the blocking job, and does not persist.
func TestSubmitProfileEditRefusedWhileJobInFlightOnConnectionChange(t *testing.T) {
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

	edited := p
	edited.Host = "otherhost.example.com" // a connection-field change, not a rename
	cmd := m.submitProfileEdit(edited)
	if cmd != nil {
		t.Fatal("a gated edit must not return a rebuild cmd")
	}
	if m.profileFormErr == nil || !strings.Contains(m.profileFormErr.Error(), "building-vm") {
		t.Fatalf("expected a job-in-flight refusal naming building-vm, got %v", m.profileFormErr)
	}
	got, ok := m.profileStore.Get(p.ID)
	if !ok || got.Host != "example.com" {
		t.Fatalf("the blocked edit must not have persisted, got %+v", got)
	}
}

// TestProxmoxEditFormPrefillToggleSave drives the edit form for a seeded
// Proxmox profile through the REAL key-dispatch path (openProfileEditForm,
// then updateProfileForm exactly as a user's keystrokes travel): every field
// prefills from the stored profile, the insecure checkbox is the one field
// that is NOT a textinput.Model yet still participates in tab-based focus
// traversal and toggles on space, and saving persists the toggled value —
// with token_file carried as a PATH, never the token value itself, exactly
// like identity_path. Creating a Proxmox profile is task 2's type picker;
// this is the edit path task 1 makes verifiable on its own
// (openProfileEditForm already opens the form for an existing profile,
// whatever its type).
func TestProxmoxEditFormPrefillToggleSave(t *testing.T) {
	isolateHostState(t)

	p := seedProxmoxProfile(t, "cluster", "pve.example.com", "pve1", "sandbar")
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

	// Give the stored profile a ca_file value too (seedProxmoxProfile leaves
	// it empty so the buildProfileProvider call just above stays a clean,
	// real construction) — through the model's OWN store instance, since
	// New() loaded an independent Store from the same profiles.yaml
	// seedProxmoxProfile already wrote to.
	withCA := p
	withCA.CAFile = "/etc/sandbar/pve-ca.pem"
	p, err = m.profileStore.Update(withCA)
	if err != nil {
		t.Fatalf("seed a ca_file value: %v", err)
	}

	cmd := m.openProfileEditForm(p)
	if cmd == nil {
		t.Fatal("opening the edit form should focus its first field")
	}
	if m.profileFormType != profiles.TypeProxmox {
		t.Fatalf("profileFormType = %v, want TypeProxmox", m.profileFormType)
	}

	// Every field prefilled from the stored profile.
	checks := []struct {
		name, got, want string
	}{
		{"name", m.profileInputs[ppName].Value(), "cluster"},
		{"host", m.profileInputs[ppHost].Value(), "pve.example.com"},
		{"node", m.profileInputs[ppNode].Value(), "pve1"},
		{"pool", m.profileInputs[ppPool].Value(), "sandbar"},
		{"storage", m.profileInputs[ppStorage].Value(), "local-lvm"},
		{"bridge", m.profileInputs[ppBridge].Value(), "vmbr0"},
		{"token_file", m.profileInputs[ppTokenFile].Value(), p.TokenFile},
		{"ca_file", m.profileInputs[ppCAFile].Value(), "/etc/sandbar/pve-ca.pem"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s prefill = %q, want %q", c.name, c.got, c.want)
		}
	}
	if m.profileInsecure {
		t.Fatal("insecure should prefill false for this profile")
	}

	// On-screen order is name,host,node,pool,storage,bridge,token_file,
	// insecure,ca_file (profileFormSlots) — 7 tabs from Name (index 0)
	// lands on the checkbox (index 7).
	for i := 0; i < 7; i++ {
		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		m = next.(model)
	}
	if !m.profileFormFocusIsCheckbox() {
		t.Fatalf("focus index %d should be the insecure checkbox after 7 tabs", m.profileFormFocus)
	}

	// Space toggles it on.
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m = next.(model)
	if !m.profileInsecure {
		t.Fatal("space should toggle the insecure checkbox on")
	}

	// One more tab must land on CA file's textinput, not stay stuck on the
	// checkbox.
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = next.(model)
	if m.profileFormFocusIsCheckbox() {
		t.Fatal("tab past the checkbox should land on the CA file text field")
	}

	// Save.
	next, cmd = m.Update(ctrlKey('s'))
	m = next.(model)
	if m.profileFormErr != nil {
		t.Fatalf("a valid edit-save should not error, got %v", m.profileFormErr)
	}
	if m.view != viewProfiles {
		t.Fatalf("a valid edit-save should return to the profile list, got view %v", m.view)
	}

	saved, ok := m.profileStore.Get(p.ID)
	if !ok {
		t.Fatal("the edited profile should still be in the store")
	}
	if !saved.Insecure {
		t.Fatal("the toggled insecure value should have been persisted")
	}
	if saved.TokenFile != p.TokenFile {
		t.Fatalf("token_file = %q, want the path unchanged (%q)", saved.TokenFile, p.TokenFile)
	}
	if strings.Contains(saved.TokenFile, "sandbar@pve!prov") {
		t.Fatal("token_file must be a PATH, never the token value itself")
	}
	if saved.Host != "pve.example.com" || saved.Node != "pve1" || saved.Pool != "sandbar" ||
		saved.Storage != "local-lvm" || saved.Bridge != "vmbr0" || saved.CAFile != "/etc/sandbar/pve-ca.pem" {
		t.Fatalf("saved profile = %+v, every other field should round-trip unchanged", saved)
	}
}

// TestProxmoxFormRequiredFieldValidation proves the in-form validation that
// mirrors profiles.validate's Proxmox rule (store.go): clearing host, node,
// pool or token_file and saving sets profileFormErr, stays on the form (does
// NOT return to the list), and never reaches the store.
func TestProxmoxFormRequiredFieldValidation(t *testing.T) {
	isolateHostState(t)

	p := seedProxmoxProfile(t, "cluster", "pve.example.com", "pve1", "sandbar")
	_, scope, err := buildProfileProvider(p)
	if err != nil {
		t.Fatalf("buildProfileProvider: %v", err)
	}
	fleet := provider.Fleet{
		{Profile: profiles.Profile{ID: profiles.LocalProfileID, Type: profiles.TypeLocal, Enabled: true}, Prov: &providerfake.Provider{}, Scope: registry.LocalScope},
		{Profile: p, Prov: &providerfake.Provider{}, Scope: scope},
	}

	for _, missing := range []struct {
		field    string
		inputIdx int
	}{
		{"host", ppHost},
		{"node", ppNode},
		{"pool", ppPool},
		{"token file", ppTokenFile},
	} {
		t.Run(missing.field, func(t *testing.T) {
			m := New(fleet).(model)
			m = resized(m, 100, 30)
			m.openProfileEditForm(p)
			m.profileInputs[missing.inputIdx].SetValue("")

			next, _ := m.submitProfileForm()
			m = next.(model)
			if m.profileFormErr == nil || !strings.Contains(m.profileFormErr.Error(), missing.field) {
				t.Fatalf("clearing %s: error = %v, want it to mention %q", missing.field, m.profileFormErr, missing.field)
			}
			if m.view != viewProfileForm {
				t.Fatalf("an invalid submit must not leave the form, got view %v", m.view)
			}
			if got, _ := m.profileStore.Get(p.ID); got.Host != p.Host || got.Node != p.Node || got.Pool != p.Pool || got.TokenFile != p.TokenFile {
				t.Fatalf("a rejected submit must not have persisted, got %+v", got)
			}
		})
	}
}

// TestProfileRowTextCoversEveryRuntimeState pins profileRowText's full
// contract: kind/target formatting for Local vs RemoteSSH, the persisted
// enabled/disabled status, and every live runtime state a member can be in
// (or none, for a profile never enabled this session).
func TestProfileRowTextCoversEveryRuntimeState(t *testing.T) {
	local := profiles.Profile{Name: "local", Type: profiles.TypeLocal, Enabled: true}
	if got := profileRowText(local, fleetMember{}, false); !strings.Contains(got, "[Local]") || !strings.Contains(got, "enabled") || strings.Contains(got, "@") {
		t.Fatalf("Local row = %q, want [Local], enabled, no target", got)
	}

	remote := profiles.Profile{Name: "build-host", Type: profiles.TypeRemoteSSH, Enabled: false, Host: "example.com", User: "dev", Port: 22}
	if got := profileRowText(remote, fleetMember{}, false); !strings.Contains(got, "[Remote SSH]") || !strings.Contains(got, "dev@example.com:22") || !strings.Contains(got, "disabled") {
		t.Fatalf("disabled remote row (no member) = %q, want [Remote SSH], target, disabled, and no runtime state", got)
	}

	if got := profileRowText(remote, fleetMember{state: connConnecting}, true); !strings.Contains(got, "connecting…") {
		t.Fatalf("connecting row = %q, want it to mention connecting", got)
	}
	if got := profileRowText(remote, fleetMember{state: connConnected}, true); !strings.Contains(got, "connected") {
		t.Fatalf("connected row = %q, want it to mention connected", got)
	}
	if got := profileRowText(remote, fleetMember{state: connErrored, lastErr: errors.New("boom")}, true); !strings.Contains(got, "error: boom") {
		t.Fatalf("errored row = %q, want it to name the error", got)
	}
	if got := profileRowText(remote, fleetMember{state: connErrored}, true); !strings.Contains(got, "error") || strings.Contains(got, "error:") {
		t.Fatalf("errored row with no captured error = %q, want bare \"error\"", got)
	}
	if got := profileRowText(remote, fleetMember{state: connDisabled}, true); !strings.Contains(got, "disabled") {
		t.Fatalf("disabled-member row = %q, want it to mention disabled", got)
	}
}
