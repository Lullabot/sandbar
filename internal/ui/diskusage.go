package ui

import (
	"path/filepath"

	"github.com/lullabot/sandbar/internal/lima"
)

// diskUsedBytes returns the allocated on-disk size of the Lima instance's qcow2
// image at <dir>/disk, or -1 when it can't be measured (empty dir, missing or
// unreadable disk file). Lima 2.x writes a single file named `disk` per instance;
// no fallback for the legacy diffdisk/basedisk layout is provided.
//
// The host-access seam is passed in — NOT a process-global — because each fleet
// member samples on the host its VMs actually run on (refreshCmd hands it the
// owning profile's hostFiles), so a local VM and a remote VM are measured on
// their own hosts in the same refresh. The old ui.hostFiles global + SetHostFiles
// setter were retired with the fleet: one sand process no longer runs exactly one
// provider. The allocated-block probe itself is platform-specific and lives in the
// seam (lima.HostFiles.DiskAllocBytes); here we only join the instance-relative
// path and guard the empty-dir case.
func diskUsedBytes(hf lima.HostFiles, dir string) int64 {
	if dir == "" {
		return -1
	}
	return hf.DiskAllocBytes(filepath.Join(dir, "disk"))
}
