package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// TestProvisionDoneWritesProvenanceMarkerForTUICreate guards the seam that a
// TUI-driven create writes the AUTHORITATIVE host-side provenance marker, not
// just the per-controller registry cache entry. The provision-done handler
// calls manage.RecordSuccess, which only writes the marker when it is handed
// the scope's Provenancer; an earlier revision omitted it, so TUI-created VMs
// got a registry entry but no marker and were therefore invisible to every
// OTHER controller of the same host (a second laptop, or the host's own local
// sand) — defeating target-attached provenance for the primary workflow.
//
// newTestModel wires a real local limaProvider (a Provenancer) over a fake
// runner, and isolateHostState points LIMA_HOME at a temp dir, so a successful
// provision must leave <LIMA_HOME>/<name>/sandbar.json on disk.
func TestProvisionDoneWritesProvenanceMarkerForTUICreate(t *testing.T) {
	m := newTestModel(t)
	seedJob(t, &m, "myvm", vm.CreateConfig{Name: "myvm", BaseName: "sandbar-base"})
	// The instance directory stands in for the clone that a real build would
	// have completed by the time it succeeds. MarkManaged refuses to mark an
	// instance that does not exist, because a marker write that created its own
	// parent leaves a lima.yaml-less directory that makes `limactl list` fatal.
	if err := os.MkdirAll(filepath.Join(os.Getenv("LIMA_HOME"), "myvm"), 0o700); err != nil {
		t.Fatalf("seed instance dir: %v", err)
	}

	done, _ := m.Update(provisionDoneMsg{job: provisionKey(registry.LocalScope, "myvm")})
	_ = done.(model)

	markerPath := filepath.Join(os.Getenv("LIMA_HOME"), "myvm", "sandbar.json")
	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("a TUI-created VM must get a provenance marker at %s (without it, other controllers can't see the VM): %v", markerPath, err)
	}
	if !strings.Contains(string(data), `"base":"sandbar-base"`) {
		t.Fatalf("provenance marker is missing the recorded base: %s", data)
	}
}

// TestAFailedProvenanceReadIsSaidOnceAndReArms pins the reporting of a batched
// provenance read that fails. The failure is NOT fatal — the board keeps its
// tiles and every VM falls through to the legacy per-controller registry — and
// that is exactly why it has to be said out loud: a controller silently showing
// only the VMs it created itself is indistinguishable, on screen, from a
// healthy board, which is how a desynchronized marker stream stayed invisible
// through a whole release of this feature.
//
// Said ONCE per failure streak (the handler runs on every 5s refresh), and
// re-armed on recovery so a later regression speaks again.
func TestAFailedProvenanceReadIsSaidOnceAndReArms(t *testing.T) {
	m := newTestModel(t)
	m = loadManaged(t, m, vm.VM{Name: "api", Status: "Running"})
	before := len(m.boardVMs())

	failed := vmsLoadedMsg{
		vms:           []vm.VM{{Name: "api", Status: "Running"}},
		provenanceErr: fmt.Errorf("parse instance markers: bad length for %q: %q", "", "api"),
	}
	for i := 0; i < 5; i++ { // five refresh ticks, as a persistent failure would produce
		next, _ := m.Update(failed)
		m = next.(model)
	}

	if got := len(m.boardVMs()); got != before {
		t.Fatalf("a failed provenance read must not empty the board: %d tiles, want %d", got, before)
	}
	if n := countMessages(m, "provenance read failed"); n != 1 {
		t.Fatalf("a persistent provenance failure must be reported once, not %d times", n)
	}
	if countMessages(m, "legacy") != 1 {
		t.Fatalf("the message must say the board fell back to the legacy gate:\n%v", m.messages)
	}

	// It recovers: the latch clears, so a LATER regression is reported again
	// rather than staying quiet for the rest of the session.
	next, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{{Name: "api", Status: "Running"}}})
	m = next.(model)
	if m.members[0].provenanceWarned {
		t.Fatal("a successful provenance read must re-arm the warning")
	}
	next, _ = m.Update(failed)
	m = next.(model)
	if n := countMessages(m, "provenance read failed"); n != 2 {
		t.Fatalf("a second failure streak should be reported again, got %d reports", n)
	}
}
