//go:build !linux && !darwin

package ui

// diskUsedBytes has no allocated-block probe on platforms without unix.Stat;
// return -1 so the "Disk Used" cell renders blank there.
func diskUsedBytes(string) int64 { return -1 }
