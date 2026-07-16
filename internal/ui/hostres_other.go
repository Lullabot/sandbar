//go:build !linux && !darwin

package ui

// Fallbacks for platforms without a RAM/disk probe: returning 0 makes the
// memory default fall back to the static cap and suppresses the disk warning.

func hostMemBytes() int64 { return 0 }

func hostDiskFreeBytes(string) int64 { return 0 }

func hostDiskTotalBytes(string) int64 { return 0 }
