package provider

// proxmoxfiles.go resolves the one place the Provider interface does not fit a
// non-Lima backend cleanly: HostFiles() names "the host where limactl runs", and
// Proxmox has no such host. The seam is still the right shape, though — every
// caller of it (the base version stamp, the base lock, partial-instance cleanup,
// the overlay read) is reaching for sand's own state ABOUT an instance, not for
// anything Lima-specific. So the answer is not to fake a Lima host: it is to say
// where that state lives when there is no shared filesystem to put it on.

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/lullabot/sandbar/internal/lima"
)

// proxmoxFiles satisfies lima.HostFiles for a backend that has no Lima host.
// Sandbar's own state about an endpoint's instances (version stamps, locks,
// provenance fallbacks) is kept LOCALLY, per endpoint, because there is no
// shared filesystem to put it on and no shell account on the PVE node to reach
// one; Proxmox itself owns the VM state and is reached through the API instead.
//
// It embeds lima.LocalFiles() rather than re-wrapping os.* so the read/write/
// stat/lock half is byte-for-byte the local implementation — same fs.ErrNotExist
// contract, same flock, no second copy to drift. Only the four methods whose
// Lima meaning does not survive the move are overridden below, each for a
// different reason worth reading.
type proxmoxFiles struct {
	lima.HostFiles
	root string
}

// Compile-time proof the embedded-plus-overridden composition really satisfies
// the whole seam — the embedding means a method dropped from the override set
// would silently fall back to the local one rather than fail to build, so this
// only guards the shape, and each override's own doc explains why it is there.
var _ lima.HostFiles = proxmoxFiles{}

// newProxmoxFiles roots a host-access seam at an endpoint's state directory.
func newProxmoxFiles(root string) proxmoxFiles {
	return proxmoxFiles{HostFiles: lima.LocalFiles(), root: root}
}

// LimaHome is the per-endpoint state root. The name is Lima's, but what hangs
// off it here is only ever sand's own bookkeeping — `_sand/<base>.playbook-version`
// and its lock — because a Proxmox VM keeps nothing on this machine. Per
// ENDPOINT, not per user: two profiles pointing at different pools must not
// share a base version stamp, or one endpoint's template would be judged fresh
// against the other's.
func (f proxmoxFiles) LimaHome() string { return f.root }

// DiskAllocBytes reports "cannot be measured" for every path, which is honest
// rather than degraded: there is no local qcow2 to stat because the disk lives
// on the PVE node's storage. -1 is the interface's own documented value for
// this, and the UI's sampling already treats a non-positive result as "no
// reading" and leaves the cell blank. Reporting 0 instead would render as a VM
// consuming no disk at all. Real per-VM figures come from the storage content
// listing in the provider, not from here.
func (f proxmoxFiles) DiskAllocBytes(string) int64 { return -1 }

// StagePlaybook returns localDir unchanged. Local Lima does the same thing for
// the opposite reason — it bind-mounts that very directory — whereas here there
// is nothing to mount at all: the playbook reaches the guest over SSH during
// provisioning. Copying it anywhere would produce a path no one ever reads.
func (f proxmoxFiles) StagePlaybook(_ context.Context, localDir string) (string, error) {
	return localDir, nil
}

// ReadInstanceMarkers reports no markers, always. Provenance for this backend
// lives in PVE's own VM metadata (tags and description), so the sidecar-file
// path is never the source of truth — and the state root is not a directory of
// instance dirs to scan in the first place. An empty map rather than an error
// keeps the board-wide provenance read working, matching the seam's standing
// contract that a missing marker is an absence, never a failure of the batch.
func (f proxmoxFiles) ReadInstanceMarkers(context.Context, string, string) (map[string][]byte, error) {
	return map[string][]byte{}, nil
}

// proxmoxStateRoot is the local state directory for one Proxmox endpoint:
// ${XDG_STATE_HOME:-~/.local/state}/sandbar/proxmox/<host>-<node>-<pool>/.
//
// The three components are what identify a target (matching TargetConfig.Scope's
// identity), so two profiles differing in any one of them get separate state.
// They are sanitized into a SINGLE directory name because each can legitimately
// contain a path separator or a colon — a host is often "10.0.0.5:8006" — and an
// unsanitized component would silently re-root the whole tree somewhere else.
func proxmoxStateRoot(cfg TargetConfig) string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			// No home and no XDG_STATE_HOME: fall back to a temp-dir root
			// rather than "" , which would put the state (and the base lock)
			// at the filesystem root.
			base = filepath.Join(os.TempDir(), "sandbar-state")
		} else {
			base = filepath.Join(home, ".local", "state")
		}
	}
	name := sanitizePathComponent(cfg.Host) + "-" + sanitizePathComponent(cfg.Node) + "-" + sanitizePathComponent(cfg.Pool)
	return filepath.Join(base, "sandbar", "proxmox", name)
}

// sanitizePathComponent reduces s to characters safe in one path component,
// mapping everything else to "-". It is deliberately a whitelist: a blacklist
// would have to anticipate every separator, and ".." alone is enough reason not
// to try.
func sanitizePathComponent(s string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			return r
		default:
			return '-'
		}
	}, s)
	if safe == "" {
		return "unset"
	}
	return safe
}
