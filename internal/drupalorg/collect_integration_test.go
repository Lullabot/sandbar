package drupalorg

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// collect_integration_test.go runs the REAL guest collection script
// (BuildCollectCommand) against REAL git repositories, and asserts which
// commits come back out the other end of ParseCollect.
//
// It exists because the bug this file's scenarios reproduce could not have
// been caught by any of collect_test.go's synthetic fixtures. Those feed
// hand-written key=value text to ParseCollect, which pins the Go decoder but
// asserts the very commit list the script was ASSUMED to produce. The defect
// was upstream of all of it, in one `rev-list` argument list: excluding only
// the fork branch answers "what has not reached the fork", and the moment the
// canonical base branch enters HEAD's ancestry that is a different set of
// commits from "what this contributor wrote". Only running the real script
// over a real history distinguishes them — see sweep_integration_test.go's
// doc comment, which makes the same argument about reversed revision ranges.
//
// The script is written for a Debian guest but is plain git plus bash (it
// relies on `set -o pipefail` and `${msg%$'\n'}`), so it runs unmodified on
// any machine with those. Where bash is missing the test skips rather than
// failing — it is a real-tool integration test, not a portability claim.

// collectGitEnv is a deterministic, user-config-free environment for the
// throwaway repos below, so a developer's own git config (a commit hook, a
// signing key, a different default branch name) can never change what these
// assert.
func collectGitEnv(home string) []string {
	return append(os.Environ(),
		"HOME="+home,
		"GIT_CONFIG_GLOBAL="+filepath.Join(home, ".gitconfig-absent"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(home, ".gitconfig-absent-system"),
		"GIT_AUTHOR_NAME=Dev Eloper", "GIT_AUTHOR_EMAIL=dev@example.com",
		"GIT_COMMITTER_NAME=Dev Eloper", "GIT_COMMITTER_EMAIL=dev@example.com",
	)
}

func collectGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = collectGitEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func collectCommit(t *testing.T, dir, file, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	collectGit(t, dir, "add", file)
	collectGit(t, dir, "commit", "-m", message)
}

// forkBranch is the branch name every fixture uses, and the remote-tracking
// ref standing in for what the issue fork already holds.
const (
	forkBranch  = "dubbot-3619578"
	forkBaseRef = "origin/" + forkBranch
)

// newCollectFixture builds the history every scenario below starts from and
// returns the repo path plus the canonical base branch's tip:
//
//   - base commit on 2.x          <- both branches share this
//   - feature work                <- the contributor's, already on the fork
//     (refs/remotes/origin/dubbot-3619578 points here)
//   - UPSTREAM commit on 2.x      <- somebody else's, already public on 2.x
//
// HEAD is left on the feature branch, which has NOT yet taken the upstream
// commit; each scenario decides how (or whether) it does.
func newCollectFixture(t *testing.T) (dir, projectBase string) {
	t.Helper()
	dir = t.TempDir()
	collectGit(t, dir, "init", "--quiet", "--initial-branch=2.x", ".")
	collectCommit(t, dir, "README.md", "base\n", "base commit on 2.x")

	collectGit(t, dir, "checkout", "--quiet", "-b", forkBranch)
	collectCommit(t, dir, "feature.php", "<?php // mine\n", "The contributor's own work")
	// What the issue fork's branch already holds.
	collectGit(t, dir, "update-ref", "refs/remotes/"+forkBaseRef, "HEAD")

	// Somebody else's commit, landed on the canonical project's base branch
	// after this contributor started. This is the commit that must never
	// appear in a change set.
	collectGit(t, dir, "checkout", "--quiet", "2.x")
	collectCommit(t, dir, "upstream.php", "<?php // someone else\n", "UPSTREAM: not this contributor's commit")
	projectBase = collectGit(t, dir, "rev-parse", "HEAD")

	collectGit(t, dir, "checkout", "--quiet", forkBranch)
	return dir, projectBase
}

// runCollect executes the actual guest script in dir and returns what
// ParseCollect makes of its output.
func runCollect(t *testing.T, dir, forkBase, projectBase string) (ChangeSet, error) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available; the collection script needs it for pipefail")
	}
	script, err := BuildCollectCommand(forkBase, projectBase)
	if err != nil {
		t.Fatalf("BuildCollectCommand: %v", err)
	}
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = dir
	cmd.Env = collectGitEnv(dir)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ChangeSet{}, errors.New(strings.TrimSpace(string(ee.Stderr)))
		}
		return ChangeSet{}, err
	}
	return ParseCollect(string(out))
}

func messages(cs ChangeSet) []string {
	out := make([]string, 0, len(cs.Commits))
	for _, c := range cs.Commits {
		out = append(out, strings.TrimSpace(c.Message))
	}
	return out
}

// TestCollectExcludesUpstreamAfterRebase is the direct regression test for
// the reported bug's rebase half. Rebasing the feature branch onto a newer
// canonical base pulls the upstream commit into HEAD's ancestry, where the
// old single-exclusion range ("what has not reached the fork") reported it as
// this contributor's unpublished work.
func TestCollectExcludesUpstreamAfterRebase(t *testing.T) {
	dir, projectBase := newCollectFixture(t)
	collectGit(t, dir, "rebase", "--quiet", "2.x")

	cs, err := runCollect(t, dir, forkBaseRef, projectBase)
	if err != nil {
		t.Fatalf("collect after rebase: %v", err)
	}
	got := messages(cs)
	want := []string{"The contributor's own work"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("collected %q, want exactly %q — the upstream commit must not be republished", got, want)
	}
}

// TestCollectRefusesBackMergedDestinationBranch is the other half, and the
// exact shape that produced the wrong merge request: the destination branch
// merged INTO the feature branch. The upstream commit is excluded by the
// canonical base, but the merge commit itself remains — and publication has
// no way to express its second parent, so the whole range is refused rather
// than silently flattened.
func TestCollectRefusesBackMergedDestinationBranch(t *testing.T) {
	dir, projectBase := newCollectFixture(t)
	collectGit(t, dir, "merge", "--quiet", "--no-ff", "2.x", "-m", "Merge branch '2.x' into "+forkBranch)

	_, err := runCollect(t, dir, forkBaseRef, projectBase)
	if err == nil {
		t.Fatal("collect accepted a back-merged branch, want a refusal")
	}
	var mergeErr *MergeCommitsError
	if !errors.As(err, &mergeErr) {
		t.Fatalf("collect error = %v (%T), want a *MergeCommitsError", err, err)
	}
	if len(mergeErr.SHAs) != 1 {
		t.Errorf("MergeCommitsError.SHAs = %v, want the one merge commit", mergeErr.SHAs)
	}
}

// TestCollectOrdinaryUnpublishedCommits pins the common case the two
// exclusions must leave alone: commits made on top of what the fork already
// holds, with the canonical base branch nowhere in the picture.
func TestCollectOrdinaryUnpublishedCommits(t *testing.T) {
	dir, projectBase := newCollectFixture(t)
	collectCommit(t, dir, "second.php", "<?php // also mine\n", "A second commit of mine")
	collectCommit(t, dir, "third.php", "<?php // mine too\n", "A third commit of mine")

	cs, err := runCollect(t, dir, forkBaseRef, projectBase)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	got := messages(cs)
	want := []string{"A second commit of mine", "A third commit of mine"}
	if len(got) != len(want) {
		t.Fatalf("collected %q, want %q", got, want)
	}
	// Oldest first: the order a replay must send them in.
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("commit %d = %q, want %q", i, got[i], want[i])
		}
	}
	// The commit already on the fork must not come back round again.
	for _, m := range got {
		if m == "The contributor's own work" {
			t.Error("collected a commit the fork branch already holds")
		}
	}
}

// TestCollectRefusesMissingProjectBase pins the diagnostic for the one new
// way this can fail: the canonical base tip is resolved host-side from
// drupal.org, and nothing in this flow fetches, so a checkout that has never
// seen the canonical project simply does not have that object. The refusal
// has to say what to do about it — `rev-list`'s own "fatal: bad object" does
// not.
func TestCollectRefusesMissingProjectBase(t *testing.T) {
	dir, _ := newCollectFixture(t)
	absent := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	_, err := runCollect(t, dir, forkBaseRef, absent)
	if err == nil {
		t.Fatal("collect accepted a projectBase the checkout does not have, want a refusal")
	}
	for _, want := range []string{"fetch", "rebase"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q, so it does not say what to do:\n%s", want, err.Error())
		}
	}
}
