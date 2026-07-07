//go:build linux || darwin

package ui

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

// diskUsedBytes returns the allocated on-disk size (st_blocks × 512) of the Lima
// instance's qcow2 image at <dir>/disk, or -1 when it can't be measured (empty
// dir, missing/unreadable disk file). Allocated blocks — not logical length — is
// the honest "blocks consumed" figure: a qcow2 only allocates the blocks it
// holds, so a 100 GiB-virtual disk holding ~5 GiB reports ~5 GiB. On CoW
// filesystems (APFS, Btrfs/XFS reflinks) it additionally reflects blocks shared
// with a clone source. Returns -1 (not 0) so an unmeasurable VM renders a blank
// cell rather than "0 B". Lima 2.x writes a single file named `disk` per
// instance; no fallback for the legacy diffdisk/basedisk layout is provided.
func diskUsedBytes(dir string) int64 {
	if dir == "" {
		return -1
	}
	var st unix.Stat_t
	if err := unix.Stat(filepath.Join(dir, "disk"), &st); err != nil {
		return -1
	}
	return int64(st.Blocks) * 512
}
