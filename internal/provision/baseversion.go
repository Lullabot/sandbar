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
	"time"
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

// playbookVersionFn, readBaseVersionFn, writeBaseVersionFn and
// readBaseBuiltAtFn are indirected through package vars so tests can stub the
// filesystem side effects.
var (
	playbookVersionFn  = contentPlaybookVersion
	readBaseVersionFn  = readBaseVersion
	writeBaseVersionFn = writeBaseVersion
	readBaseBuiltAtFn  = readBaseBuiltAt
)

// playbookVersionPrefix marks a stamp as produced by the content-hash scheme.
// baseStale treats any stamp lacking this prefix — including every stamp the
// old git-HEAD scheme ever wrote — as stale, so an upgrading user converges
// onto the new scheme once rather than silently trusting a base a different
// versioning scheme vouched for.
const playbookVersionPrefix = "v2:"

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
// real directory on disk either way) — combined with toolset, the requesting
// create's CreateConfig.ToolsetKey(). Unlike the old git-HEAD scheme, this
// does not fail merely because dir is not a git checkout: os.DirFS never
// errors up front, so a released/Homebrew binary stamps and rebuilds exactly
// like a build run from a checkout does.
func contentPlaybookVersion(dir, toolset string) (string, error) {
	return PlaybookVersion(os.DirFS(dir), toolset)
}

// baseVersionPath is the host file recording which playbook version a base
// image was built from. It sits under the Lima home (hostFiles.LimaHome — both
// Lima's own per-instance state and sand's state ABOUT an instance live under it)
// so it lives beside the base it describes, namespaced in a _sand subdir to avoid
// colliding with Lima's own state.
func baseVersionPath(baseName string) string {
	return filepath.Join(hostFiles.LimaHome(), "_sand", baseName+".playbook-version")
}

// baseStamp is a base image's on-disk stamp: the content-hash version (task 4)
// on line 1, and the moment it was written (BuiltAt, RFC3339) on line 2 — the
// signal ensureBaseStopped's age check (baseMaxAge) compares against so a base
// is refreshed with a single in-place `apt upgrade` at most once every 30 days,
// instead of every clone re-paying for it in the finalize phase.
type baseStamp struct {
	Version string
	BuiltAt time.Time
}

// parseBaseStamp parses a stamp file's raw content. ok is true only when BOTH
// lines are present and BuiltAt parses as RFC3339 — an older stamp (written
// before this task, or by the pre-v2 git-HEAD scheme) has no usable line 2, and
// a corrupt one does not parse as RFC3339 either way. Both cases return
// ok=false: NEVER GUESS a build time. Callers that need "is this base older
// than N days" treat ok=false as "cannot prove freshness", the same posture
// baseStale already takes for an unparseable/missing version.
func parseBaseStamp(data []byte) (baseStamp, bool) {
	lines := strings.SplitN(string(data), "\n", 2)
	version := strings.TrimSpace(lines[0])
	if version == "" {
		return baseStamp{}, false
	}
	if len(lines) < 2 {
		return baseStamp{Version: version}, false // pre-timestamp stamp: version only
	}
	builtAt, err := time.Parse(time.RFC3339, strings.TrimSpace(lines[1]))
	if err != nil {
		return baseStamp{Version: version}, false
	}
	return baseStamp{Version: version, BuiltAt: builtAt}, true
}

// readBaseVersion returns the stamped playbook version for a base image, or ""
// when no stamp exists (a base built before stamping, or by an unknown path) —
// which the caller treats as stale so it is rebuilt once. It reads only line 1
// (the version); a missing or unparseable BuiltAt (line 2) has no bearing on
// this — readBaseBuiltAt is the seam for that.
func readBaseVersion(baseName string) string {
	b, err := hostFiles.ReadFile(baseVersionPath(baseName))
	if err != nil {
		return ""
	}
	stamp, _ := parseBaseStamp(b)
	return stamp.Version
}

// readBaseBuiltAt returns the BUILT-AT timestamp recorded in a base's stamp, or
// ok=false when the stamp is missing, unreadable, or does not carry a usable
// timestamp (an older stamp written before this task, or a corrupt one). The
// caller (ensureBaseStopped's age check) reads ok=false as "cannot prove this
// base is fresh" and refreshes it once rather than assuming it is new.
func readBaseBuiltAt(baseName string) (time.Time, bool) {
	b, err := hostFiles.ReadFile(baseVersionPath(baseName))
	if err != nil {
		return time.Time{}, false
	}
	stamp, ok := parseBaseStamp(b)
	if !ok {
		return time.Time{}, false
	}
	return stamp.BuiltAt, true
}

// writeBaseVersion records the playbook version a freshly built, re-applied, or
// refreshed base was made from, together with builtAt — the moment its PACKAGES
// were last known current, which is the clock baseNeedsRefresh measures the
// 30-day apt-upgrade age against.
//
// builtAt is a parameter rather than an internal time.Now() precisely because
// those two things are not the same event. A content-only re-apply (a playbook
// edit, aptUpgrade=false) updates what the base CONTAINS without upgrading a
// single package, so stamping "now" for it would silently restart the apt clock.
// Anyone editing the playbook more often than every 30 days would then reset the
// clock on every create, baseNeedsRefresh would never fire, and — since finalize
// no longer runs apt upgrade — no VM would ever receive a security update again.
// reapplyBase therefore carries the PRIOR builtAt forward on a content-only run.
//
// A write failure is non-fatal to the build: a missing/stale stamp just forces
// a rebuild or refresh on the next create.
func writeBaseVersion(baseName, version string, builtAt time.Time) error {
	content := version + "\n" + builtAt.UTC().Format(time.RFC3339) + "\n"
	return hostFiles.WriteFile(baseVersionPath(baseName), []byte(content), 0o755, 0o644)
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

// Ansible can converge an ADDITION to the tool-set (install the newly
// selected package) but not a REMOVAL: it will not uninstall a package whose
// task no longer applies to it. So when a stale base is converged IN PLACE
// (reapplyBase) rather than rebuilt from scratch, a shrinking selection
// leaves the de-selected tool's package installed on the base — residue that
// persists into every future clone until the base is rebuilt. The functions
// below detect that case so the caller can warn about it instead of silently
// leaving stale software installed.

// parseToolset is the inverse of vm.CreateConfig.ToolsetKey(): it turns a
// canonical toolset string back into the set of tools it enables. Both ""
// and "none" parse to the empty set — "" covers a stamp that carries no
// toolset suffix at all (older scheme, or no stamp).
func parseToolset(s string) map[string]bool {
	out := make(map[string]bool)
	if s == "" || s == "none" {
		return out
	}
	for _, tool := range strings.Split(s, "+") {
		out[tool] = true
	}
	return out
}

// toolsetFromStamp extracts the toolset suffix a v2-scheme stamp carries
// ("v2:<hash>:<toolset>"). It returns "" for anything that isn't a
// recognizable v2 stamp with a toolset suffix — an older-scheme stamp, an
// empty/missing one, or a malformed one — which parseToolset then reads as
// "no toolset information", not as an empty selection.
func toolsetFromStamp(stamp string) string {
	if !strings.HasPrefix(stamp, playbookVersionPrefix) {
		return ""
	}
	rest := strings.TrimPrefix(stamp, playbookVersionPrefix)
	i := strings.Index(rest, ":") // the hash is fixed-length hex; it never contains ':'
	if i < 0 {
		return ""
	}
	return rest[i+1:]
}

// shrunk reports which tools are enabled in stamped but not in want — the
// set a re-apply-in-place cannot converge away. Sorted for a stable, readable
// advisory message.
func shrunk(stamped, want map[string]bool) []string {
	var lost []string
	for tool, on := range stamped {
		if on && !want[tool] {
			lost = append(lost, tool)
		}
	}
	sort.Strings(lost)
	return lost
}

// shrunkTools compares a base's stamped version (haveStamp) to the toolset a
// new create is requesting (want, from CreateConfig.ToolsetKey()) and returns
// the tools that would be de-selected by reapplying want to it. Empty when
// haveStamp carries no toolset suffix (older scheme / no stamp) — there is
// nothing to compare against, and the stale-base machinery already handles
// that case by converging or rebuilding regardless.
func shrunkTools(haveStamp, want string) []string {
	stamped := parseToolset(toolsetFromStamp(haveStamp))
	if len(stamped) == 0 {
		return nil
	}
	return shrunk(stamped, parseToolset(want))
}

// BaseToolset reports the tool-set the existing base image was actually BUILT
// with, read back out of its version stamp, as the set of enabled tool names
// (the same names vm.CreateConfig.ToolsetKey renders).
//
// It exists so a create does not have to guess. The tool-set is a property of
// the SHARED base, but it is chosen on a per-VM screen whose fields default to
// the all-on DefaultCreateConfig — so a user who built a base with no tools was
// shown four ticked boxes on the next create, and had to un-tick them again
// every time or silently re-converge the base back to the full tool-set. The
// base already records what it contains; the create path just has to read it.
//
// ok=false means "no toolset information": no stamp (no base built yet), or one
// written by an older sand whose scheme carried no toolset suffix. Callers fall
// back to their own default (all on) in that case. A base built with NOTHING
// selected stamps "none", which parses to an empty set with ok=TRUE — an empty
// selection is a real answer and must not be confused with an absent one.
func BaseToolset(baseName string) (map[string]bool, bool) {
	suffix := toolsetFromStamp(readBaseVersionFn(baseName))
	if suffix == "" {
		return nil, false
	}
	return parseToolset(suffix), true
}

// hashFromStamp extracts the playbook content hash from a v2 stamp
// ("v2:<hash>:<toolset>"), or "" for anything that is not one.
func hashFromStamp(stamp string) string {
	if !strings.HasPrefix(stamp, playbookVersionPrefix) {
		return ""
	}
	rest := strings.TrimPrefix(stamp, playbookVersionPrefix)
	if i := strings.Index(rest, ":"); i >= 0 {
		return rest[:i]
	}
	return rest
}

// toolsetKey renders a tool set back into the canonical string
// vm.CreateConfig.ToolsetKey() produces: the enabled tools joined by "+" in
// sorted order, or "none" when the set is empty. It is the inverse of
// parseToolset, and must agree with vm.CreateConfig.ToolsetKey() exactly or a
// base would be perpetually stale against its own stamp.
func toolsetKey(set map[string]bool) string {
	var on []string
	for tool, enabled := range set {
		if enabled {
			on = append(on, tool)
		}
	}
	if len(on) == 0 {
		return "none"
	}
	sort.Strings(on)
	return strings.Join(on, "+")
}

// mergeToolsetVersion returns the stamp a converged-in-place base will
// TRUTHFULLY carry: the current playbook hash (from want), with the UNION of the
// tools the base is already stamped with (have) and the tools this create asks
// for.
//
// The union — rather than simply want's toolset — is what makes a single shared
// base stable. There is exactly one base per user, and a converge-in-place can
// only ADD: Ansible will not uninstall a package whose task no longer applies
// (see shrunkTools above). Stamping only the requested set would therefore claim
// the base had LOST tools it demonstrably still has, so the next create with the
// other selection would see a mismatch, converge again, and stamp it back the
// other way — an unbreakable ping-pong in which alternating `--with-go=false`
// and default creates each pay a multi-minute re-apply, forever, while still
// getting Go anyway. Recording what the base actually CONTAINS ends that: a
// de-selected tool stays installed (ensureBaseStopped warns, and only a
// --rebuild truly removes it), while a NEWLY selected tool is still absent from
// the union and so still registers as stale and still gets installed.
//
// A from-scratch build does not go through here: buildBase stamps exactly the
// requested selection, because a fresh base really does contain only that.
func mergeToolsetVersion(want, have string) string {
	// Only a v2 stamp has a toolset suffix to merge. Anything else — "" from a
	// version-lookup error, or a version in some other scheme — is passed through
	// untouched rather than being re-spelled as a v2 stamp with an invented empty
	// hash, which would make the base perpetually stale against a version it never
	// claimed to have.
	if !strings.HasPrefix(want, playbookVersionPrefix) {
		return want
	}
	merged := parseToolset(toolsetFromStamp(want))
	for tool, enabled := range parseToolset(toolsetFromStamp(have)) {
		if enabled {
			merged[tool] = true
		}
	}
	return playbookVersionPrefix + hashFromStamp(want) + ":" + toolsetKey(merged)
}
