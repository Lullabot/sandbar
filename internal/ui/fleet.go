package ui

// fleet.go is the model's multi-profile core: the board is no longer a view over
// ONE provider, it is the UNION of a FLEET of per-profile sub-states, each
// connecting, listing, reconciling, heartbeating and self-healing on its own —
// so a slow or unreachable remote profile can never block the UI or a healthy
// profile's tiles.
//
// # Why a slice of sub-states, not one provider
//
// Before this, the model held a single `p provider.Provider` + `scope`. Every
// VM lifecycle call, every reconcile, every heartbeat keyed off that one scope,
// and the board rendered exactly one provider's `limactl list`. A remote profile
// was a whole separate `sand` process. This file replaces that with `members` —
// one fleetMember per ENABLED profile (from provider.BuildFleet) — and threads
// the OWNING scope through every per-VM operation so two profiles that both have
// a VM named "web" can never prune, delete, or sample each other's (the
// vmHandle/jobKey scope keys task 6 introduced are what make that safe).
//
// # Model-by-value discipline
//
// members is value state on the model, updated by returning copies from Update
// handlers — exactly like m.vms was. All blocking work (Preflight, List,
// per-VM disk sampling, host-capacity probes) happens inside tea.Cmd closures
// that capture a member's provider/hostFiles/scope by VALUE; results come back
// as scope-tagged messages (vmsLoadedMsg) that Update routes to the right
// member. No tea.Cmd goroutine ever reads the members slice.

import (
	"time"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
)

// connState is one fleet member's connection lifecycle. It is deliberately NOT
// derivedStatus (which is a VM's status): this is about the PROFILE's link to
// its backend, surfaced by task 10's per-profile status bar.
type connState int

const (
	// connConnecting is the state from startup until the member's first list
	// returns — its preflight and initial `limactl list` are still in flight.
	// The local member passes through this almost instantly; a remote member
	// stays here for the length of its SSH handshake.
	connConnecting connState = iota
	// connConnected means the member's last list succeeded. Only connected
	// members' VMs are reconciled/pruned; a member's tiles keep rendering from
	// its last-known list.
	connConnected
	// connErrored means the member's preflight or last list FAILED (or its
	// provider failed to construct — an error binding). Its entries stay
	// DORMANT: rendered from the last-known list, but never reconciled or
	// pruned on a failed list, and it self-heals with backoff.
	connErrored
	// connDisabled is a member whose profile the user disabled without
	// deleting it (task 8's live mutation): its binding is torn down, its
	// refresh/heartbeat cmds are stopped, and its tiles are hidden (boardVMs
	// skips it) — but the member itself stays in the fleet so the header can
	// still name it and say why its VMs are gone, exactly as an errored
	// member does (task 10's banner row).
	connDisabled
)

// backoffSteps is the errored-member self-heal schedule: instead of retrying
// every refreshInterval, an errored member backs off 5s → 30s → 60s (capped) on
// consecutive failures and resets to the normal cadence on the first success.
// The first step equals refreshInterval so a single blip retries at the normal
// rate; only a persistent failure slows down. Per-member, so one wedged profile
// never slows a healthy one's refresh.
var backoffSteps = []time.Duration{5 * time.Second, 30 * time.Second, 60 * time.Second}

// hostSample is one member's host capacity + identity, sampled off the Update
// goroutine (in refreshCmd) exactly as the single-provider header used to sample
// them. For a remote member these are the REMOTE host's numbers; for local Lima
// the provider returns zero and the ui probes this machine directly (see
// refreshCmd). Zero fields mean "not sampled yet".
type hostSample struct {
	mem      int64
	diskFree int64
	cpus     int
	user     string

	// memAvail is the host's available memory (/proc/meminfo's MemAvailable,
	// via hostMemAvailBytes — hostwarn.go), and diskTotal is the total size of
	// the volume diskFree is measured against (hostDiskTotalBytes locally,
	// lima.SSHHost.HostResources over ssh). Both are new denominators/readings
	// the low-capacity-warning feature needs beside mem/diskFree, sampled the
	// same way and subject to the same "zero means not sampled" rule — a
	// warning check must never compute a percentage from an absent one.
	memAvail  int64
	diskTotal int64
}

// fleetMember is one enabled profile's live sub-state. Everything a tile, the
// header, a reconcile or a heartbeat needs about ONE profile lives here, keyed
// into the model's members slice.
type fleetMember struct {
	profile profiles.Profile
	// prov is the constructed backend, or nil for an error binding (a profile
	// whose provider failed to construct — see provider.Binding.Err). A nil-prov
	// member is permanently errored for this session: it never lists and never
	// self-heals (task 8's live mutation is what re-builds it).
	prov  provider.Provider
	scope registry.Scope
	// hostFiles is this member's host-access seam — the local filesystem for
	// local Lima, or a remote provider's SSHHost. It is what makes a tile's disk
	// / up-since / last-used sampling resolve on the host the VM ACTUALLY runs
	// on (in refreshCmd), retiring the old ui.hostFiles process-global: a local
	// VM and a remote VM sample on their own hosts in the same render.
	hostFiles lima.HostFiles

	// vms is this member's last-known `limactl list`, already sampled. The board
	// roster is the union of every member's vms (boardVMs).
	vms  []vm.VM
	host hostSample

	state   connState
	lastErr error

	// listRace is this member's OWN lima#5236 clone-window suppression counter
	// (the model used to hold one shared one). A clone in flight on one profile
	// must not suppress another's list errors.
	listRace int

	// backoff counts this member's CONSECUTIVE failed lists (0 = healthy). It
	// drives refreshDelay's self-heal cadence and resets to 0 on any success.
	backoff int

	// arming is true while exactly one refresh-tick loop is in flight for this
	// member — the per-member equivalent of the old model-wide m.refreshing
	// guard, so tickRefresh cannot stack duplicate ticks for one member while
	// still arming the others.
	arming bool

	// warnedHostMem/warnedHostDisk latch a host-capacity warning already
	// logged for THIS member (hostwarn.go's checkHostMemWarn/checkHostDiskWarn),
	// so the refresh loop — which re-evaluates every refreshInterval — does not
	// re-log the same warning every tick while the host sits below the 5%-free
	// line. Cleared the moment the member recovers to >=5% free, so a LATER
	// re-crossing warns again rather than staying silent forever after the
	// first time.
	warnedHostMem  bool
	warnedHostDisk bool

	// everListed is set the first time this member's list SUCCEEDS (model.go's
	// vmsLoadedMsg handler), and is NEVER cleared afterward — not by a later
	// error, not by disable. boardReady asks this in ADDITION to the member's
	// CURRENT state, because "has this member ever proven it has no VMs" is a
	// one-way door: a sole member that connects with zero VMs shows the
	// create-ghost, and a LATER persistent list failure on that same member
	// must not un-ring that bell and flip the board back to "connecting…" —
	// which would also stop Enter from creating a VM. See boardReady.
	everListed bool
}

// boardVM is one roster entry: a VM plus the scope of the member that owns it.
// The board roster is the union across the fleet, so a bare vm.VM is no longer
// enough to key its per-VM state — two profiles can both have a "web", and its
// job status, heartbeat, base image and secrets all resolve by (scope, name).
// vm.VM is embedded, so bv.Name / bv.Status / bv.CPUs read straight through
// while bv.scope carries the owning profile.
type boardVM struct {
	vm.VM
	scope registry.Scope
}

// refreshDelay is when this member's next refresh tick should fire: the normal
// cadence while connected/connecting, or its current backoff step while errored.
func (mem fleetMember) refreshDelay() time.Duration {
	if mem.state != connErrored || mem.backoff == 0 {
		return refreshInterval
	}
	i := mem.backoff - 1
	if i >= len(backoffSteps) {
		i = len(backoffSteps) - 1
	}
	return backoffSteps[i]
}

// memberIndex returns the index of the member owning scope, and whether one
// exists. Scope is a comparable struct, so this is an exact match — never a
// same-name-different-scope collision.
func (m model) memberIndex(sc registry.Scope) (int, bool) {
	for i := range m.members {
		if m.members[i].scope == sc {
			return i, true
		}
	}
	return 0, false
}

// memberByScope returns a value copy of the member owning scope.
func (m model) memberByScope(sc registry.Scope) (fleetMember, bool) {
	if i, ok := m.memberIndex(sc); ok {
		return m.members[i], true
	}
	return fleetMember{}, false
}

// routeIndex resolves the member a scope-tagged message belongs to. A real
// message always carries a genuine scope. A ZERO-value scope (never a real
// member — LocalScope carries Provider="local", remote scopes carry a target)
// routes to the ACTIVE member: this is the seam that lets a hand-built model in
// a test drive its single member with a bare `vmsLoadedMsg{...}` exactly as it
// did when there was only one provider. Production always tags the scope.
//
// Any OTHER unmatched, NON-zero scope is deliberately NOT routed to the active
// member — it is reported not-found instead. Before task 8 this branch was
// unreachable in production: the fleet's member count never changed after New,
// so a genuinely-tagged scope always matched some member. Task 8's live
// profile management changes that — DELETING a profile drops its member
// outright — so an in-flight refresh/connect cmd that was already running
// against that scope can still deliver its result afterward, for a scope no
// member owns any more. Falling back to "active" here would silently splice
// one profile's stale list/error onto whatever member happens to be active
// right now; dropping it (the message handlers already guard ok=false) is the
// only safe thing to do with a result nobody can use any more.
func (m model) routeIndex(sc registry.Scope) (int, bool) {
	if i, ok := m.memberIndex(sc); ok {
		return i, true
	}
	if sc != (registry.Scope{}) || len(m.members) == 0 {
		return 0, false
	}
	return m.activeIndex(), true
}

// activeIndex is the member the single-band header and a NEW create target use
// (task 10 makes the header multi-band; task 9 lets the create form pick). It is
// pinned by New (the Local member, or the first) and clamped to a valid slot.
func (m model) activeIndex() int {
	i := m.active
	if i < 0 || i >= len(m.members) {
		return 0
	}
	return i
}

// activeMember is a value copy of the active member (the zero value when the
// fleet is empty, so callers never index a nil slice).
func (m model) activeMember() fleetMember {
	if len(m.members) == 0 {
		return fleetMember{}
	}
	return m.members[m.activeIndex()]
}

// activeScope is the active member's scope — the scope a NEW create targets and
// the single-band header reports.
func (m model) activeScope() registry.Scope { return m.activeMember().scope }

// provFor returns the provider owning scope, or nil (an errored/absent member).
func (m model) provFor(sc registry.Scope) provider.Provider {
	if mem, ok := m.memberByScope(sc); ok {
		return mem.prov
	}
	return nil
}

// formHostSample is the host sample of the member the create/reset form targets
// (m.formScope) — the source of the form's cpu/memory/user defaults. For the
// zero-config single-member fleet this is the active member's, unchanged.
func (m model) formHostSample() hostSample {
	mem, _ := m.memberByScope(m.formScope)
	return mem.host
}

// formProvider is the provider the create/reset form dispatches through (its
// HostFiles seed the tool-set toggles, its Create/Reset run the build).
func (m model) formProvider() provider.Provider { return m.provFor(m.formScope) }

// boardReady reports whether ANY member's list has succeeded at least once —
// the point past which an empty board genuinely means "no VMs", not "the fleet
// hasn't connected yet". It is the fleet generalization of the old vmsLoaded
// flag: before it holds, syncBoard must not park the ring on the empty-slot
// ghost and the grid shows the connecting hint (see gridView), because the
// board is empty because nothing is loaded, not because the user has no VMs.
//
// Checked TWO ways, deliberately. state == connConnected is the fast, common
// case (and is what every test that pokes a member's state directly without
// going through the vmsLoadedMsg handler still relies on). everListed is the
// one-way door: it catches the member that connected, proved itself empty,
// and has SINCE gone connErrored — a currently-connected check alone would
// flip the board back to the "connecting…" hint (and stop Enter from
// creating a VM) the moment a sole member's list starts failing, even though
// the fleet already proved it has no VMs.
func (m model) boardReady() bool {
	for i := range m.members {
		if m.members[i].state == connConnected || m.members[i].everListed {
			return true
		}
	}
	return false
}

// enabledMemberCount is how many profiles the fleet is trying to reach — every
// member is an enabled profile (BuildFleet drops disabled ones). Used for the
// "connecting to N profiles…" hint.
func (m model) enabledMemberCount() int { return len(m.members) }

// refreshMemberCmd re-lists a single member off the Update goroutine — the
// follow-up to an action or a finished build. It re-preflights only while that
// member is not currently connected (self-heal), and returns nil for an
// absent/error-binding member (nothing to list). A zero-value scope resolves to
// the active member (a hand-built test's action carries no scope).
func (m model) refreshMemberCmd(sc registry.Scope) tea.Cmd {
	i, ok := m.routeIndex(sc)
	if !ok {
		return nil
	}
	mem := m.members[i]
	if mem.prov == nil {
		return nil
	}
	return refreshCmd(mem.scope, mem.prov, mem.hostFiles, mem.state != connConnected)
}
