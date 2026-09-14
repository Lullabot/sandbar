//go:build unix

package profiles

import (
	"fmt"
	"os"
)

// checkTokenPerms refuses a token file any group or other can read. This is
// the original, unchanged check: a leaked API token is not a recoverable
// mistake, so the file is refused outright rather than warned about.
func checkTokenPerms(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("proxmox token file: %w", err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("proxmox token file %s has mode %04o; it must not be readable by group or other (chmod 600)", path, mode)
	}
	return nil
}
