//go:build linux || darwin

package ui

import "testing"

// hostDiskTotalBytes is hostDiskFreeBytes' total-side companion (hostres_unix.go):
// the denominator a host-disk low-capacity warning (hostwarn.go) needs beside the
// free reading that already existed for the header. Both are one unix.Statfs
// call on the same path, so total must never be smaller than free — that would
// mean more space is "available" than the filesystem itself claims to hold.
func TestHostDiskTotalBytesAtLeastFree(t *testing.T) {
	dir := t.TempDir()
	total := hostDiskTotalBytes(dir)
	free := hostDiskFreeBytes(dir)
	if total <= 0 {
		t.Fatalf("hostDiskTotalBytes(%q) = %d, want > 0 for a real mounted dir", dir, total)
	}
	if total < free {
		t.Fatalf("hostDiskTotalBytes = %d must be >= hostDiskFreeBytes = %d", total, free)
	}
}

// An unstattable path degrades to 0, exactly like hostDiskFreeBytes — a
// warning check must see "unknown", never a fabricated denominator.
func TestHostDiskTotalBytesUnstattablePathIsZero(t *testing.T) {
	if got := hostDiskTotalBytes("/nonexistent/definitely-not-a-real-path-xyz"); got != 0 {
		t.Fatalf("hostDiskTotalBytes on an unstattable path = %d, want 0", got)
	}
}
