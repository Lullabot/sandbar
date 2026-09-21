package lima

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// instanceNameRE is limactl's own instance-name rule, copied from the error it
// prints when it refuses one ("identifier `x` must match ..."), measured against
// limactl 2.2.0. Letters, digits, and SINGLE `.`/`-`/`_` separators that must sit
// between two alphanumerics — so `a_b` is fine, `a--b`, `-a` and `a-` are not.
var instanceNameRE = regexp.MustCompile(`^[A-Za-z0-9]+(?:[._-][A-Za-z0-9]+)*$`)

// MaxInstanceNameLen caps an instance name at the DNS label limit.
//
// Lima's own limit is neither this nor any other fixed number: it rejects a name
// once `<LIMA_HOME>/<name>/ssh.sock.<16 digits>` would exceed UNIX_PATH_MAX (108),
// so the ceiling moves with the length of the Lima home directory and a name that
// fits on one machine fails on another. Refusing at 63 is both stricter than the
// worst realistic Lima home and exactly the limit the name faces anyway once it
// becomes the guest's hostname.
const MaxInstanceNameLen = 63

// ValidateInstanceName reports whether limactl will accept name as an instance
// name, WITHOUT running limactl.
//
// It exists so a bad name is refused while the user is still looking at the field
// they typed it into. The alternative is what sand did before: hand the name to
// the backend, have it refused minutes later mid-create, and leave a failed VM
// that never existed to be cleaned up.
//
// Mirroring limactl's rule rather than imposing a stricter one of sand's own is
// deliberate: every name a Lima VM already carries passed this check, so nothing
// that exists today becomes un-resettable or un-recreatable. Proxmox's rule is
// different (and tighter — no underscores); see pve.ValidateVMName.
func ValidateInstanceName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("name is required")
	}
	if len(name) > MaxInstanceNameLen {
		return fmt.Errorf("name %q is too long (%d characters; the limit is %d)", name, len(name), MaxInstanceNameLen)
	}
	for _, r := range name {
		if !isInstanceNameRune(r) {
			return fmt.Errorf("name %q cannot contain %q — Lima allows only letters, digits, '.', '-' and '_'", name, r)
		}
	}
	if !instanceNameRE.MatchString(name) {
		return fmt.Errorf("name %q must start and end with a letter or digit, with single '.', '-' or '_' separators in between — Lima rejects %q", name, name)
	}
	return nil
}

func isInstanceNameRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '.', r == '-', r == '_':
		return true
	}
	return false
}
