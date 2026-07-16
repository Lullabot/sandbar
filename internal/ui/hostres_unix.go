//go:build linux || darwin

package ui

import "golang.org/x/sys/unix"

// hostDiskFreeBytes returns the free space (to an unprivileged user) on the
// filesystem backing path, or 0 when it can't be statted. Linux and macOS share
// the same unix.Statfs call; the Bsize field differs in width between them, so
// both factors are widened to int64 before multiplying.
func hostDiskFreeBytes(path string) int64 {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// hostDiskTotalBytes returns the TOTAL (not free) size of the filesystem
// backing path, or 0 when it can't be statted — hostDiskFreeBytes' companion,
// needed so a host-disk low-capacity warning (hostwarn.go) can compute a
// free% without inventing a denominator.
func hostDiskTotalBytes(path string) int64 {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0
	}
	return int64(st.Blocks) * int64(st.Bsize)
}
