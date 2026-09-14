//go:build unix

package drupalorg

import (
	"fmt"
	"os"
)

// checkTokenPerms refuses a PAT file any group or other can read. This is the
// original, unchanged check, moved behind a build tag so Windows can answer
// the same question a different way: a leaked PAT is not a recoverable
// mistake, so the file is refused outright rather than warned about.
//
// It takes the OPEN descriptor, not the path, so the permission check and the
// subsequent read observe the same inode -- see LoadToken's comment.
func checkTokenPerms(f *os.File, path string) error {
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf(errTokenFile, err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("drupal.org token file %s has mode %04o; it must not be readable by group or other (chmod 600)", path, mode)
	}
	return nil
}
