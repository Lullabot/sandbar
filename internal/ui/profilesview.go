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
// can still name it; DELETE drops the member outright. Both leave the
// profile's registry/secrets entries dormant on disk — no reconcile, no
// prune — so they reappear intact if the profile is re-enabled or re-added.
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

// profileFieldInfo is the FOCUSED row's help, parallel to profileFieldLabels
// — the profile form's counterpart to the VM create form's fieldInfo
// (form.go), rendered the same way and in the same voice: "Required."/
// "Optional." first, and "Blank → <default>" for anything with a fallback,
// so a blank field's meaning is legible without leaving the form.
//
// Local's rename-only form shows just the first entry (see profileFormSlots),
// which is why that one must read true for a Local profile too.
var profileFieldInfo = []string{
	"Required. Display label for this profile. Rename it freely — VMs, jobs and the last-used pointer follow it by ID, not by name.",
	"Required. Hostname or IP sand reaches this Lima host at over SSH.",
	"Required. SSH login on that host. With host and port this forms the profile's registry scope, so editing it re-keys which VMs the profile owns.",
	"SSH port. Blank → 22.",
	"Optional. Path to the SSH private key. Blank → whatever your ssh agent and ~/.ssh/config already resolve for this host.",
	"Optional. LIMA_HOME on the remote host, if Lima's instances do not live in that user's default ~/.lima.",
}

// Proxmox profile form field indices — this type's own set, distinct from
// pfXxx above (a Local/RemoteSSH form never shows these, and vice versa: see
// newProfileInputs). These are STORAGE positions into m.profileInputs only —
// NOT the form's on-screen/focus order, which profileFormSlots derives
// separately (the insecure checkbox sits between token_file and ca_file on
// screen but has no textinput.Model of its own, so it needs no slot here).
const (
	ppName = iota
	ppHost
	ppNode
	ppPool
	ppStorage
	ppBridge
	ppTokenFile
	ppCAFile
	// ppIdentityPath is REQUIRED for Proxmox, unlike its RemoteSSH namesake
	// (which may fall back to the ssh agent): sand installs
	// <identity_path>.pub into every guest via cloud-init and then connects
	// with the private key. Without a field for it here, a profile created on
	// this screen could never launch a VM — it failed at Preflight with an
	// error the form gave no way to fix.
	ppIdentityPath
	// ppUser, ppImageStorage and ppBaseImage are the OPTIONAL Proxmox fields:
	// each has a provider-side default (the local username, "local", and the
	// project's golden image respectively — see NewProxmox), so an empty input
	// is a real answer here, not a missing one, and none of the three joins
	// submitProfileForm's required-field table.
	//
	// Optional never meant invisible, though. A node whose file-based storage
	// is not named "local" needs image_storage before it can build a single
	// VM, and until these had inputs the only way to set one — or even to SEE
	// what it was set to — was to leave the TUI and hand-edit profiles.yaml.
	// Adding them is also what retires the edit path's carry-across of exactly
	// these three fields (see submitProfileForm).
	ppUser
	ppImageStorage
	ppBaseImage
)

var proxmoxFieldLabels = []string{
	"Name", "Host", "Node", "Pool", "Storage", "Bridge", "Token file", "CA file",
	"Identity path", "User", "Image storage", "Base image",
}

// proxmoxFieldInfo is the focused row's help, parallel to proxmoxFieldLabels
// and keyed by the same ppXxx storage index — see profileFieldInfo above for
// the shared voice, and profileFormInfo for where it is rendered.
//
// This supersedes the per-field Placeholder hints these three optional fields
// briefly carried. A placeholder can only say "defaults to local"; the space
// here says WHY (block storages reject content=import) and, for User, that
// changing it leaves existing VMs behind — neither of which fits in a 44-cell
// input. Two channels saying the same thing in a 13-row form is noise, and
// this is the one the VM create form already established.
var proxmoxFieldInfo = []string{
	profileFieldInfo[pfName],
	"Required. Hostname or IP the Proxmox API answers on. A bare host uses port 8006; append :port only if you changed it.",
	"Required. The PVE node name — the identifier in /nodes/<node>/… API paths, which is not always the same string as the host above.",
	"Required. The resource pool sand's VMs are created in, and the pool the API token is scoped to. Host, node and pool together form this profile's registry scope.",
	"Required. Storage backing VM disks — it must accept content type \"images\" (local-lvm, a ZFS pool, …).",
	"Required. The Linux bridge each VM's NIC attaches to, usually vmbr0. Leaving it blank would not mean \"no network\" — it gives QEMU user-mode NAT, and the guest is then unreachable over SSH.",
	"Required. PATH to a file holding user@realm!tokenid=value — never the token itself. It must not be readable by group or other.",
	"Optional. PEM CA bundle to verify the Proxmox API certificate against — the safe alternative to Insecure for a self-signed cert.",
	"Required. Path to your SSH private key. sand installs <path>.pub into every guest via cloud-init, then connects with the key.",
	"Optional. The guest login cloud-init creates in each VM, and that sand SSHes in as. Blank → your local username. Existing VMs keep the account they were built with.",
	"Optional. FILE-BASED storage (dir/NFS/CIFS) the cloud image downloads to with content=import, which block storages like zfspool and lvm-thin reject. Blank → local.",
	"Optional. URL of the cloud image the base template is built from. Blank → the project golden image (Debian with qemu-guest-agent, checksum-verified).",
}

// proxmoxInsecureInfo is the checkbox row's help. It is a literal rather than
// a proxmoxFieldInfo entry because that slice is indexed by ppXxx storage
// position and the checkbox has none — it is not a textinput.Model at all.
const proxmoxInsecureInfo = "Skip TLS verification of the Proxmox API certificate. Prefer the CA file field for a self-signed cert: this accepts ANY certificate, so anything on the path can read the API token. Space toggles."

// profileFormFieldKind distinguishes a text row (backed by a
// textinput.Model) from the one boolean row the form can show (Proxmox's
// insecure checkbox) — the ONLY place any focus/key/view code needs to
// reason about which is which.
type profileFormFieldKind int

const (
	pffText profileFormFieldKind = iota
	pffCheckbox
)

// profileFormField describes one row of the create/edit form in on-screen —
// and focus-traversal — order. inputIdx is meaningful only when kind ==
// pffText; the checkbox row carries no textinput.Model at all (its value
// lives in model.profileInsecure), which is exactly why introducing it as a
// new field KIND here, rather than overloading it onto profileInputs, keeps
// the (byte-for-byte unchanged) text-input path untouched: profileInputs
// stays a plain []textinput.Model for every type, including Proxmox's other
// twelve fields.
type profileFormField struct {
	label    string
	kind     profileFormFieldKind
	inputIdx int
	// info is the help shown under the form while THIS row has focus (see
	// profileFormInfo). It lives on the row rather than in one table indexed by
	// focus position — as the VM create form's fieldInfo is — because this
	// form has three different layouts and reorders rows away from storage
	// order, so a focus-indexed table would have to be re-derived per type and
	// would silently mismatch the moment a row moved.
	info string
}

// textOnlySlots builds an all-text profileFormField list whose inputIdx is
// simply its position — true of every Local/RemoteSSH field today (pfName..
// pfLimaHome are declared in that same order) and is what keeps
// profileFormSlots's Local/RemoteSSH branches bit-identical to the form's
// pre-Proxmox behaviour. labels and infos are indexed together, so a caller
// slicing one (Local takes just [:1]) must slice the other identically.
func textOnlySlots(labels, infos []string) []profileFormField {
	slots := make([]profileFormField, len(labels))
	for i, l := range labels {
		slots[i] = profileFormField{label: l, kind: pffText, inputIdx: i, info: infos[i]}
	}
	return slots
}

// proxmoxSlot builds the text row for one ppXxx storage index, taking its
// label, its help AND its inputIdx from that single index — so a row cannot
// be given one field's label beside another field's help or input, which a
// hand-written literal per row invites every time the on-screen order is
// rearranged (and it has been, twice).
func proxmoxSlot(idx int) profileFormField {
	return profileFormField{
		label:    proxmoxFieldLabels[idx],
		kind:     pffText,
		inputIdx: idx,
		info:     proxmoxFieldInfo[idx],
	}
}

// profileFormSlots returns the form's fields for its CURRENT type, in the
// exact order focus traversal, the key loop and the view all walk — the
// single place that answers "which row is the checkbox" so
// profileFormFocusNext/Prev, updateProfileForm and profileFormView never
// have to special-case a raw index themselves.
func (m model) profileFormSlots() []profileFormField {
	switch m.profileFormType {
	case profiles.TypeProxmox:
		// On-screen order groups the form by what the reader is answering, not
		// by ppXxx storage order: the two storages adjacent (they are a pair —
		// disk images and the file-based staging area the cloud image is
		// downloaded to) with the base image that lands in the second right
		// after them; then the network; then the GUEST login pair, user and the
		// identity whose .pub is installed into that account; and only then the
		// API/TLS credentials (token file, insecure, CA file), which authenticate
		// to Proxmox rather than to any VM.
		return []profileFormField{
			proxmoxSlot(ppName),
			proxmoxSlot(ppHost),
			proxmoxSlot(ppNode),
			proxmoxSlot(ppPool),
			proxmoxSlot(ppStorage),
			proxmoxSlot(ppImageStorage),
			proxmoxSlot(ppBaseImage),
			proxmoxSlot(ppBridge),
			proxmoxSlot(ppUser),
			proxmoxSlot(ppIdentityPath),
			proxmoxSlot(ppTokenFile),
			{label: "Insecure", kind: pffCheckbox, info: proxmoxInsecureInfo},
			proxmoxSlot(ppCAFile),
		}
	case profiles.TypeRemoteSSH:
		return textOnlySlots(profileFieldLabels, profileFieldInfo)
	default: // TypeLocal
		return textOnlySlots(profileFieldLabels[:1], profileFieldInfo[:1])
	}
}

// profileFormFocusIsCheckbox reports whether the form's CURRENT focus is on
// the (Proxmox-only) insecure checkbox rather than a textinput.Model — the
// guard updateProfileForm needs so space/enter toggle it instead of either
// submitting or advancing focus, and so no key loop reaches a text input
// that has nothing focused right now.
func (m model) profileFormFocusIsCheckbox() bool {
	slots := m.profileFormSlots()
	return m.profileFormFocus >= 0 && m.profileFormFocus < len(slots) &&
		slots[m.profileFormFocus].kind == pffCheckbox
}

// isRemoteProfile reports whether p is reached over the NETWORK rather than
// being the machine sand runs on — true for RemoteSSH and for Proxmox, which
// is equally remote, just not over ssh. The one place that answers this, so
// the connection lifecycle speaks with one voice: the sites that announce
// "connecting to", "connected to", "reconnecting to" and "reconnected to"
// have to agree about which members are remote, and when they did not, a
// Proxmox member announced a connection attempt that then never resolved.
// Local is deliberately excluded everywhere — see New's loop for why.
func isRemoteProfile(p profiles.Profile) bool {
	return p.Type == profiles.TypeRemoteSSH || p.Type == profiles.TypeProxmox
}

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
	switch key.kind {
	case kindTransfer:
		kind = "file transfer"
	case kindLand:
		kind = "landing action"
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
// (internal/provider/fleet.go's buildBinding). The FIELD MAPPING is not
// reimplemented here: provider.TargetConfigFor is the one exported source of
// truth for it, because the hand-copy that used to live here silently dropped
// IdentityPath (and ImageStorage/BaseImage), so a Proxmox profile enabled
// from this screen built a provider with no SSH key and failed at Preflight
// while the identical profile worked from `sand --profile`. Only the
// constructor choice and this package's own error wrapping stay local.
// Construction never round-trips the network (NewDefault/NewRemoteLima/
// NewProxmox do not connect), so this is always fast and safe to call from
// the live enable/rebuild path.
func buildProfileProvider(p profiles.Profile) (provider.Provider, registry.Scope, error) {
	var (
		cfg  provider.TargetConfig
		prov provider.Provider
		err  error
	)
	switch p.Type {
	case profiles.TypeRemoteSSH:
		cfg = provider.TargetConfigFor(p)
		prov, err = provider.NewRemoteLima(cfg)
	case profiles.TypeProxmox:
		cfg = provider.TargetConfigFor(p)
		prov, err = provider.NewProxmox(cfg)
	default: // TypeLocal
		prov, err = provider.NewDefault()
		if err != nil {
			return nil, registry.Scope{}, fmt.Errorf("profile %q: %w", p.Name, err)
		}
		return prov, registry.LocalScope, nil
	}
	if err != nil {
		return nil, registry.Scope{}, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	return prov, cfg.Scope(), nil
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
		// Live enable/edit of a remote starts a fresh connection attempt —
		// same announcement New makes for the startup fleet (local stays
		// silent there and here alike).
		if isRemoteProfile(p) {
			m.logMsg("connecting to " + p.Name + "…")
		}
	}

	if i, exists := m.memberIndexByProfileID(p.ID); exists {
		m.members[i] = mem
	} else {
		m.members = append(m.members, mem)
	}
	// The heartbeat resolver's snapshot must reflect the fleet as it stands
	// NOW: a profile enabled (or connection-edited) after New() ran would
	// otherwise never resolve to a shell, and its VMs would carry em-dash
	// gauges forever (see heartbeatRegistry.setShell).
	m.heartbeats.setShell(fleetShellResolver(m.members))
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
// the member ITSELF stays in the fleet (the header/board still name it), but
// its provider is dropped and its state becomes connDisabled — which
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
		// A deliberate disable logs for EVERY profile type (unlike the
		// connect/reconnect chatter, which is remote-only — see New): the user
		// asked for this disconnect, so the session log should say it happened.
		m.logMsg("disconnected from " + p.Name + " (disabled)")
	}
	// The disabled member's provider is now nil; the resolver must reflect
	// that too, exactly like every other fleet mutation (see setShell).
	m.heartbeats.setShell(fleetShellResolver(m.members))
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
	// The removed member must not linger in the resolver's snapshot either
	// (see setShell) — though it is moot in practice here, since the member
	// (and every heartbeat under its scope) is already gone by this point.
	m.heartbeats.setShell(fleetShellResolver(m.members))
	m.applySize(m.width, m.height)
}

// connectionFieldsEqual reports whether a and b would build the SAME
// provider binding — same target, same identity, same remote LIMA_HOME (or,
// for Proxmox, the same node/pool/storage/image storage/base image/bridge/
// token file/insecure flag/CA file). It is what tells a pure rename (or any
// other metadata-only edit) apart from a connection-field edit: only the
// latter needs a tear-down-and-rebuild and the idle gate. Always true for two
// Local profiles (both sides zero), which is exactly right — Local has no
// connection fields, so any edit to it is a rename.
//
// The Proxmox fields used to be missing here entirely — a real bug: editing
// a Proxmox profile's node or pool was silently read as a pure rename and
// never rebuilt the live binding against the new target. Every field
// provider.TargetConfigFor reads has to be compared here, or that bug simply
// comes back for whichever one is left out.
func connectionFieldsEqual(a, b profiles.Profile) bool {
	return a.Host == b.Host && a.User == b.User && a.Port == b.Port &&
		a.IdentityPath == b.IdentityPath && a.LimaHome == b.LimaHome &&
		a.Node == b.Node && a.Pool == b.Pool && a.Storage == b.Storage &&
		a.ImageStorage == b.ImageStorage && a.BaseImage == b.BaseImage &&
		a.Bridge == b.Bridge && a.TokenFile == b.TokenFile &&
		a.Insecure == b.Insecure && a.CAFile == b.CAFile
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

// newProfileInputs builds the form's TEXT inputs: just Name for a Local
// profile (it has no connection fields and its Type is immutable), the full
// six for a RemoteSSH profile, or the twelve text fields for a Proxmox
// profile (its thirteenth row, the insecure checkbox, is not a
// textinput.Model at all — see model.profileInsecure and profileFormSlots).
func newProfileInputs(t profiles.Type) []textinput.Model {
	n := 1
	switch t {
	case profiles.TypeRemoteSSH:
		n = len(profileFieldLabels)
	case profiles.TypeProxmox:
		n = len(proxmoxFieldLabels)
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

// creatableProfileTypes are the types `n` can create, in menu order. Local is
// omitted: it is permanent and pre-seeded (there is exactly one, created on
// first run), so it is editable but never creatable — a future type is one
// line to add here.
var creatableProfileTypes = []profiles.Type{profiles.TypeRemoteSSH, profiles.TypeProxmox}

// profileTypeLabel is the picker's (and any future menu's) human label for a
// creatable profile type. A small function of its own, rather than reusing
// profileRowText's inline switch, since the picker has only a bare Type to
// name — not a whole Profile value.
func profileTypeLabel(t profiles.Type) string {
	switch t {
	case profiles.TypeProxmox:
		return "Proxmox"
	default: // TypeRemoteSSH
		return "Remote SSH"
	}
}

// openProfileCreateForm opens the pre-form type picker (viewProfileTypePicker)
// rather than jumping straight into a field form: now that a second creatable
// type (Proxmox) exists alongside RemoteSSH, `n` has to ask WHICH one before
// any inputs are built. See creatableProfileTypes for the offered types and
// openProfileFormForType for what a picker selection opens into. Local is
// never offered: it is permanent and pre-seeded, never created from this
// screen — see openProfileEditForm for its rename-only edit form.
func (m *model) openProfileCreateForm() {
	m.profileTypeCursor = 0
	m.profileMsg = ""
	m.view = viewProfileTypePicker
}

// openProfileFormForType opens a blank create form for profile type t — what
// openProfileCreateForm used to do unconditionally for RemoteSSH before the
// type picker existed, now parameterized so the picker can reach it for any
// creatable type.
func (m *model) openProfileFormForType(t profiles.Type) tea.Cmd {
	m.profileFormID = ""
	m.profileFormType = t
	m.profileInputs = newProfileInputs(t)
	m.profileFormFocus = 0
	m.profileFormErr = nil
	m.profileInsecure = false
	m.view = viewProfileForm
	return m.profileInputs[0].Focus()
}

// openProfileEditForm opens the form pre-filled from an existing profile.
func (m *model) openProfileEditForm(p profiles.Profile) tea.Cmd {
	m.profileFormID = p.ID
	m.profileFormType = p.Type
	m.profileInputs = newProfileInputs(p.Type)
	m.profileInsecure = p.Insecure // meaningless (false) for a non-Proxmox profile
	m.profileInputs[pfName].SetValue(p.Name)
	switch p.Type {
	case profiles.TypeRemoteSSH:
		m.profileInputs[pfHost].SetValue(p.Host)
		m.profileInputs[pfUser].SetValue(p.User)
		if p.Port != 0 {
			m.profileInputs[pfPort].SetValue(strconv.Itoa(p.Port))
		}
		m.profileInputs[pfIdentityPath].SetValue(p.IdentityPath)
		m.profileInputs[pfLimaHome].SetValue(p.LimaHome)
	case profiles.TypeProxmox:
		// pfName == ppName == 0, so Name is already set above.
		m.profileInputs[ppHost].SetValue(p.Host)
		m.profileInputs[ppNode].SetValue(p.Node)
		m.profileInputs[ppPool].SetValue(p.Pool)
		m.profileInputs[ppStorage].SetValue(p.Storage)
		m.profileInputs[ppBridge].SetValue(p.Bridge)
		m.profileInputs[ppTokenFile].SetValue(p.TokenFile)
		m.profileInputs[ppCAFile].SetValue(p.CAFile)
		m.profileInputs[ppIdentityPath].SetValue(p.IdentityPath)
		// The optional three. Prefilling them is not just symmetry: an empty
		// input here means "unset, so take the provider's default", and the
		// placeholder says which default that is — so a profile that HAS set
		// one must show the stored value, or the form would claim it was on the
		// default when it was not.
		m.profileInputs[ppUser].SetValue(p.User)
		m.profileInputs[ppImageStorage].SetValue(p.ImageStorage)
		m.profileInputs[ppBaseImage].SetValue(p.BaseImage)
	}
	m.profileFormFocus = 0
	m.profileFormErr = nil
	m.view = viewProfileForm
	return m.profileInputs[0].Focus()
}

// profileFormStep walks the form's fields (profileFormSlots) by delta,
// wrapping around. A single-field (Local, rename-only) form has nothing to
// walk between. Landing on the insecure checkbox blurs whatever text input
// was previously focused and focuses nothing — there is no textinput.Model
// backing that row to call .Focus() on.
//
// One function for both directions, not a matched pair: forward and backward
// traversal have to agree about which rows exist and which are focusable, and
// two copies can only agree by being kept in step by hand — a row skipped in
// one and not the other strands the user on a field they cannot leave.
func (m *model) profileFormStep(delta int) tea.Cmd {
	slots := m.profileFormSlots()
	n := len(slots)
	if n <= 1 {
		return nil
	}
	if cur := slots[m.profileFormFocus]; cur.kind == pffText {
		m.profileInputs[cur.inputIdx].Blur()
	}
	m.profileFormFocus = ((m.profileFormFocus+delta)%n + n) % n
	if next := slots[m.profileFormFocus]; next.kind == pffText {
		return m.profileInputs[next.inputIdx].Focus()
	}
	return nil
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
	switch m.profileFormType {
	case profiles.TypeRemoteSSH:
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
	case profiles.TypeProxmox:
		p.Host = strings.TrimSpace(m.profileInputs[ppHost].Value())
		p.Node = strings.TrimSpace(m.profileInputs[ppNode].Value())
		p.Pool = strings.TrimSpace(m.profileInputs[ppPool].Value())
		p.Storage = strings.TrimSpace(m.profileInputs[ppStorage].Value())
		p.Bridge = strings.TrimSpace(m.profileInputs[ppBridge].Value())
		// A PATH to the credential file, never the token value itself — the
		// same secret-free contract IdentityPath keeps above.
		p.TokenFile = strings.TrimSpace(m.profileInputs[ppTokenFile].Value())
		p.CAFile = strings.TrimSpace(m.profileInputs[ppCAFile].Value())
		p.IdentityPath = strings.TrimSpace(m.profileInputs[ppIdentityPath].Value())
		// The optional three: a blank input is a deliberate "use the default"
		// (the local username, "local", the project golden image — see
		// NewProxmox), so each is stored empty rather than being defaulted
		// here. Resolving them in the form would freeze today's default into
		// the saved profile and quietly opt that profile out of the next one.
		p.User = strings.TrimSpace(m.profileInputs[ppUser].Value())
		p.ImageStorage = strings.TrimSpace(m.profileInputs[ppImageStorage].Value())
		p.BaseImage = strings.TrimSpace(m.profileInputs[ppBaseImage].Value())
		p.Insecure = m.profileInsecure
		// The first four mirror profiles.validate's own Proxmox rule (store.go)
		// for immediate in-form feedback; the store re-checks those (and
		// uniqueness) regardless, so for them this is a UX nicety, not the
		// authority.
		//
		// The last three are NOT in validate, and for them this table is the
		// only gate. Each is demanded by the layer that actually creates a VM,
		// so a profile saved without one is a profile that can never build
		// anything — the form would have accepted a configuration that is dead
		// on arrival:
		//
		//   identity path — the key sand installs into every guest via
		//     cloud-init and then connects with (readPublicKey, then ssh -i).
		//   storage       — backs scsi0/ide2; pve.CreateVMOptions rejects an
		//     empty one outright (internal/pve/vm.go).
		//   bridge        — likewise rejected there, and for a subtler reason:
		//     an omitted bridge does not mean "no network", it silently gives
		//     QEMU user-mode NAT, which leaves the guest unreachable over SSH
		//     in a way that looks like a boot failure.
		//
		// Deliberately caught here rather than in profiles.validate: LoadFrom
		// must keep loading a hand-edited profile that fails these (see its doc
		// comment — quarantining the whole file over one field would lock the
		// user out of every other profile), and buildBinding already surfaces
		// what it can as a per-profile error binding. This is the create/edit
		// path, the one place a human is present to fix it.
		for _, req := range []struct{ v, name string }{
			{p.Host, "host"}, {p.Node, "node"}, {p.Pool, "pool"}, {p.TokenFile, "token file"},
			{p.IdentityPath, "identity path"}, {p.Storage, "storage"}, {p.Bridge, "bridge"},
		} {
			if req.v == "" {
				m.profileFormErr = fmt.Errorf("%s is required", req.name)
				return m, nil
			}
		}
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
	// NOTE: nothing is carried across from `existing` beyond Enabled, and that
	// is a property of the FORMS, not a simplification. p is built from the
	// inputs alone and Store.Update replaces the stored record WHOLESALE, so
	// any field the form cannot show is erased from profiles.yaml by an edit as
	// innocent as a rename — silently, since the user never saw it to know they
	// dropped it. Each form renders every field its type uses (the RemoteSSH
	// one always has; the Proxmox one now does too, which is what retired the
	// User/ImageStorage/BaseImage carry-across this comment replaces), so there
	// is nothing left to lose. Add a Proxmox field without an input and that
	// silent erasure comes straight back — restore the carry-across here, or,
	// better, give the field a row in profileFormSlots.
	cmd := m.submitProfileEdit(p)
	if m.profileFormErr == nil {
		m.view = viewProfiles
		m.clampProfileCursor()
	}
	return m, cmd
}

// updateProfileForm handles keys on the create/edit sub-form.
func (m model) updateProfileForm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	onCheckbox := m.profileFormFocusIsCheckbox()
	switch {
	case msg.Code == tea.KeyEsc:
		m.view = viewProfiles
		m.profileFormErr = nil
		return m, nil
	case onCheckbox && msg.Code == tea.KeySpace:
		// SPACE ONLY. Enter deliberately falls through to m.keys.Down below and
		// advances focus like it does on every other row: the footer promises
		// "↓/enter next field", so toggling on enter both broke that promise
		// and made the row a trap — enter could never leave it, and each press
		// silently flipped TLS verification off. The footer names space as the
		// toggle whenever this row exists (profileFormHelp).
		m.profileInsecure = !m.profileInsecure
		return m, nil
	case key.Matches(msg, m.keys.Save):
		return m.submitProfileForm()
	case key.Matches(msg, m.keys.ShiftTab), key.Matches(msg, m.keys.Up):
		return m, m.profileFormStep(-1)
	case key.Matches(msg, m.keys.Down), key.Matches(msg, m.keys.Tab):
		return m, m.profileFormStep(+1)
	}
	if onCheckbox {
		// No textinput.Model backs this row, and text keys must never reach
		// one of the actual fields while the checkbox is focused — every
		// other input is already blurred (profileFormFocusNext/Prev), so
		// this is belt-and-suspenders, not load-bearing, but it keeps the
		// contract explicit rather than relying on bubbles' own blurred-input
		// no-op.
		return m, nil
	}
	cmds := make([]tea.Cmd, len(m.profileInputs))
	for i := range m.profileInputs {
		m.profileInputs[i], cmds[i] = m.profileInputs[i].Update(msg)
	}
	return m, tea.Batch(cmds...)
}

// profileFormToggle is the checkbox row's own key, screen-local like
// profileMove/profileEdit. It earns a footer slot because the checkbox is
// otherwise undiscoverable: every other row is a text input, so nothing tells
// the reader that this one takes a key at all.
var profileFormToggle = key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle"))

// profileFormHelp is the create/edit form's footer. The toggle key appears
// only for a form that actually HAS a checkbox row (Proxmox today), so the
// Local/RemoteSSH footers stay exactly as they were.
func (m model) profileFormHelp() []key.Binding {
	binds := []key.Binding{m.keys.Up, m.keys.Down}
	for _, s := range m.profileFormSlots() {
		if s.kind == pffCheckbox {
			binds = append(binds, profileFormToggle)
			break
		}
	}
	return append(binds, m.keys.Save, m.keys.Back)
}

// profileFormInfo is the help text for the row that currently has focus, or
// "" when there is none to show. Bounds-checked rather than indexed straight
// into the slice: profileFormFocus is clamped by profileFormStep alone, and a
// form opened at focus 0 on a type whose slots are shorter than the last
// type's would otherwise panic in the VIEW, where a panic takes the whole TUI
// down rather than surfacing as an error.
func (m model) profileFormInfo() string {
	slots := m.profileFormSlots()
	if m.profileFormFocus < 0 || m.profileFormFocus >= len(slots) {
		return ""
	}
	return slots[m.profileFormFocus].info
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

	for i, s := range m.profileFormSlots() {
		ls := labelStyle
		if i == m.profileFormFocus {
			ls = focusedLabelStyle
		}
		if s.kind == pffCheckbox {
			box := "[ ]"
			if m.profileInsecure {
				box = "[x]"
			}
			// Rendered as one "[x] Insecure" chip rather than the
			// "Label: value" shape the text rows use below — there is no
			// value column for a checkbox — but through the SAME
			// labelStyle/focusedLabelStyle pair, so the focused row still
			// highlights exactly like every other field (styles.go).
			b.WriteString(ls.Render(box+" "+s.label) + "\n")
			continue
		}
		b.WriteString(ls.Render(s.label+":") + " " + m.profileInputs[s.inputIdx].View() + "\n")
	}

	// The error and the footer are built BEFORE the help block so the help can
	// be given only the lines those two leave behind. Order of precedence, not
	// of rendering: the footer is the sole on-screen statement of how to save
	// or leave the form, and the error is why a save did not happen — help
	// text is the one part a short terminal can afford to lose. The Proxmox
	// form is thirteen rows now, so at 80x24 with an error showing this is not
	// hypothetical: unbudgeted, the footer scrolled off the bottom.
	var tail strings.Builder
	if m.profileFormErr != nil {
		tail.WriteString("\n" + errStyle.Width(cw).Render("Error: "+m.profileFormErr.Error()) + "\n")
	}
	tail.WriteString("\n" + m.footerView(m.profileFormHelp()))

	if info := m.profileFormInfo(); info != "" {
		// cw-2 accounts for fieldInfoStyle's left border + left padding, so the
		// wrapped help still fits the content column — the same subtraction the
		// VM create form makes (form.go).
		style := fieldInfoStyle.Width(cw - 2)
		// -1 for the blank line that separates the help from the fields above.
		budget := m.layout.ContentHeight - lipgloss.Height(b.String()) - lipgloss.Height(tail.String()) - 1
		if block := clampBlockHeight(style.Render(info), budget, style); block != "" {
			b.WriteString("\n" + block + "\n")
		}
	}

	b.WriteString(tail.String())
	return appStyle.Render(b.String())
}

// clampBlockHeight truncates an already-rendered block to at most n lines,
// marking the cut with an ellipsis row drawn through the SAME style so the
// truncation reads as "there is more" rather than as a sentence that just
// stops.
//
// A budget under two lines drops the block entirely rather than spending the
// one line it has on a bare "…", which says nothing a reader can use. A zero
// or negative budget is the ordinary case on a short terminal, not an error:
// the caller simply draws no help.
func clampBlockHeight(block string, n int, style lipgloss.Style) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(block, "\n")
	if len(lines) <= n {
		return block
	}
	if n < 2 {
		return ""
	}
	// Full slice expression so the append writes into a fresh array: `lines`
	// aliases the caller's block, and growing it in place would scribble the
	// ellipsis over a line the caller may still read.
	return strings.Join(append(lines[:n-1:n-1], style.Render("…")), "\n")
}

// profileTypePickerSelect is the picker's own local key.Binding, mirroring
// profileMove/profileEdit above — screen-local since nothing outside this
// view dispatches it. Up/down reuse profileMove; there is no separate
// binding for them.
var profileTypePickerSelect = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select"))

// updateProfileTypePicker handles keys on the CREATE path's pre-form type
// picker (viewProfileTypePicker): move the ring over creatableProfileTypes,
// esc back to the profile list, or enter to open the field form for the
// type under the cursor (openProfileFormForType) — mirroring
// updateProfiles' own up/down-then-verb shape.
func (m model) updateProfileTypePicker(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.Code {
	case tea.KeyUp:
		if m.profileTypeCursor > 0 {
			m.profileTypeCursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.profileTypeCursor < len(creatableProfileTypes)-1 {
			m.profileTypeCursor++
		}
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.Back):
		m.view = viewProfiles
		return m, nil
	case key.Matches(msg, profileTypePickerSelect):
		return m, m.openProfileFormForType(creatableProfileTypes[m.profileTypeCursor])
	}
	return m, nil
}

// profileTypePickerHelp is the picker's footer.
func (m model) profileTypePickerHelp() []key.Binding {
	return []key.Binding{profileMove, profileTypePickerSelect, m.keys.Back}
}

// profileTypePickerView renders the pre-form type picker: one row per
// creatable type (creatableProfileTypes), with the same cursor/focus styling
// the profile list and the field form already use, so the three screens read
// as one flow rather than three different UIs bolted together.
func (m model) profileTypePickerView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("New Connection Profile"))
	b.WriteString("\n\n")

	for i, t := range creatableProfileTypes {
		cursor := "  "
		ls := labelStyle
		if i == m.profileTypeCursor {
			cursor = "> "
			ls = focusedLabelStyle
		}
		b.WriteString(ls.Render(cursor+profileTypeLabel(t)) + "\n")
	}

	b.WriteString("\n" + m.footerView(m.profileTypePickerHelp()))
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
		m.openProfileCreateForm()
		return m, nil

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
	switch p.Type {
	case profiles.TypeRemoteSSH:
		kind = "Remote SSH"
		target = fmt.Sprintf("%s@%s:%d", p.User, p.Host, p.Port)
	case profiles.TypeProxmox:
		kind = "Proxmox"
		// Mirrors profiles.Profile.proxmoxTarget's "host:node/pool" format —
		// TokenFile is deliberately excluded, exactly as there.
		target = fmt.Sprintf("%s:%s/%s", p.Host, p.Node, p.Pool)
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
