//go:build linux || darwin

package lima

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

// DiskAllocBytes returns the allocated on-disk size (st_blocks × 512) of the file
// at path, or -1 when it can't be measured. Allocated blocks — not logical length
// — is the honest "blocks consumed" figure: a qcow2 only allocates the blocks it
// holds, so a 100 GiB-virtual disk holding ~5 GiB reports ~5 GiB. On CoW
// filesystems (APFS, Btrfs/XFS reflinks) it additionally reflects blocks shared
// with a clone source. Returns -1 (not 0) so an unmeasurable file renders a blank
// cell rather than "0 B".
func (localFiles) DiskAllocBytes(path string) int64 {
	// filepath.Clean guards against a path that is empty or has odd separators
	// before it reaches the syscall; unix.Stat on "" would just error to -1 anyway.
	var st unix.Stat_t
	if err := unix.Stat(filepath.Clean(path), &st); err != nil {
		return -1
	}
	return int64(st.Blocks) * 512
}
