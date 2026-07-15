//go:build !linux && !darwin

package lima

// DiskAllocBytes has no allocated-block probe on platforms without unix.Stat;
// return -1 so the disk-used cell renders blank there.
func (localFiles) DiskAllocBytes(string) int64 { return -1 }
