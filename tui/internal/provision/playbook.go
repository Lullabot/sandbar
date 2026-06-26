package provision

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LocatePlaybook finds the playbook directory to mount into the VM. It resolves
// the current git checkout's toplevel and returns it when it contains site.yml,
// matching new-vm.sh's "repo mode": the working tree provisions the VM, so
// uncommitted edits to the playbook take effect.
//
// TODO: standalone cache-clone mode — when not run from a checkout, new-vm.sh
// clones REPO_URL into the XDG data dir, pins to the newest release tag, and
// mounts that. Port that fallback here when the TUI needs to run outside a
// checkout.
func LocatePlaybook() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git checkout: run the TUI from a clone of the playbook repository: %w", err)
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return "", fmt.Errorf("could not determine the git toplevel directory")
	}
	if _, err := os.Stat(filepath.Join(top, "site.yml")); err != nil {
		return "", fmt.Errorf("playbook not found at %s (no site.yml); run the TUI from a checkout of the playbook repository", top)
	}
	return top, nil
}
