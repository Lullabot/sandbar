package provision

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	sandbar "github.com/lullabot/sandbar"
)

// LocatePlaybook finds the playbook directory to mount into the VM. It
// resolves in two tiers:
//
//  1. If run inside a git checkout containing site.yml, return the working
//     tree — matching the original bash provisioner's "repo mode" so
//     uncommitted edits to the playbook take effect.
//  2. Otherwise (no checkout, e.g. a Homebrew-installed binary run outside
//     any repository), extract the playbook fileset embedded in the
//     sandbar package to a fresh private temp dir and return that path.
//
// The temp dir created in tier 2 is intentionally not removed here: the
// caller mounts it read-only at /mnt/playbook and the guest rsyncs from it
// over the course of provisioning, so it must outlive this call. Cleanup is
// left to process exit.
func LocatePlaybook() (string, error) {
	if top, err := gitCheckoutPlaybookDir(); err == nil {
		return top, nil
	}
	return materializeEmbedded(sandbar.PlaybookFS)
}

// gitCheckoutPlaybookDir resolves the current git checkout's toplevel and
// returns it when it contains site.yml.
func gitCheckoutPlaybookDir() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git checkout: %w", err)
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return "", fmt.Errorf("could not determine the git toplevel directory")
	}
	if _, err := os.Stat(filepath.Join(top, "site.yml")); err != nil {
		return "", fmt.Errorf("playbook not found at %s (no site.yml): %w", top, err)
	}
	return top, nil
}

// materializeEmbedded writes every file in fsys to a fresh, private temp
// dir, preserving directory structure and file contents byte-for-byte, and
// returns the temp dir's path. It is the tier-2 fallback for LocatePlaybook
// when no git checkout is available.
func materializeEmbedded(fsys fs.FS) (string, error) {
	dir, err := os.MkdirTemp("", "sand-playbook-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir for embedded playbook: %w", err)
	}

	err = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, path)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return fmt.Errorf("read embedded file %s: %w", path, err)
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		return "", fmt.Errorf("materialise embedded playbook to %s: %w", dir, err)
	}

	return dir, nil
}
