package ui

import (
	"path/filepath"

	"github.com/lullabot/sandbar/internal/lima"
)

// hostFiles is the host-access seam the tile's per-VM sampling reads through —
// the qcow2's allocated size (diskUsedBytes) and the boot / last-used mtimes
// (upSince, lastUsed in tile.go). It defaults to the local filesystem; a remote
// provider redirects it via SetHostFiles so a remote VM's instance files are
// sampled on the host where the VM actually runs rather than stat'd on the laptop.
var hostFiles lima.HostFiles = lima.LocalFiles()

// SetHostFiles points the tile-sampling seam at hf. cmd/sand/main.go calls it once
// with the resolved provider's host-access seam (provision.HostFiles) so the disk
// gauge and up-since / last-used read the REMOTE host under a remote provider —
// otherwise they stat the remote instance dir on the local filesystem, find
// nothing, and the disk gauge renders "?". A process-global swap for the same
// reason provision.SetHostFiles is: one sand process runs exactly one provider.
func SetHostFiles(hf lima.HostFiles) { hostFiles = hf }

// diskUsedBytes returns the allocated on-disk size of the Lima instance's qcow2
// image at <dir>/disk, or -1 when it can't be measured (empty dir, missing or
// unreadable disk file). Lima 2.x writes a single file named `disk` per instance;
// no fallback for the legacy diffdisk/basedisk layout is provided. The
// allocated-block probe itself is platform-specific and lives in the host-access
// seam (lima.HostFiles.DiskAllocBytes); here we only join the instance-relative
// path and guard the empty-dir case.
func diskUsedBytes(dir string) int64 {
	if dir == "" {
		return -1
	}
	return hostFiles.DiskAllocBytes(filepath.Join(dir, "disk"))
}
