package drupalorg

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Publication replays commits through drupal.org's content API rather than
// pushing a ref (see the "There is no force push" section of the publishing
// guide). Every commit it lands is therefore a NEW commit object: a new SHA,
// a new committer of record (the PAT's owner), a new timestamp, a new parent
// chain. The guest checkout still holds the originals.
//
// The result is two histories with identical content and no commit in
// common. That is not a bug, and resumption is already built for it —
// alreadyLandedCount matches commits by (message, author name, author
// email), never by SHA, which is exactly why a partial publish can be
// resumed at all. But nothing told the CHECKOUT, whose remote-tracking ref
// still pointed at whatever the fork held before the publish. A developer
// looking at `git log origin/<branch>` saw a stale picture, and a later
// `git pull` merged the two histories into a pile of duplicated commits.
//
// This file closes that gap with two guest-side steps, in the same
// build-here/parse-here idiom as remoteinfo.go and collect.go:
//
//   - BuildFetchCommand, which fetches the fork's branch and reports how the
//     checkout compares. It is a read: it moves no local branch and touches
//     no working tree.
//   - BuildResetCommand, which adopts the published commits by resetting the
//     local branch onto them. It is the one guest WRITE in this flow, it is
//     offered only when SyncStatus says it is safe, and it re-checks that
//     safety in the guest immediately before acting.

// syncBranchRe is the branch names these scripts will interpolate. It is
// deliberately far stricter than git's own ref rules: the only branch either
// script is ever asked for is a Destination.Branch, which NewDestination
// derives through ForkPath and is therefore always "<module>-<nid>" with
// module "[a-z0-9_]+" and nid a positive integer.
//
// RunArgv's (workdir, expr) signature leaves no argv slot to pass a branch
// through, so it has to reach the guest as script text — the same bind
// BuildCollectCommand is in with its base ref, and resolved the same way:
// validate against a set that cannot contain a quote, a space, a "$", a
// backtick, or a newline, and the substitution is safe rather than merely
// convenient. Validating here rather than trusting the caller is the point;
// provenance is an argument, a pattern is a guarantee.
var syncBranchRe = regexp.MustCompile(`^[a-z0-9_]+-[0-9]+$`)

// validateSyncBranch refuses any branch these scripts must not interpolate.
func validateSyncBranch(branch string) error {
	if !syncBranchRe.MatchString(branch) {
		return fmt.Errorf("drupalorg: refusing to build a guest sync command for branch %q: it is not a <module>-<issue> fork branch", branch)
	}
	return nil
}

// remoteResolution is the remote-picking preamble both scripts share, and it
// is character-for-character the rule remoteInfoScript and
// internal/ui/landing.go's commitAndPushExpr already use: the branch's
// configured remote, falling back to the first configured one, never
// assuming "origin". Sharing the text is the point — a checkout whose fork
// remote is not called "origin" must be fetched from the same place the
// publish read it from, or the sync would report on a different repository
// than the one just published to.
const remoteResolution = `r=$(git config --get "branch.$(git symbolic-ref --short HEAD).remote") || true
[ -n "$r" ] || r=$(git remote | head -n1)
if [ -z "$r" ]; then
  echo "this checkout has no remote configured" >&2
  exit 1
fi
`

// fetchScriptTemplate fetches the fork branch and prints how the checkout
// compares to it, as "key=value" lines in sweep.go's field idiom.
//
// It reports against FETCH_HEAD rather than a remote-tracking ref because
// FETCH_HEAD is what this fetch definitively just wrote: whether `git fetch
// <remote> <branch>` also updates refs/remotes/<remote>/<branch> varies with
// git's version and the remote's configured refspec, and a sync must not
// report on a ref it cannot be sure it refreshed.
//
// "same" is the field that decides everything downstream. After a clean
// replay the two histories are divergent (each is "ahead" of the other in
// commit count) while their TREES are identical — which is precisely the
// state in which adopting the fork's commits costs nothing but the local
// SHAs. `git diff --quiet` answers that in one call; the ahead/behind counts
// are reported alongside for the human, not for the decision.
//
// EVERY value is captured into a variable before anything is printed, and
// that is load-bearing rather than stylistic. `set -e` does NOT abort on a
// command substitution that fails as an ARGUMENT — `printf '%s' "$(git
// rev-parse HEAD)"` succeeds even when the rev-parse inside it does not, and
// rev-parse helpfully echoes its own argument back on failure. Written that
// way this script printed `local=HEAD` and exited 0 in a repository with no
// commits, and a failing `git status` would have reported `dirty=0` — the
// direction that lets a reset proceed. A plain `var=$(cmd)` assignment is a
// simple command whose status IS the substitution's, so `set -e` catches it
// and the script dies instead of reporting a fiction.
//
// The one deliberate exception is the `git diff` in the `if`, whose non-zero
// status is its answer. It cannot distinguish "differ" (1) from "error" (2),
// which means a git failure there reads as "different" — the safe direction,
// since SameContent false only ever withholds the offer to reset.
const fetchScriptTemplate = `set -e
` + remoteResolution + `git fetch --quiet "$r" "__BRANCH__"
head=$(git rev-parse --verify HEAD)
fork=$(git rev-parse --verify FETCH_HEAD)
status=$(git status --porcelain)
if [ -z "$status" ]; then dirty=0; else dirty=$(printf '%s\n' "$status" | wc -l | tr -d ' '); fi
ahead=$(git rev-list --count "$fork..$head")
behind=$(git rev-list --count "$head..$fork")
if git diff --quiet "$head" "$fork"; then same=1; else same=0; fi
printf 'dirty=%s\nlocal=%s\nfork=%s\nahead=%s\nbehind=%s\nsame=%s\n' \
  "$dirty" "$head" "$fork" "$ahead" "$behind" "$same"
`

// resetScriptTemplate adopts the published commits: it re-fetches, re-checks
// both safety conditions IN THE GUEST, and only then moves the branch.
//
// Re-checking is not belt-and-braces, it is the whole reason this is one
// script rather than a host-side `if` around a bare reset. Between the fetch
// that produced a SyncStatus and a human answering the prompt it justified,
// an agent in the guest can have written files, committed, or fetched — this
// VM runs untrusted code, and the working tree is exactly what it is there
// to change. A guard evaluated on the host against a minutes-old reading is
// a guard against the past; these two run immediately before `git reset
// --hard` and abort it with a diagnostic instead of destroying whatever
// appeared in the meantime.
//
// The status read is assigned to a variable rather than tested inline for
// the reason fetchScriptTemplate explains at length: `[ -n "$(git status
// --porcelain)" ]` treats a FAILED git status as a clean tree, and a clean
// tree is what authorizes the reset. Here that mistake would not merely
// misreport, it would destroy uncommitted work.
const resetScriptTemplate = `set -e
` + remoteResolution + `git fetch --quiet "$r" "__BRANCH__"
head=$(git rev-parse --verify HEAD)
fork=$(git rev-parse --verify FETCH_HEAD)
status=$(git status --porcelain)
if [ -n "$status" ]; then
  echo "refusing to adopt: this checkout has uncommitted changes" >&2
  exit 1
fi
if ! git diff --quiet "$head" "$fork"; then
  echo "refusing to adopt: the fork's content no longer matches this checkout" >&2
  exit 1
fi
git reset --hard --quiet "$fork"
git rev-parse --verify HEAD
`

// BuildFetchCommand returns the guest-side command that fetches branch from
// the checkout's fork remote and reports the comparison for ParseSyncStatus.
//
// Like every other script in this package it takes no checkout path and
// embeds none: the checkout is selected entirely by the working directory
// RunArgv sets. It writes nothing — no local branch moves, no file changes —
// so it is safe to run after every publish without asking anyone.
func BuildFetchCommand(branch string) (string, error) {
	if err := validateSyncBranch(branch); err != nil {
		return "", err
	}
	return strings.NewReplacer("__BRANCH__", branch).Replace(fetchScriptTemplate), nil
}

// BuildResetCommand returns the guest-side command that resets the checkout's
// current branch onto the fork's published commits, printing the adopted SHA.
//
// This is the only guest write publication performs, and callers must offer
// it only when SyncStatus.CanAdopt reports true. The script refuses on its
// own terms as well — see resetScriptTemplate — so a caller that offered it
// wrongly, or a guest that changed underneath a correct offer, gets a
// refusal rather than a destroyed working tree.
func BuildResetCommand(branch string) (string, error) {
	if err := validateSyncBranch(branch); err != nil {
		return "", err
	}
	return strings.NewReplacer("__BRANCH__", branch).Replace(resetScriptTemplate), nil
}

// SyncStatus is how a checkout compares to the fork branch just published to.
type SyncStatus struct {
	// Dirty is the number of uncommitted changes in the working tree.
	// Publication carries committed commits ONLY, so a dirty tree means work
	// stayed behind — and adopting the fork's history would destroy it.
	Dirty int
	// Local is the checkout's HEAD; Fork is the commit the fork's branch
	// points at. After a replay these always differ: the content API creates
	// new commit objects rather than transporting the local ones.
	Local string
	Fork  string
	// Ahead and Behind are the two halves of the divergence, for reporting.
	// After a clean replay of n commits both are n — each history holds n
	// commits the other does not, being the same changes twice over.
	Ahead  int
	Behind int
	// SameContent reports that HEAD and the fork's branch have identical
	// TREES. This is the fact that makes adopting safe: identical content
	// means the only thing a reset discards is the local commit objects,
	// whose changes are already public under different SHAs.
	SameContent bool
}

// Diverged reports whether the checkout and the fork are at different
// commits — true after essentially every publish, since a replay cannot
// produce the local SHAs.
func (s SyncStatus) Diverged() bool { return s.Local != s.Fork && s.Fork != "" }

// CanAdopt reports whether resetting the checkout onto the fork's published
// commits is safe to OFFER: the histories differ, their content does not,
// and there is no uncommitted work that the reset would throw away.
//
// All three conditions are required. Without SameContent a reset would
// discard real local changes that never made it to the fork; with a dirty
// tree it would discard the uncommitted remainder publication deliberately
// left behind. Neither is a thing to do on a developer's behalf, and neither
// is a thing to offer.
func (s SyncStatus) CanAdopt() bool {
	return s.Diverged() && s.SameContent && s.Dirty == 0
}

// Summary is the one-line human description of where the checkout stands,
// shared by both surfaces so they cannot describe the same state in two
// idioms.
func (s SyncStatus) Summary() string {
	switch {
	case !s.Diverged():
		return "this checkout already matches the fork"
	case s.SameContent && s.Dirty == 0:
		return fmt.Sprintf(
			"this checkout and the fork hold the same content under different commits (%d local, %d published) — publication replays commits rather than pushing them, so the SHAs differ",
			s.Ahead, s.Behind)
	case s.SameContent:
		return fmt.Sprintf(
			"this checkout and the fork hold the same committed content under different commits (%d local, %d published), but %d uncommitted change(s) would be lost by adopting the published history",
			s.Ahead, s.Behind, s.Dirty)
	default:
		return fmt.Sprintf(
			"this checkout differs from the fork in content, not just in commit SHAs (%d local, %d published, %d uncommitted change(s))",
			s.Ahead, s.Behind, s.Dirty)
	}
}

// syncFieldKeys are the recognized "key=value" field names, so that a login
// shell's banner or any other stray guest output is ignored as noise rather
// than misparsed — the same rule sweep.go and ParseCollect apply.
var syncFieldKeys = map[string]bool{
	"dirty": true, "local": true, "fork": true, "ahead": true, "behind": true, "same": true,
}

// ParseSyncStatus converts BuildFetchCommand's stdout into a SyncStatus. It
// is pure — no exec, no VM — so the comparison logic is unit-testable apart
// from the subprocess plumbing, exactly as ParseCollect and ParseRemoteInfo
// are.
func ParseSyncStatus(out []byte) (SyncStatus, error) {
	var s SyncStatus
	seen := make(map[string]bool, len(syncFieldKeys))

	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || !syncFieldKeys[key] {
			continue
		}
		seen[key] = true
		switch key {
		case "local":
			s.Local = value
		case "fork":
			s.Fork = value
		case "same":
			s.SameContent = value == "1"
		default:
			n, err := strconv.Atoi(value)
			if err != nil {
				return SyncStatus{}, fmt.Errorf("drupalorg: sync field %q has a non-numeric value %q", key, value)
			}
			switch key {
			case "dirty":
				s.Dirty = n
			case "ahead":
				s.Ahead = n
			case "behind":
				s.Behind = n
			}
		}
	}

	// Every field must be present. A partial reading is the dangerous case:
	// a missing "dirty" would default to 0 and a missing "same" to false,
	// and CanAdopt would then be deciding whether to destroy a working tree
	// from values nothing actually reported.
	for key := range syncFieldKeys {
		if !seen[key] {
			return SyncStatus{}, fmt.Errorf("drupalorg: sync output is missing the %q field: %q", key, string(out))
		}
	}
	if s.Local == "" || s.Fork == "" {
		return SyncStatus{}, fmt.Errorf("drupalorg: sync output named no commit: %q", string(out))
	}
	return s, nil
}
