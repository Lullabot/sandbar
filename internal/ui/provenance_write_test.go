package ui

import (
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
