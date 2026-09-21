package pve

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// vmNameRE is PVE's `dns-name` format — the type its API declares for a VM's
// `name` parameter — transcribed from pve-common's verify_dns_name: dot-separated
// labels, each starting and ending with an alphanumeric and carrying hyphens in
// between. Notably it admits `a--b` (which Lima refuses) and excludes `_` (which
// Lima allows), so the two backends' rules are neither the same nor nested.
var vmNameRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)*$`)

// MaxVMNameLen caps a VM name at the DNS label limit. PVE's own dns-name check
// imposes no length limit, but the name is a hostname in every practical sense
// (it is what the guest is reached by) and a label over 63 characters is not one.
const MaxVMNameLen = 63

// ValidateVMName reports whether PVE will accept name for a VM's `name`
// parameter, WITHOUT calling the API.
//
// Without it the rejection arrives from the far side of the wire, as a clone task
// failing with `400 Parameter verification failed. (name: invalid format - value
// does not look like a valid DNS name)` — after sand has already built a tile for
// the VM, and followed by a cleanup delete that fails in its turn because the
// instance was never created. Checking here turns all of that into one sentence
// under the field the user typed.
func ValidateVMName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("name is required")
	}
	if len(name) > MaxVMNameLen {
		return fmt.Errorf("name %q is too long (%d characters; the limit is %d)", name, len(name), MaxVMNameLen)
	}
	for _, r := range name {
		if !isVMNameRune(r) {
			return fmt.Errorf("name %q cannot contain %q — Proxmox requires a DNS name, so only letters, digits, '-' and '.' are allowed", name, r)
		}
	}
	if !vmNameRE.MatchString(name) {
		return fmt.Errorf("name %q must start and end with a letter or digit, with '-' only in between — Proxmox requires a DNS name", name)
	}
	return nil
}

func isVMNameRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '.', r == '-':
		return true
	}
	return false
}
