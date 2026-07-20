package ui

import (
	"strings"
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

// TestRemoteBuildProgressReachesTheObserversBar proves the OTHER half of
// in-flight convergence: not just THAT a VM is building elsewhere, but HOW FAR
// ALONG it is.
//
// The bar on the building controller's own tile is parsed from the
// provisioner's streamed stdout — a byte stream that exists only in that
// process — so an observer has no access to it and its bar sat at zero for the
// entire build. The marker's Progress field is the only channel that crosses,
// and this pins that the tile renders from it when there is no local job.
func TestRemoteBuildProgressReachesTheObserversBar(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 40)

	m.members[0].vms = []vm.VM{{Name: "building", Status: "Running"}}
	m.members[0].provenance = map[string]provider.Provenance{
		"building": {
			SchemaVersion: provider.MarkerSchemaVersion,
			Base:          "sandbar-base",
			Provisioning:  true,
			Progress:      provider.BuildProgress{Role: "claude-code", Index: 30, Total: 120},
		},
	}

	got := m.remoteProgress(registry.LocalScope, "building")
	if got.Role != "claude-code" || got.Index != 30 || got.Total != 120 {
		t.Fatalf("remoteProgress = %+v, want the marker's published position", got)
	}
	if f := got.Fraction(); f < 0.24 || f > 0.26 {
		t.Fatalf("Fraction = %v, want ~0.25 (30/120)", f)
	}

	// And it reaches the rendered tile: the role name is drawn from the same
	// progress the bar is, so finding it proves the renderer took the REMOTE
	// source rather than the (empty) local job's.
	tile := renderTile(tileInput{
		VM:                 vm.VM{Name: "building", Status: "Running"},
		HasJob:             false,
		RemoteProvisioning: true,
		RemoteProgress:     got,
		Width:              40,
	})
	if !strings.Contains(tile, "claude-code") {
		t.Fatalf("the observer's tile does not name the remote build's role:\n%s", tile)
	}
	// A zero-progress marker (an older v2 one, or a build that has not reached
	// its first role) must render an EMPTY bar, never a full one.
	empty := renderTile(tileInput{
		VM:                 vm.VM{Name: "building", Status: "Running"},
		HasJob:             false,
		RemoteProvisioning: true,
		RemoteProgress:     ansibleProgress{},
		Width:              40,
	})
	if strings.Contains(empty, "claude-code") {
		t.Fatalf("a marker with no progress must not inherit another tile's role:\n%s", empty)
	}
}

// TestProgressRepublishesOnlyAtRoleBoundaries pins the throttle. Each republish
// is a marker write — an ssh round trip for a remote build — and an Ansible run
// is hundreds of tasks but only a dozen roles. Publishing per task would make
// the feature unusable on exactly the setup it exists for; publishing per role
// gives a bar that visibly moves.
func TestProgressRepublishesOnlyAtRoleBoundaries(t *testing.T) {
	m := newTestModel(t)
	m.jobs = newJobRegistry()
	key := provisionKey(registry.LocalScope, "web")
	if !m.jobs.begin(&job{key: key, state: jobRunning, cfg: vm.CreateConfig{Name: "web"}, cancel: func() {}}) {
		t.Fatal("begin job")
	}

	feed := func(lines string) (provider.BuildProgress, bool) {
		m.jobs.addOutput(key, lines)
		_, prog, due := m.jobs.progressToPublish(key)
		return prog, due
	}

	if _, due := feed("PLAY RECAP\n"); due {
		t.Fatal("output that moves nothing must not trigger a republish")
	}
	// A task total, then the first role: a real change, so it publishes.
	feed("SAND_ANSIBLE_TASK_TOTAL=120\n")
	prog, due := feed("TASK [base : install packages] ***\n")
	if !due {
		t.Fatal("entering the first role must publish")
	}
	if prog.Role != "base" || prog.Total != 120 {
		t.Fatalf("published %+v, want Role=base Total=120", prog)
	}

	// More tasks in the SAME role: the bar advances locally, but nothing crosses
	// the wire.
	for i := 0; i < 5; i++ {
		if _, due := feed("TASK [base : another thing] ***\n"); due {
			t.Fatalf("task %d within the same role triggered a republish — the throttle is defeated", i)
		}
	}

	// A new role: publishes again, carrying the advanced index.
	prog, due = feed("TASK [claude-code : install] ***\n")
	if !due {
		t.Fatal("crossing into a new role must publish")
	}
	if prog.Role != "claude-code" {
		t.Fatalf("published Role = %q, want claude-code", prog.Role)
	}
	if prog.Index <= 1 {
		t.Fatalf("published Index = %d, want the tasks counted since the run began", prog.Index)
	}
}

// TestOnlyRunningProvisionJobsRepublishProgress guards the two ways a republish
// would write a marker it has no business writing: a TRANSFER has no marker to
// update at all, and a FINISHED build's last word belongs to RecordSuccess — a
// late chunk arriving after the ready marker was written must not resurrect a
// Provisioning=true one on a VM that just finished building.
func TestOnlyRunningProvisionJobsRepublishProgress(t *testing.T) {
	m := newTestModel(t)
	m.jobs = newJobRegistry()

	transfer := transferKey(registry.LocalScope, "web")
	if !m.jobs.begin(&job{key: transfer, state: jobRunning, cancel: func() {}}) {
		t.Fatal("begin transfer")
	}
	m.jobs.addOutput(transfer, "SAND_ANSIBLE_TASK_TOTAL=120\nTASK [base : x] ***\n")
	if _, _, due := m.jobs.progressToPublish(transfer); due {
		t.Fatal("a file transfer republished build progress — it owns no marker")
	}

	build := provisionKey(registry.LocalScope, "api")
	if !m.jobs.begin(&job{key: build, state: jobRunning, cfg: vm.CreateConfig{Name: "api"}, cancel: func() {}}) {
		t.Fatal("begin build")
	}
	if _, ok := m.jobs.finish(build, nil); !ok {
		t.Fatal("finish build")
	}
	m.jobs.addOutput(build, "TASK [late : straggler] ***\n")
	if _, _, due := m.jobs.progressToPublish(build); due {
		t.Fatal("a finished build republished progress — it would undo RecordSuccess's ready marker")
	}
}
