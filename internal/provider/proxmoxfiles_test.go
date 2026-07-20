package provider

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProxmoxStateRootIsPerEndpoint proves two profiles pointing at different
// pools (or nodes, or hosts) get DIFFERENT state roots. They share one laptop
// filesystem, so a collision here would have one endpoint's base version stamp
// and locks silently describing another endpoint's template.
func TestProxmoxStateRootIsPerEndpoint(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	a := proxmoxStateRoot(TargetConfig{Host: "pve.example.com", Node: "pve1", Pool: "sandbar"})
	b := proxmoxStateRoot(TargetConfig{Host: "pve.example.com", Node: "pve1", Pool: "sandbar-test"})
	c := proxmoxStateRoot(TargetConfig{Host: "pve.example.com", Node: "pve2", Pool: "sandbar"})

	if a == b || a == c || b == c {
		t.Fatalf("state roots collided across endpoints: %q %q %q", a, b, c)
	}
	for _, got := range []string{a, b, c} {
		if !strings.Contains(got, filepath.Join("sandbar", "proxmox")) {
			t.Errorf("state root %q should live under sandbar/proxmox", got)
		}
	}
}

// TestProxmoxStateRootSanitizesComponents proves a host carrying a ":port" (or
// any other separator PVE and DNS permit) becomes ONE filesystem-safe directory
// name rather than a nested path — a "/" in a component would otherwise silently
// re-root the whole state tree.
func TestProxmoxStateRootSanitizesComponents(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)

	got := proxmoxStateRoot(TargetConfig{Host: "10.0.0.5:8006", Node: "pve/1", Pool: "a b"})
	rel, err := filepath.Rel(filepath.Join(base, "sandbar", "proxmox"), got)
	if err != nil {
		t.Fatalf("state root %q is not under the proxmox state dir: %v", got, err)
	}
	if strings.ContainsAny(rel, `/\ :`) {
		t.Fatalf("endpoint dir name %q must be a single sanitized component", rel)
	}
}

// TestProxmoxFilesDiskAllocIsUnknown pins the awkward-seam answer: there is no
// local qcow2 to stat, and -1 is the interface's documented "cannot be measured"
// value. Returning 0 instead would render as a VM using no disk at all.
func TestProxmoxFilesDiskAllocIsUnknown(t *testing.T) {
	f := newProxmoxFiles(t.TempDir())
	if got := f.DiskAllocBytes("/anything"); got != -1 {
		t.Fatalf("DiskAllocBytes = %d; want -1 (cannot be measured)", got)
	}
}

// TestProxmoxFilesLimaHomeIsStateRoot proves the state root is what the
// provisioner's stamp/lock/overlay paths hang off.
func TestProxmoxFilesLimaHomeIsStateRoot(t *testing.T) {
	root := t.TempDir()
	if got := newProxmoxFiles(root).LimaHome(); got != root {
		t.Fatalf("LimaHome() = %q; want the state root %q", got, root)
	}
}

// TestProxmoxFilesStagePlaybookIsIdentity proves the playbook is NOT staged
// anywhere: it reaches the guest over SSH during provisioning, not through a
// bind mount, so returning anything but the caller's own directory would point
// the provisioner at a path that does not exist.
func TestProxmoxFilesStagePlaybookIsIdentity(t *testing.T) {
	local := t.TempDir()
	got, err := newProxmoxFiles(t.TempDir()).StagePlaybook(context.Background(), local)
	if err != nil {
		t.Fatalf("StagePlaybook: %v", err)
	}
	if got != local {
		t.Fatalf("StagePlaybook = %q; want its input %q unchanged", got, local)
	}
}

// TestProxmoxFilesMarkersAreEmpty proves the sidecar-marker path is never a
// source of truth for this backend: provenance lives in PVE's own VM metadata,
// and an empty map (not an error) is what keeps a board-wide provenance read
// working rather than failing on a directory that will never hold markers.
func TestProxmoxFilesMarkersAreEmpty(t *testing.T) {
	f := newProxmoxFiles(t.TempDir())
	got, err := f.ReadInstanceMarkers(context.Background(), f.LimaHome(), "sandbar.json")
	if err != nil {
		t.Fatalf("ReadInstanceMarkers: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ReadInstanceMarkers = %v; want an empty map", got)
	}
}

// TestProxmoxFilesLocalRoundTrip proves the read/write/stat half really is the
// local filesystem, including the contract that a missing file reports
// fs.ErrNotExist — every caller of the seam branches on that.
func TestProxmoxFilesLocalRoundTrip(t *testing.T) {
	root := t.TempDir()
	f := newProxmoxFiles(root)
	path := filepath.Join(root, "_sand", "sandbar-base.playbook-version")

	if _, err := f.ReadFile(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadFile of a missing file err = %v; want fs.ErrNotExist", err)
	}
	if _, err := f.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat of a missing path err = %v; want fs.ErrNotExist", err)
	}
	if err := f.WriteFile(path, []byte("v1"), 0o755, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := f.ReadFile(path)
	if err != nil || string(got) != "v1" {
		t.Fatalf("ReadFile = %q, %v; want %q", got, err, "v1")
	}
	if _, err := os.Stat(filepath.Join(root, "_sand")); err != nil {
		t.Fatalf("WriteFile did not create the parent directory: %v", err)
	}

	lock, err := f.OpenLock(filepath.Join(root, "_sand", "base.lock"), 0o644)
	if err != nil {
		t.Fatalf("OpenLock: %v", err)
	}
	defer lock.Close()
	if acquired, err := lock.TryLock(); err != nil || !acquired {
		t.Fatalf("TryLock = %v, %v; want an acquired lock", acquired, err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
}
