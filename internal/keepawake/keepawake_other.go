//go:build !darwin && !linux && !windows

package keepawake

import (
	"fmt"
	"runtime"
)

// acquireOS has no backend here; Hold reports this and returns a no-op.
func acquireOS(string) (func() error, error) {
	return nil, fmt.Errorf("keepawake: not supported on %s", runtime.GOOS)
}
