package ui

// profilesview.go is Component 4's management half: a profile management
// screen — a `view` (viewProfiles, listing every profile) plus a sub-form
// (viewProfileForm, creating or editing one) — that lets a user manage
// Connection Profiles entirely from the TUI, following the same view-enum +
// sub-model pattern as the existing secrets editor (secrets.go) and create
// form (form.go).
//
// Every mutation is LIVE, with no restart: ENABLE builds that one profile's
// provider/scope binding (buildProfileProvider — the same conversion
// provider.BuildFleet applies per-profile, reimplemented here exactly as
// cmd/sand/resolve.go's providerForProfile already does, since neither
// package can reach the other's unexported constructor without risking an
// import cycle), appends (or revives) its fleetMember in the connecting
// state, and kicks its connect/list cmd — see rebuildMember. DISABLE tears
// the binding down (nils the provider, marks the member connDisabled) but
// KEEPS the member in the fleet, exactly like an error binding, so the header
// (task 10) can still name it; DELETE drops the member outright. Both leave
// the profile's registry/secrets entries dormant on disk — no reconcile, no
// prune (see the plan's Decision Log).
//
// DISABLE, DELETE and a CONNECTION-FIELD edit are gated on the profile being
// IDLE: reusing the job registry's per-scope running check (jobs.go's
// runningInScope), the profile-level generalization of the existing per-VM
// Delete gate (commandreg.go's notBuilding/vmBuilding). A pure RENAME (or any
// metadata-only edit that leaves the target unchanged — see
// connectionFieldsEqual) is NOT gated and rebuilds nothing: the profile's
// immutable id and derived scope are untouched, so its member, tiles, jobs
// and last-used pointer all follow it across the rename.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Profile form field indices — this sub-form's own set, distinct from the
// VM-create form's fXxx constants in form.go. A Local profile's form only
// ever shows pfName (see newProfileInputs); a RemoteSSH profile's shows all
// six.
const (
	pfName = iota
	pfHost
	pfUser
	pfPort
	pfIdentityPath
	pfLimaHome
)

var profileFieldLabels = []string{"Name", "Host", "User", "Port", "Identity path", "Lima home"}

// profileCursorStyle highlights the row under the management screen's ring —
// the same accent (63) the board's focused-tile border and the create form's
// focused label use, so the highlight reads as "focus" consistently across
// screens. Not focusedLabelStyle: that carries a fixed Width(18) meant for a
// column of field labels, which would truncate a full profile row.
var profileCursorStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))

// Local key.Bindings for the management screen's list — screen-local, like
// board.go's boardMove/ghostEnter, rather than fields on the shared keyMap:
// nothing outside this screen dispatches them.
var (
	profileMove   = key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "move"))
	profileEdit   = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "edit"))
	profileToggle = key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "enable/disable"))
	profileDelete = key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete"))
)

// openProfiles opens the profile management screen from the board.
func (m *model) openProfiles() {
	m.profileCursor = 0
	m.profileMsg = ""
	m.profileConfirmDeleteID = ""
	m.view = viewProfiles
}

// profileList returns the store's profiles in stable (insertion) order, or
// nil when there is no store — a hand-built model (tests unrelated to this
// screen) that never wired one up. Nil-safe rather than panicking, mirroring
// the jobs/heartbeats registries' own nil-safe convention.
func (m model) profileList() []profiles.Profile {
	if m.profileStore == nil {
		return nil
	}
	return m.profileStore.List()
}

// currentProfile returns the profile under the management screen's cursor.
func (m model) currentProfile() (profiles.Profile, bool) {
	list := m.profileList()
	if m.profileCursor < 0 || m.profileCursor >= len(list) {
		return profiles.Profile{}, false
	}
	return list[m.profileCursor], true
}

// clampProfileCursor keeps the cursor inside the current profile list —
// called after any mutation that can change the list's length (create,
// delete).
func (m *model) clampProfileCursor() {
	n := len(m.profileList())
	if m.profileCursor >= n {
		m.profileCursor = n - 1
	}
	if m.profileCursor < 0 {
		m.profileCursor = 0
	}
}

// memberIndexByProfileID finds the member for profile id, matched by the
// profile's IMMUTABLE id rather than its scope (fleet.go's memberIndex) — a
// disabled member's scope still matches (disable never changes it), but this
// is also what lets enableProfile/rebuildMember find a DISABLED member to
// revive in place rather than appending a duplicate for the same profile.
func (m model) memberIndexByProfileID(id string) (int, bool) {
	for i := range m.members {
		if m.members[i].profile.ID == id {
			return i, true
		}
	}
	return 0, false
}

// profileBlockingJob names the in-flight job (if any) anywhere under scope —
// the profile-level idle gate, built on jobs.go's runningInScope.
func (m model) profileBlockingJob(scope registry.Scope) (string, bool) {
	key, ok := m.jobs.runningInScope(scope)
	if !ok {
		return "", false
	}
	kind := "build"
	if key.kind == kindTransfer {
		kind = "file transfer"
	}
	return kind + " of " + key.vm, true
}

// profileBlockingJobForID is profileBlockingJob resolved from a profile id: a
// profile with no live member (never enabled this session) has no scope to
// check and so is never blocked.
func (m model) profileBlockingJobForID(id string) (string, bool) {
	i, ok := m.memberIndexByProfileID(id)
	if !ok {
		return "", false
	}
	return m.profileBlockingJob(m.members[i].scope)
}

// buildProfileProvider constructs profile p's provider and registry scope —
// the same conversion provider.BuildFleet applies per-profile
// (internal/provider/fleet.go's unexported buildBinding), reimplemented here
// exactly as cmd/sand/resolve.go's providerForProfile does: neither package
// can call the other's unexported constructor without risking an import
// cycle (see profiles.Profile's remoteTarget doc comment). Construction never
// round-trips the network (NewDefault/NewRemoteLima do not connect), so this
// is always fast and safe to call from the live enable/rebuild path.
func buildProfileProvider(p profiles.Profile) (provider.Provider, registry.Scope, error) {
	if p.Type == profiles.TypeRemoteSSH {
		cfg := provider.TargetConfig{
			Provider:       provider.RemoteLimaProviderID,
			Host:           p.Host,
			User:           p.User,
			Port:           p.Port,
			IdentityPath:   p.IdentityPath,
			RemoteLimaHome: p.LimaHome,
		}
		prov, err := provider.NewRemoteLima(cfg)
		if err != nil {
			return nil, registry.Scope{}, fmt.Errorf("profile %q: %w", p.Name, err)
		}
		return prov, cfg.Scope(), nil
	}
	prov, err := provider.NewDefault()
	if err != nil {
		return nil, registry.Scope{}, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	return prov, registry.LocalScope, nil
}

// rebuildMember builds (or REBUILDS) profile p's live binding and appends —
// or, for a profile that already has a member (a disabled one being
// re-enabled, or an enabled one whose connection fields just changed), revives
// IN PLACE — its fleetMember, then kicks its first connect/list, exactly as
// Init does for the whole startup fleet (just for one member, live). A
// profile whose provider fails to construct becomes an error member rather
// than failing the mutation, mirroring provider.BuildFleet's own per-binding
// error handling.
func (m *model) rebuildMember(p profiles.Profile) tea.Cmd {
	prov, scope, err := buildProfileProvider(p)
	mem := fleetMember{profile: p, scope: scope}
	if err != nil {
		mem.state, mem.lastErr, mem.hostFiles = connErrored, err, lima.LocalFiles()
	} else {
		mem.prov, mem.state, mem.hostFiles = prov, connConnecting, prov.HostFiles()
		if scope.RemoteTarget == "" {
			mem.host.mem, mem.host.diskFree = hostMemBytesFn(), hostDiskFreeFn()
		}
	}

	if i, exists := m.memberIndexByProfileID(p.ID); exists {
		m.members[i] = mem
	} else {
		m.members = append(m.members, mem)
	}
	if mem.prov == nil {
		return nil
	}
	return refreshCmd(scope, mem.prov, mem.hostFiles, true)
}

// enableProfile persists id as enabled and spins its binding up live (see
// rebuildMember).
func (m *model) enableProfile(id string) tea.Cmd {
	if m.profileStore == nil {
		m.profileMsg = "profile store unavailable"
		return nil
	}
	if err := m.profileStore.Enable(id); err != nil {
		m.profileMsg = err.Error()
		return nil
	}
	p, _ := m.profileStore.Get(id)
	m.profileMsg = ""
	cmd := m.rebuildMember(p)
	m.applySize(m.width, m.height)
	return cmd
}

// disableProfile persists id as disabled and tears its live binding down:
// the member ITSELF stays in the fleet (task 10's header/board still name
// it), but its provider is dropped and its state becomes connDisabled — which
// is what stops tickRefresh from re-arming its refresh loop (fleet.go, a nil
// provider is never armed) and boardVMs from rendering its now-stale tiles
// (board.go already skips a connDisabled member). Gated on the profile being
// idle.
func (m *model) disableProfile(id string) {
	if m.profileStore == nil {
		m.profileMsg = "profile store unavailable"
		return
	}
	p, ok := m.profileStore.Get(id)
	if !ok {
		m.profileMsg = fmt.Sprintf("no profile with id %q", id)
		return
	}
	if job, blocked := m.profileBlockingJobForID(id); blocked {
		m.profileMsg = fmt.Sprintf("cannot disable %q: %s is in flight — finish or cancel it first", p.Name, job)
		return
	}
	if err := m.profileStore.Disable(id); err != nil {
		m.profileMsg = err.Error()
		return
	}
	m.profileMsg = ""
	if i, exists := m.memberIndexByProfileID(id); exists {
		m.members[i].prov = nil
		m.members[i].state = connDisabled
		m.members[i].lastErr = nil
	}
	m.applySize(m.width, m.height)
}

// deleteProfile removes id from the store and drops its member ENTIRELY —
// unlike disable, there is no "stays dormant in the fleet" state for a
// profile that no longer exists. It does NOT touch the remote server, its
// VMs, or the registry/secrets entries under its scope: they stay dormant on
// disk (no reconcile, no prune) and reappear if the profile is re-added. Any
// open heartbeat under the profile's scope is stopped explicitly BEFORE the
// member is removed — syncHeartbeats (heartbeat.go) only reconciles scopes
// still present in m.members, so a member removed out from under it would
// otherwise leak an open guest shell forever. Gated on the profile being
// idle, exactly like disable.
func (m *model) deleteProfile(id string) {
	if m.profileStore == nil {
		m.profileMsg = "profile store unavailable"
		return
	}
	p, ok := m.profileStore.Get(id)
	if !ok {
		m.profileMsg = fmt.Sprintf("no profile with id %q", id)
		return
	}
	if job, blocked := m.profileBlockingJobForID(id); blocked {
		m.profileMsg = fmt.Sprintf("cannot delete %q: %s is in flight — finish or cancel it first", p.Name, job)
		return
	}
	if err := m.profileStore.Remove(id); err != nil {
		m.profileMsg = err.Error()
		return
	}
	m.profileMsg = "deleted " + p.Name
	if i, exists := m.memberIndexByProfileID(id); exists {
		scope := m.members[i].scope
		for _, name := range m.heartbeats.names(scope) {
			m.heartbeats.stop(scope, name)
		}
		m.members = append(m.members[:i], m.members[i+1:]...)
		if m.active >= len(m.members) {
			m.active = 0
		}
	}
	m.applySize(m.width, m.height)
}

// connectionFieldsEqual reports whether a and b would build the SAME
// provider binding — same target, same identity, same remote LIMA_HOME. It
// is what tells a pure rename (or any other metadata-only edit) apart from a
// connection-field edit: only the latter needs a tear-down-and-rebuild and
// the idle gate. Always true for two Local profiles (both sides zero), which
// is exactly right — Local has no connection fields, so any edit to it is a
// rename.
func connectionFieldsEqual(a, b profiles.Profile) bool {
	return a.Host == b.Host && a.User == b.User && a.Port == b.Port &&
		a.IdentityPath == b.IdentityPath && a.LimaHome == b.LimaHome
}

// submitProfileEdit persists an edited profile (from the create/edit form)
// and, only when its CONNECTION fields changed (not a pure rename), tears
// down and rebuilds its live binding — gated on the profile being idle
// exactly like disable/delete, since a rebuild replaces the provider a job
// might be mid-flight against. A pure rename (or an edit to a currently
// DISABLED profile, which has no live binding to rebuild) skips both the
// gate and the rebuild: it just persists and, if a stale member entry exists
// (a disabled profile still carries one), refreshes its label so the header
// shows the new name immediately.
func (m *model) submitProfileEdit(p profiles.Profile) tea.Cmd {
	if m.profileStore == nil {
		m.profileFormErr = fmt.Errorf("profile store unavailable")
		return nil
	}
	old, ok := m.profileStore.Get(p.ID)
	if !ok {
		m.profileFormErr = fmt.Errorf("no profile with id %q", p.ID)
		return nil
	}
	rename := connectionFieldsEqual(old, p)
	if !rename {
		if job, blocked := m.profileBlockingJobForID(p.ID); blocked {
			m.profileFormErr = fmt.Errorf("cannot change connection settings for %q: %s is in flight — finish or cancel it first", old.Name, job)
			return nil
		}
	}

	updated, err := m.profileStore.Update(p)
	if err != nil {
		m.profileFormErr = err
		return nil
	}
	m.profileFormErr = nil

	if rename || !updated.Enabled {
		if i, exists := m.memberIndexByProfileID(updated.ID); exists {
			m.members[i].profile = updated
		}
		return nil
	}
	// Connection fields changed on an ENABLED profile: tear down and rebuild
	// live, exactly like a fresh enable.
	cmd := m.rebuildMember(updated)
	m.applySize(m.width, m.height)
	return cmd
}

// newProfileInputs builds the form's text inputs: just Name for a Local
// profile (it has no connection fields and its Type is immutable), or the
// full six for a RemoteSSH profile.
func newProfileInputs(t profiles.Type) []textinput.Model {
	n := 1
	if t == profiles.TypeRemoteSSH {
		n = len(profileFieldLabels)
	}
	inputs := make([]textinput.Model, n)
	for i := range inputs {
		ti := textinput.New()
		ti.CharLimit = 256
		ti.SetWidth(44)
		inputs[i] = ti
	}
	return inputs
}

// openProfileCreateForm opens a blank RemoteSSH profile form. Create only
// ever offers RemoteSSH: Local is permanent and pre-seeded, never created
// from this screen — see openProfileEditForm for its rename-only form.
func (m *model) openProfileCreateForm() tea.Cmd {
	m.profileFormID = ""
	m.profileFormType = profiles.TypeRemoteSSH
	m.profileInputs = newProfileInputs(profiles.TypeRemoteSSH)
	m.profileFormFocus = 0
	m.profileFormErr = nil
	m.view = viewProfileForm
	return m.profileInputs[0].Focus()
}

// openProfileEditForm opens the form pre-filled from an existing profile.
func (m *model) openProfileEditForm(p profiles.Profile) tea.Cmd {
	m.profileFormID = p.ID
	m.profileFormType = p.Type
	m.profileInputs = newProfileInputs(p.Type)
	m.profileInputs[pfName].SetValue(p.Name)
	if p.Type == profiles.TypeRemoteSSH {
		m.profileInputs[pfHost].SetValue(p.Host)
		m.profileInputs[pfUser].SetValue(p.User)
		if p.Port != 0 {
			m.profileInputs[pfPort].SetValue(strconv.Itoa(p.Port))
		}
		m.profileInputs[pfIdentityPath].SetValue(p.IdentityPath)
		m.profileInputs[pfLimaHome].SetValue(p.LimaHome)
	}
	m.profileFormFocus = 0
	m.profileFormErr = nil
	m.view = viewProfileForm
	return m.profileInputs[0].Focus()
}

// profileFormFocusNext/Prev walk the form's fields, wrapping around. A
// single-field (Local, rename-only) form has nothing to walk between.
func (m *model) profileFormFocusNext() tea.Cmd {
	n := len(m.profileInputs)
	if n <= 1 {
		return nil
	}
	m.profileInputs[m.profileFormFocus].Blur()
	m.profileFormFocus = (m.profileFormFocus + 1) % n
	return m.profileInputs[m.profileFormFocus].Focus()
}

func (m *model) profileFormFocusPrev() tea.Cmd {
	n := len(m.profileInputs)
	if n <= 1 {
		return nil
	}
	m.profileInputs[m.profileFormFocus].Blur()
	m.profileFormFocus = (m.profileFormFocus - 1 + n) % n
	return m.profileInputs[m.profileFormFocus].Focus()
}

// submitProfileForm validates the form's fields and either creates a new,
// immediately-enabled RemoteSSH profile (profileFormID == "") or persists an
// edit to an existing one, reusing the store's own target-uniqueness/
// single-Local validation (Add/Update — see profiles/store.go's validate)
// rather than re-implementing it here.
func (m model) submitProfileForm() (tea.Model, tea.Cmd) {
	if m.profileStore == nil {
		m.profileFormErr = fmt.Errorf("profile store unavailable")
		return m, nil
	}
	name := strings.TrimSpace(m.profileInputs[pfName].Value())
	if name == "" {
		m.profileFormErr = fmt.Errorf("name is required")
		return m, nil
	}

	p := profiles.Profile{ID: m.profileFormID, Name: name, Type: m.profileFormType, Enabled: true}
	if m.profileFormType == profiles.TypeRemoteSSH {
		p.Host = strings.TrimSpace(m.profileInputs[pfHost].Value())
		p.User = strings.TrimSpace(m.profileInputs[pfUser].Value())
		if p.Host == "" || p.User == "" {
			m.profileFormErr = fmt.Errorf("host and user are required")
			return m, nil
		}
		port := 22
		if portStr := strings.TrimSpace(m.profileInputs[pfPort].Value()); portStr != "" {
			n, err := strconv.Atoi(portStr)
			if err != nil || n <= 0 {
				m.profileFormErr = fmt.Errorf("port must be a positive number")
				return m, nil
			}
			port = n
		}
		p.Port = port
		p.IdentityPath = strings.TrimSpace(m.profileInputs[pfIdentityPath].Value())
		p.LimaHome = strings.TrimSpace(m.profileInputs[pfLimaHome].Value())
	}

	if m.profileFormID == "" {
		added, err := m.profileStore.Add(p)
		if err != nil {
			m.profileFormErr = err
			return m, nil
		}
		m.profileFormErr = nil
		cmd := m.rebuildMember(added)
		m.applySize(m.width, m.height)
		m.view = viewProfiles
		m.clampProfileCursor()
		return m, cmd
	}

	// EDIT: the form has no enable/disable toggle of its own (that is the
	// list view's 't'), so preserve whatever the profile's Enabled flag
	// already is.
	existing, ok := m.profileStore.Get(m.profileFormID)
	if !ok {
		m.profileFormErr = fmt.Errorf("no profile with id %q", m.profileFormID)
		return m, nil
	}
	p.Enabled = existing.Enabled
	cmd := m.submitProfileEdit(p)
	if m.profileFormErr == nil {
		m.view = viewProfiles
		m.clampProfileCursor()
	}
	return m, cmd
}

// updateProfileForm handles keys on the create/edit sub-form.
func (m model) updateProfileForm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Code == tea.KeyEsc:
		m.view = viewProfiles
		m.profileFormErr = nil
		return m, nil
	case key.Matches(msg, m.keys.Save):
		return m.submitProfileForm()
	case key.Matches(msg, m.keys.ShiftTab), key.Matches(msg, m.keys.Up):
		return m, m.profileFormFocusPrev()
	case key.Matches(msg, m.keys.Down), key.Matches(msg, m.keys.Tab):
		return m, m.profileFormFocusNext()
	}
	cmds := make([]tea.Cmd, len(m.profileInputs))
	for i := range m.profileInputs {
		m.profileInputs[i], cmds[i] = m.profileInputs[i].Update(msg)
	}
	return m, tea.Batch(cmds...)
}

// profileFormHelp is the create/edit form's footer.
func (m model) profileFormHelp() []key.Binding {
	return []key.Binding{m.keys.Up, m.keys.Down, m.keys.Save, m.keys.Back}
}

// profileFormView renders the create/edit sub-form.
func (m model) profileFormView() string {
	cw := m.layout.ContentWidth
	var b strings.Builder
	title := "New Connection Profile"
	if m.profileFormID != "" {
		title = "Edit Connection Profile"
	}
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n\n")

	labels := profileFieldLabels[:len(m.profileInputs)]
	for i, ti := range m.profileInputs {
		ls := labelStyle
		if i == m.profileFormFocus {
			ls = focusedLabelStyle
		}
		b.WriteString(ls.Render(labels[i]+":") + " " + ti.View() + "\n")
	}

	if m.profileFormErr != nil {
		b.WriteString("\n" + errStyle.Width(cw).Render("Error: "+m.profileFormErr.Error()) + "\n")
	}

	b.WriteString("\n" + m.footerView(m.profileFormHelp()))
	return appStyle.Render(b.String())
}

// updateProfiles handles keys on the profile management list.
func (m model) updateProfiles(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.profileConfirmDeleteID != "" {
		switch {
		case key.Matches(msg, m.keys.Confirm): // y
			id := m.profileConfirmDeleteID
			m.profileConfirmDeleteID = ""
			m.deleteProfile(id)
			m.clampProfileCursor()
			return m, nil
		case key.Matches(msg, m.keys.Cancel): // n / esc
			m.profileConfirmDeleteID = ""
			return m, nil
		}
		return m, nil
	}

	switch msg.Code {
	case tea.KeyUp:
		if m.profileCursor > 0 {
			m.profileCursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.profileCursor < len(m.profileList())-1 {
			m.profileCursor++
		}
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.Back):
		m.view = viewBoard
		return m, nil

	case key.Matches(msg, m.keys.New):
		return m, m.openProfileCreateForm()

	case key.Matches(msg, profileEdit):
		p, ok := m.currentProfile()
		if !ok {
			return m, nil
		}
		return m, m.openProfileEditForm(p)

	case key.Matches(msg, profileToggle):
		p, ok := m.currentProfile()
		if !ok {
			return m, nil
		}
		if p.Enabled {
			m.disableProfile(p.ID)
			return m, nil
		}
		return m, m.enableProfile(p.ID)

	case key.Matches(msg, profileDelete):
		p, ok := m.currentProfile()
		if !ok {
			return m, nil
		}
		if p.ID == profiles.LocalProfileID {
			m.profileMsg = "the local profile is permanent and cannot be deleted"
			return m, nil
		}
		if job, blocked := m.profileBlockingJobForID(p.ID); blocked {
			m.profileMsg = fmt.Sprintf("cannot delete %q: %s is in flight — finish or cancel it first", p.Name, job)
			return m, nil
		}
		m.profileMsg = ""
		m.profileConfirmDeleteID = p.ID
		return m, nil
	}
	return m, nil
}

// profilesHelp is the management screen's footer.
func (m model) profilesHelp() []key.Binding {
	if m.profileConfirmDeleteID != "" {
		return []key.Binding{m.keys.Confirm, m.keys.Cancel}
	}
	return []key.Binding{profileMove, m.keys.New, profileEdit, profileToggle, profileDelete, m.keys.Back}
}

// profileRowText formats one profile's list row: its name, type, (for
// RemoteSSH) its target, enabled/disabled, and — when it has a live member —
// its runtime connection state.
func profileRowText(p profiles.Profile, mem fleetMember, hasMember bool) string {
	kind := "Local"
	target := ""
	if p.Type == profiles.TypeRemoteSSH {
		kind = "Remote SSH"
		target = fmt.Sprintf("%s@%s:%d", p.User, p.Host, p.Port)
	}
	status := "disabled"
	if p.Enabled {
		status = "enabled"
	}

	parts := []string{p.Name, "[" + kind + "]"}
	if target != "" {
		parts = append(parts, target)
	}
	parts = append(parts, status)

	if hasMember {
		switch mem.state {
		case connConnecting:
			parts = append(parts, "connecting…")
		case connConnected:
			parts = append(parts, "connected")
		case connErrored:
			live := "error"
			if mem.lastErr != nil {
				live += ": " + mem.lastErr.Error()
			}
			parts = append(parts, live)
		case connDisabled:
			parts = append(parts, "disabled")
		}
	}
	return strings.Join(parts, "  ")
}

// profilesView renders the profile management list.
func (m model) profilesView() string {
	cw := m.layout.ContentWidth
	var b strings.Builder
	b.WriteString(titleStyle.Render("Connection Profiles"))
	b.WriteString("\n\n")

	list := m.profileList()
	if len(list) == 0 {
		b.WriteString(statusStyle.Render("No profiles configured.") + "\n")
	}
	for i, p := range list {
		cursor := "  "
		if i == m.profileCursor {
			cursor = "> "
		}
		mem, hasMember := m.memberIndexByProfileIDValue(p.ID)
		line := cursor + profileRowText(p, mem, hasMember)
		if i == m.profileCursor {
			b.WriteString(m.clipLine(profileCursorStyle.Render(line)) + "\n")
		} else {
			b.WriteString(m.clipLine(statusStyle.Render(line)) + "\n")
		}
	}

	switch {
	case m.profileConfirmDeleteID != "":
		if p, ok := m.profileStore.Get(m.profileConfirmDeleteID); ok {
			b.WriteString("\n" + errStyle.Width(cw).Render(fmt.Sprintf("Delete profile %q?  [y] yes   [n] cancel", p.Name)) + "\n")
		}
	case m.profileMsg != "":
		b.WriteString("\n" + warnStyle.Width(cw).Render(m.profileMsg) + "\n")
	}

	b.WriteString("\n" + m.footerView(m.profilesHelp()))
	return appStyle.Render(b.String())
}

// memberIndexByProfileIDValue is memberIndexByProfileID's value-copy form, for
// rendering (profilesView never needs to mutate the member it looks up).
func (m model) memberIndexByProfileIDValue(id string) (fleetMember, bool) {
	i, ok := m.memberIndexByProfileID(id)
	if !ok {
		return fleetMember{}, false
	}
	return m.members[i], true
}
