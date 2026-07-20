package lima

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// toleranceFixtureLimaYAML is a minimal RESOLVED instance lima.yaml — no
// unresolved `base:` template reference — the same shape
// configure_strip_test.go's resolvedCloneFixtureTemplate uses (see that
// file's doc comment for why: `limactl edit`/`list` refuse to load an
// instance whose base: is still a template reference, which only happens
// after a base image has actually been started once). It is exactly what a
// real `limactl clone` leaves behind before the clone is ever booted, so
// this fixture proves the guard below against a STOPPED instance with no
// VM boot required.
const toleranceFixtureLimaYAML = `images:
- location: "https://example.invalid/debian-13.qcow2"
  arch: "x86_64"
  digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000"
cpus: 2
memory: "2GiB"
disk: "20GiB"
`

// TestLimactlToleratesProvenanceMarkerAgainstRealLimactl is the standing
// "limactl tolerance" guard: sand writes its
// provenance marker (MarkerFilename, "sandbar.json") directly INSIDE the
// Lima instance directory, alongside lima.yaml and the other files limactl
// itself manages there. Nothing in Lima's own contract promises it will
// tolerate an extra, unrecognised file sitting next to lima.yaml — this is
// the standing guard that a future Lima release refusing to load an
// instance dir with unexpected files (or deleting them on some maintenance
// pass) fails CI instead of silently breaking marker-based provenance for
// every user, exactly the way TestConfigureStripsWritableMountAgainstRealLimactl
// (configure_strip_test.go) guards the clone-strip invariant.
//
// It skips (does not fail) when limactl is not on PATH, so `go test ./...`
// stays green on a machine without Lima installed — and, like that other
// real-limactl guard, needs no VM boot: a stopped instance with a RESOLVED
// lima.yaml is enough for `limactl list`/`list <name>` to enumerate and
// parse it, so this test runs in the DEFAULT suite whenever limactl is
// present (this dev box has 2.1.3), not gated behind //go:build limae2e.
func TestLimactlToleratesProvenanceMarkerAgainstRealLimactl(t *testing.T) {
	if _, err := exec.LookPath("limactl"); err != nil {
		t.Skip("limactl not on PATH; skipping the real-limactl tolerance guard")
	}

	limaHome := t.TempDir()
	t.Setenv("LIMA_HOME", limaHome)
	const name = "sand-tolerance-test"
	instDir := filepath.Join(limaHome, name)
	if err := os.MkdirAll(instDir, 0o700); err != nil {
		t.Fatalf("mkdir instance dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(instDir, "lima.yaml"), []byte(toleranceFixtureLimaYAML), 0o644); err != nil {
		t.Fatalf("write fixture lima.yaml: %v", err)
	}
	markerContent := []byte(`{"schema":1,"base":"sandbar-base"}`)
	markerPath := filepath.Join(instDir, MarkerFilename)
	if err := os.WriteFile(markerPath, markerContent, 0o600); err != nil {
		t.Fatalf("write provenance marker: %v", err)
	}

	c := New(NewExecRunner())

	// `limactl list --format json` must still succeed and enumerate the
	// instance with a marker sitting in its directory.
	vms, err := c.List()
	if err != nil {
		t.Fatalf("limactl list --format json (marker present in instance dir): %v", err)
	}
	found := false
	for _, v := range vms {
		if v.Name == name {
			found = true
			if v.Status == "" {
				t.Errorf("List() found %s but with no status", name)
			}
		}
	}
	if !found {
		t.Fatalf("List() did not enumerate %s (with a provenance marker present in its instance dir): %+v", name, vms)
	}

	// `limactl list <name>` must also succeed and parse the SAME instance.
	got, err := c.Get(name)
	if err != nil {
		t.Fatalf("limactl list %s --format json (marker present): %v", name, err)
	}
	if got.Name != name {
		t.Fatalf("Get(%s) = %+v, want Name=%s", name, got, name)
	}

	// And the marker itself must have survived byte-for-byte — limactl's own
	// enumeration must not have deleted, truncated, or otherwise mangled it.
	after, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read back provenance marker after limactl list: %v", err)
	}
	if string(after) != string(markerContent) {
		t.Fatalf("provenance marker changed after limactl list: got %q, want %q", after, markerContent)
	}
}
