package ui

import (
	"testing"

	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// TestInFlightRemoteMarkerGetsABuildingTile is the board-level proof for the
// "in-flight build convergence" feature: a VM another controller is still
// building carries a PROVISIONAL provenance marker (Provisioning=true) on
// disk, but THIS controller has no local job for it — no seedJob, nothing in
// m.jobs. Without the marker it would be invisible (or, worse, a reassuring
// green "Running" once Lima reports it). With it, boardVMs must raise a tile
// (the marker alone makes it "managed", mirroring
// TestBoardRosterGatesOnProvenanceWithLegacyRegistryFallback) and statusOf
// must derive statusBuilding, not statusRunning (mirroring deriveStatus's
// remoteProvisioning branch, jobs.go).
func TestInFlightRemoteMarkerGetsABuildingTile(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)

	// Seed the member's live VM list AND its provenance map directly — the
	// same pattern board_test.go's
	// TestBoardRosterGatesOnProvenanceWithLegacyRegistryFallback uses via
	// vmsLoadedMsg's provenance field, but set on the member in place since
	// this test needs no registry/legacy-fallback interaction at all.
	m.members[0].vms = []vm.VM{{Name: "building", Status: "Running"}}
	m.members[0].provenance = map[string]provider.Provenance{
		"building": {SchemaVersion: provider.MarkerSchemaVersion, Base: "sandbar-base", Provisioning: true},
	}

	if got := boardNames(m); len(got) != 1 || got[0] != "building" {
		t.Fatalf("boardNames = %v, want [building] (a provisional provenance marker alone makes a VM managed)", got)
	}

	found := false
	for _, v := range m.boardVMs() {
		if v.Name == "building" {
			found = true
		}
	}
	if !found {
		t.Fatal("boardVMs did not include the in-flight VM")
	}

	v := vm.VM{Name: "building", Status: "Running"}
	if got := m.statusOf(registry.LocalScope, v); got != statusBuilding {
		t.Fatalf("statusOf = %v, want statusBuilding (no local job, but the remote provenance marker is Provisioning=true)", got)
	}
}
