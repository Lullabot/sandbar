package pve

import (
	"context"
	"net/http"
	"os"
	"testing"
)

// serveFixture returns a handler that always responds with the named
// testdata file wrapped in the {"data": ...} envelope every PVE endpoint
// uses.
func serveFixture(t *testing.T, name string) http.HandlerFunc {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading testdata/%s: %v", name, err)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":` + string(body) + `}`))
	}
}

func TestNodeStatusDecodesCPUMemoryAndCPUInfo(t *testing.T) {
	c := newTestClient(t, serveFixture(t, "node-status.json"))

	got, err := c.NodeStatus(context.Background())
	if err != nil {
		t.Fatalf("NodeStatus: %v", err)
	}

	if got.CPUInfo.CPUs != 8 {
		t.Errorf("CPUInfo.CPUs = %d; want 8", got.CPUInfo.CPUs)
	}
	if got.Memory.Total != 16000000000 {
		t.Errorf("Memory.Total = %d; want 16000000000", got.Memory.Total)
	}
	if got.Memory.Used != 12000000000 {
		t.Errorf("Memory.Used = %d; want 12000000000", got.Memory.Used)
	}
	if got.Memory.Available != 3000000000 {
		t.Errorf("Memory.Available = %d; want 3000000000", got.Memory.Available)
	}
}

// TestNodeStatusMemoryHeadroomIsAvailableNotTotalMinusUsed asserts the core
// non-obvious semantic of this endpoint: Proxmox computes
// memused = memtotal - memavailable, so Used + Free != Total and
// Total - Used is NOT the honest free-memory figure. The fixture's
// total-used (4000000000) deliberately differs from available (3000000000)
// so a caller who "simplifies" the math is caught by this test.
func TestNodeStatusMemoryHeadroomIsAvailableNotTotalMinusUsed(t *testing.T) {
	c := newTestClient(t, serveFixture(t, "node-status.json"))

	got, err := c.NodeStatus(context.Background())
	if err != nil {
		t.Fatalf("NodeStatus: %v", err)
	}

	namingTotalMinusUsed := got.Memory.Total - got.Memory.Used
	if namingTotalMinusUsed == got.Memory.Available {
		t.Fatalf("fixture is not a valid regression guard: total-used (%d) equals available (%d)",
			namingTotalMinusUsed, got.Memory.Available)
	}
	const wantHeadroom = 3000000000
	if got.Memory.Available != wantHeadroom {
		t.Errorf("Memory.Available = %d; want %d (the honest headroom figure, NOT total-used = %d)",
			got.Memory.Available, wantHeadroom, namingTotalMinusUsed)
	}
}

func TestNodeStatusLoadAvgDecodesAsStringArray(t *testing.T) {
	c := newTestClient(t, serveFixture(t, "node-status.json"))

	got, err := c.NodeStatus(context.Background())
	if err != nil {
		t.Fatalf("NodeStatus: %v", err)
	}

	want := []string{"0.15", "0.22", "0.18"}
	if len(got.LoadAvg) != len(want) {
		t.Fatalf("LoadAvg = %v; want %v", got.LoadAvg, want)
	}
	for i := range want {
		if got.LoadAvg[i] != want[i] {
			t.Errorf("LoadAvg[%d] = %q; want %q", i, got.LoadAvg[i], want[i])
		}
	}
}

// TestNodeStatusToleratesMissingDiskFields covers a response that omits
// disk/maxdisk entirely (they are absent from the published schema but
// present in practice on some PVE versions) — the decode must still succeed.
func TestNodeStatusToleratesMissingDiskFields(t *testing.T) {
	c := newTestClient(t, serveFixture(t, "node-status.json"))

	got, err := c.NodeStatus(context.Background())
	if err != nil {
		t.Fatalf("NodeStatus: %v", err)
	}
	if got.Disk != nil {
		t.Errorf("Disk = %v; want nil for a response that omits the field", got.Disk)
	}
	if got.MaxDisk != nil {
		t.Errorf("MaxDisk = %v; want nil for a response that omits the field", got.MaxDisk)
	}
}

func TestStorageStatusDecodesSizeAndFraction(t *testing.T) {
	c := newTestClient(t, serveFixture(t, "storage-status.json"))

	got, err := c.StorageStatus(context.Background(), "local-zfs")
	if err != nil {
		t.Fatalf("StorageStatus: %v", err)
	}

	if got.Total != 500000000000 {
		t.Errorf("Total = %d; want 500000000000", got.Total)
	}
	if got.Used != 210000000000 {
		t.Errorf("Used = %d; want 210000000000", got.Used)
	}
	if got.Avail != 290000000000 {
		t.Errorf("Avail = %d; want 290000000000", got.Avail)
	}
	if got.UsedFraction != 0.42 {
		t.Errorf("UsedFraction = %v; want 0.42", got.UsedFraction)
	}
	if !got.HasSizeReading() {
		t.Errorf("HasSizeReading() = false; want true for an active storage with a positive Total")
	}
	if !got.SupportsContent("images") {
		t.Errorf("SupportsContent(%q) = false; want true", "images")
	}
	if got.SupportsContent("backup") {
		t.Errorf("SupportsContent(%q) = true; want false", "backup")
	}
}

// TestStorageStatusInactiveOmitsSizeFieldsWithoutFailingDecode covers PVE's
// enabled-but-unreachable case: enabled:1, active:0, and every size field
// omitted from the body. The decode must succeed and HasSizeReading must
// report false so callers treat this as "unknown", never as a false
// "0 bytes free" that would trip a low-disk warning.
func TestStorageStatusInactiveOmitsSizeFieldsWithoutFailingDecode(t *testing.T) {
	c := newTestClient(t, serveFixture(t, "storage-inactive.json"))

	got, err := c.StorageStatus(context.Background(), "nfs-backup")
	if err != nil {
		t.Fatalf("StorageStatus: %v", err)
	}

	if got.Active != 0 {
		t.Errorf("Active = %d; want 0", got.Active)
	}
	if got.Enabled != 1 {
		t.Errorf("Enabled = %d; want 1", got.Enabled)
	}
	if got.Total != 0 {
		t.Errorf("Total = %d; want 0 (omitted from response)", got.Total)
	}
	if got.HasSizeReading() {
		t.Errorf("HasSizeReading() = true; want false for an unreachable storage — a false 0-bytes-free reading must never be reported")
	}
}
