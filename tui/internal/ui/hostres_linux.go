package ui

import "golang.org/x/sys/unix"

// hostMemBytes returns the host's total physical RAM in bytes (Linux), or 0 when
// it can't be determined so callers fall back to the static memory cap.
func hostMemBytes() int64 {
	var info unix.Sysinfo_t
	if err := unix.Sysinfo(&info); err != nil {
		return 0
	}
	// Totalram is counted in info.Unit-byte units; widen both before multiplying
	// so 32-bit arches don't overflow.
	return int64(uint64(info.Totalram) * uint64(info.Unit))
}
