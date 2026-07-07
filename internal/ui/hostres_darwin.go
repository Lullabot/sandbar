package ui

import "golang.org/x/sys/unix"

// hostMemBytes returns the host's total physical RAM in bytes (macOS), or 0 when
// it can't be determined so callers fall back to the static memory cap.
func hostMemBytes() int64 {
	n, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return int64(n)
}
