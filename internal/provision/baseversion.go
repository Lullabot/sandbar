package provision

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A base image bakes in a snapshot of the playbook (rsynced into /root/playbook
// at build time) and is then reused as a clone source indefinitely. Without a
// staleness check, a playbook update never reaches new VMs until someone deletes
// the base by hand. To close that gap we stamp each base with the playbook
// version it was built from, and rebuild when the current playbook differs.
//
// The version is a content hash of the playbook fileset combined with the
// tool-set selection, not the git checkout's HEAD. A git-HEAD scheme is inert
// for a released/Homebrew binary: outside a checkout there is no HEAD to read,
// the lookup errors, and (per baseStale in provision.go) an error is treated as
// "not stale" so a non-git install never rebuilds and never even writes a
// stamp. Hashing content instead works identically whether the playbook came
// from a git working tree or the fileset embedded in the binary (see
// provision.LocatePlaybook), so this cannot fail for "not a git checkout" the
// way the old scheme did.
//
// The stamp lives host-side (keyed by base name) so the check is a cheap file
// read — no need to boot the base to inspect it.

// playbookVersionFn, readBaseVersionFn and writeBaseVersionFn are indirected
// through package vars so tests can stub the filesystem side effects.
var (
	playbookVersionFn  = contentPlaybookVersion
	readBaseVersionFn  = readBaseVersion
	writeBaseVersionFn = writeBaseVersion
)

// playbookVersionPrefix marks a stamp as produced by the content-hash scheme.
// baseStale treats any stamp lacking this prefix — including every stamp the
// old git-HEAD scheme ever wrote — as stale, so an upgrading user converges
// onto the new scheme once rather than silently trusting a base a different
// versioning scheme vouched for.
const playbookVersionPrefix = "v2:"

// toolsetPlaceholder stands in for the real tool-set selection until task 5
// wires vm.CreateConfig.ToolsetKey() through. It names the default,
// everything-on selection so the stamp does not change again when the real
// value is threaded in for that default case.
const toolsetPlaceholder = "ddev+go+java"

// playbookFileset lists the top-level entries that constitute the playbook —
// the fs.FS spelling of the go:embed directives in playbook_embed.go and the
// rsync filter in provision.go's inGuestScript. TestGuestSyncCopiesOnlyThePlaybook
// already pins those two together, so it now guards this hash too: change one,
// change all three.
var playbookFileset = map[string]bool{
	"site.yml":    true,
	"ansible.cfg": true,
	"inventory":   true,
	"roles":       true,
	"group_vars":  true,
}

// playbookContentHash hashes exactly the fileset that reaches the guest,
// filtering fsys down to playbookFileset first so extraneous entries (e.g. a
// working-tree checkout's .git, go sources, or agent tooling) never perturb
// the result. Paths are walked in sorted order and each entry is hashed as
// path, then length, then content, so a rename (same bytes, different path)
// is detected rather than cancelling out.
func playbookContentHash(fsys fs.FS) (string, error) {
	var paths []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		top := p
		if i := strings.IndexByte(p, '/'); i >= 0 {
			top = p[:i]
		}
		if !playbookFileset[top] {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, p := range paths {
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\n%d\n", p, len(b)) // path + length frame the content
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// PlaybookVersion is the base image's version stamp: a content hash of the
// playbook fileset in fsys, combined with a canonical rendering of the
// tool-set selection so changing the selection also invalidates the base.
func PlaybookVersion(fsys fs.FS, toolset string) (string, error) {
	h, err := playbookContentHash(fsys)
	if err != nil {
		return "", err
	}
	return playbookVersionPrefix + h + ":" + toolset, nil
}

// contentPlaybookVersion computes the base version stamp for the playbook
// rooted at dir — the resolved working tree or an extracted copy of the
// embedded fileset (see provision.LocatePlaybook, which always resolves to a
// real directory on disk either way). Unlike the old git-HEAD scheme, this
// does not fail merely because dir is not a git checkout: os.DirFS never
// errors up front, so a released/Homebrew binary stamps and rebuilds exactly
// like a build run from a checkout does.
func contentPlaybookVersion(dir string) (string, error) {
	return PlaybookVersion(os.DirFS(dir), toolsetPlaceholder)
}

// baseVersionPath is the host file recording which playbook version a base
// image was built from. It sits under the Lima home so it lives beside the
// base it describes, namespaced in a subdir to avoid colliding with Lima's own
// state.
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

// shortVersion trims a stamp for human-readable log lines: full stamps are a
// "v2:" prefix, a 64-hex-char SHA-256, and a toolset suffix, more than a
// status line needs.
func shortVersion(v string) string {
	const maxLen = 40
	if len(v) > maxLen {
		return v[:maxLen] + "…"
	}
	return v
}
