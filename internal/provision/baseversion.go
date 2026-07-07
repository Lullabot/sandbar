package provision

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// A base image bakes in a snapshot of the playbook (rsynced into /root/playbook
// at build time) and is then reused as a clone source indefinitely. Without a
// staleness check, a playbook update never reaches new VMs until someone deletes
// the base by hand. To close that gap we stamp each base with the playbook
// version it was built from, and rebuild when the current playbook differs.
//
// The version is the playbook checkout's git HEAD, suffixed "-dirty" when the
// working tree has uncommitted changes so local edits always force a rebuild.
// The stamp lives host-side (keyed by base name) so the check is a cheap file
// read — no need to boot the base to interrogate it.

// playbookVersionFn, readBaseVersionFn and writeBaseVersionFn are indirected
// through package vars so tests can stub the git/filesystem side effects.
var (
	playbookVersionFn  = gitPlaybookVersion
	readBaseVersionFn  = readBaseVersion
	writeBaseVersionFn = writeBaseVersion
)

// gitPlaybookVersion identifies the playbook content at dir by its git HEAD,
// suffixed "-dirty" when the working tree has uncommitted changes. It returns an
// error when dir is not a git checkout (the caller then leaves any existing base
// untouched rather than rebuild-looping).
func gitPlaybookVersion(dir string) (string, error) {
	head, err := runGit(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("determine playbook version at %s: %w", dir, err)
	}
	v := strings.TrimSpace(head)
	if v == "" {
		return "", fmt.Errorf("empty git HEAD for playbook at %s", dir)
	}
	status, err := runGit(dir, "status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("check playbook working tree at %s: %w", dir, err)
	}
	if strings.TrimSpace(status) != "" {
		v += "-dirty"
	}
	return v, nil
}

func runGit(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return string(out), err
}

// baseVersionPath is the host file recording which playbook version a base image
// was built from. It sits under the Lima home so it lives beside the base it
// describes, namespaced in a subdir to avoid colliding with Lima's own state.
func baseVersionPath(baseName string) string {
	home := os.Getenv("LIMA_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".lima")
		}
	}
	return filepath.Join(home, "_sand", baseName+".playbook-version")
}

// readBaseVersion returns the stamped playbook version for a base image, or ""
// when no stamp exists (a base built before stamping, or by an unknown path) —
// which the caller treats as stale so it is rebuilt once.
func readBaseVersion(baseName string) string {
	b, err := os.ReadFile(baseVersionPath(baseName))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// writeBaseVersion records the playbook version a freshly built base was made
// from. A write failure is non-fatal to the build: a missing stamp just forces a
// rebuild on the next create.
func writeBaseVersion(baseName, version string) error {
	path := baseVersionPath(baseName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(version+"\n"), 0o644)
}

// shortVersion trims a commit-hash version for human-readable log lines while
// keeping the "-dirty" suffix visible.
func shortVersion(v string) string {
	dirty := ""
	if strings.HasSuffix(v, "-dirty") {
		dirty = "-dirty"
		v = strings.TrimSuffix(v, "-dirty")
	}
	if len(v) > 12 {
		v = v[:12]
	}
	return v + dirty
}
