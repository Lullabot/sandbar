package ui

// markerheal.go repairs ABANDONED in-flight provenance markers.
//
// The bug it closes: a marker's Provisioning flag is raised when a build starts
// and lowered only by manage.RecordSuccess, in the controller that started it.
// If that controller never reaches the handler — its TUI was quit or killed
// mid-build, its transport dropped, its final marker write failed — the flag
// stays raised on a VM that finished building and works perfectly. Every
// controller then reads that marker on every refresh and paints "Building", and
// since deriveStatus consults remoteProvisioning BEFORE the VM's real status,
// the tile cannot even fall through to Stopped. Nothing in sand cleared it: the
// only exit was hand-editing the marker on the host (a Proxmox VM's Notes
// field), which is not a thing a user should ever need to know exists.
//
// The repair is deliberately AUTOMATIC and has no user-facing verb. A stale
// marker is not a decision anyone needs to make — it is bookkeeping that fell
// out of step with reality, and the tool can see that and fix it. Shipping a
// "clear this marker" command instead would have made every user learn the
// marker exists in order to recover from a bug in how sand maintains it.

import (
	"context"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
)

// markerHealedMsg reports one abandoned-marker repair. It carries the scope and
// name so the handler can clear the in-flight guard and patch the member's own
// provenance map, and an error so a repair that could not be written is said
// once rather than retried in silence.
type markerHealedMsg struct {
	scope registry.Scope
	vm    string
	err   error
}

// healMarkerCmd writes pv's ready form back over the VM's marker, off the Update
// goroutine — for a remote member this is an ssh or REST round trip, and it is
// dispatched from the refresh handler, so doing it inline would stall the UI on
// every tick that noticed a stale marker.
//
// It writes the marker it was GIVEN, cleaned (Provenance.Ready), rather than
// building a fresh one: the healing controller may not be the one that built
// this VM and has no authority over its Base or Config. Preserving them is what
// keeps the repaired VM recreate-able from what it was actually built from.
func healMarkerCmd(pv provider.Provenancer, scope registry.Scope, name string, p provider.Provenance) tea.Cmd {
	if pv == nil {
		return nil
	}
	return func() tea.Msg {
		return markerHealedMsg{scope: scope, vm: name, err: pv.MarkManaged(context.Background(), name, p.Ready())}
	}
}

// healAbandonedMarkers finds every VM in mem whose marker is an abandoned
// in-flight one and returns a repair command for each, recording them in the
// member's in-flight guard as it goes.
//
// THE LOCAL JOB REGISTRY IS THE VETO, and it is why this lives here on the model
// rather than inside refreshCmd with the rest of the provenance read.
// provider.BuildAbandoned can only see the marker, and the one controller whose
// judgement must override the clock is the controller actually running the build
// — its own role could, in principle, outlast the cutoff. A VM this controller
// has a live provision job for is therefore never healed, whatever its marker
// says.
//
// Names are sorted so a member with several stale markers dispatches (and logs)
// them deterministically, which is what makes the behaviour testable.
func (m *model) healAbandonedMarkers(mem *fleetMember, now time.Time) []tea.Cmd {
	// Re-arm the narration latch for every name whose marker this refresh reports
	// as no longer abandoned — the repair landed, or someone else's did, or the VM
	// is gone. Done FIRST (before the empty-map early return) so an emptied
	// provenance map cannot strand a name latched forever.
	for name := range mem.healSaid {
		if p, ok := mem.provenance[name]; !ok || !p.BuildAbandoned(now) {
			delete(mem.healSaid, name)
		}
	}
	if len(mem.provenance) == 0 {
		return nil
	}
	// mem.prov, not m.provFor(mem.scope): the member is already in hand, so
	// re-resolving it by scope is a needless scan of the fleet that would, for two
	// members sharing a scope, hand back the FIRST one's backend rather than this
	// one's — writing the repair over the wrong transport.
	pv, ok := mem.prov.(provider.Provenancer)
	if !ok {
		return nil // no marker facility on this backend: nothing to repair
	}
	names := make([]string, 0, len(mem.provenance))
	for name, p := range mem.provenance {
		if !p.BuildAbandoned(now) || mem.healing[name] || m.jobs.Building(mem.scope, name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	cmds := make([]tea.Cmd, 0, len(names))
	for _, name := range names {
		if mem.healing == nil {
			mem.healing = map[string]bool{}
		}
		mem.healing[name] = true
		// Said out loud, not repaired in silence. The user watched this tile claim
		// to be building for however long the marker sat there; a line in the
		// Messages log is what connects the tile they remember to the tile they
		// now see, and distinguishes a repair from the board simply glitching.
		//
		// ONCE per stuck marker, though, not once per attempt. A repair that keeps
		// failing is re-dispatched on every 5s refresh (that is deliberate — a
		// transport blip must not latch the repair off), and narrating each of those
		// attempts would flood the 50-entry Messages ring with this one sentence in
		// about four minutes, pushing out the latched warning that says WHY it is
		// failing. The latch clears above, once the marker is seen repaired.
		if !mem.healSaid[name] {
			if mem.healSaid == nil {
				mem.healSaid = map[string]bool{}
			}
			mem.healSaid[name] = true
			m.logMsg(name + " was still marked as building by an interrupted run — recording it as finished")
		}
		cmds = append(cmds, healMarkerCmd(pv, mem.scope, name, mem.provenance[name]))
	}
	return cmds
}

// applyMarkerHealed folds a repair's outcome back into the member: the in-flight
// guard is released either way, and a SUCCESS also patches the member's own
// provenance map so the tile flips this frame instead of waiting for the next
// refresh to re-read a marker we already know the contents of.
func (m *model) applyMarkerHealed(msg markerHealedMsg) {
	// routeIndex, and a POINTER into m.members — memberByScope hands back a value
	// copy, which every mutation below would write to and throw away. A repair
	// whose scope no longer matches any member (the profile was deleted or
	// connection-edited while the write was in flight) is dropped, exactly as the
	// other scope-tagged handlers drop theirs.
	mi, ok := m.routeIndex(msg.scope)
	if !ok {
		return
	}
	mem := &m.members[mi]
	delete(mem.healing, msg.vm)
	if msg.err != nil {
		// A repair that cannot be written leaves the tile exactly as it was — still
		// reading Building — so this must not pass unremarked. Latched per member
		// (the repair is retried on the next refresh) so an unwritable marker says
		// this once, not once every few seconds.
		if !mem.healWarned {
			mem.healWarned = true
			m.logWarn("could not clear the stale building marker on " + msg.vm + ": " + msg.err.Error())
		}
		return
	}
	mem.healWarned = false
	if p, ok := mem.provenance[msg.vm]; ok {
		mem.provenance[msg.vm] = p.Ready()
	}
}
