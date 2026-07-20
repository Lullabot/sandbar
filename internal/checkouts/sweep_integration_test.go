package checkouts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sweep_integration_test.go runs the REAL guest sweep script (BuildSweepCommand)
// against REAL git repositories, and asserts the PushState/Dirty values that
// come back out the other end of ParseSweep.
//
// Every other test in this package feeds hand-written key=value text to
// ParseSweep. Those pin the Go classifier, but they prove nothing about the
// half of the system that actually decides the answer: whether the shell
// really emits `tracking=0` for a never-pushed branch, whether the `rev-list`
// ranges are the right way round, whether `git status --porcelain | grep -c .`
// counts what we think. A synthetic test cannot catch a reversed revision
// range, because it asserts the very numbers the script was ASSUMED to
// produce.
//
// The script is written for a Debian guest but is plain POSIX sh plus git and
// timeout, so it runs unmodified on any Linux host with those. Where they are
// missing (a macOS dev box has no `timeout`), the test skips rather than
// failing — it is a real-tool integration test, not a portability claim.

// gitEnv is a deterministic, user-config-free environment for the throwaway
// repos below, so a developer's own git config (a commit hook, a signing key,
// a different default branch name) can never change what these assert.
func gitEnv(home string) []string {
	return append(os.Environ(),
		"HOME="+home,
		"GIT_CONFIG_GLOBAL="+filepath.Join(home, ".gitconfig-absent"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(home, ".gitconfig-absent-system"),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
}

func runGit(t *testing.T, dir, home string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv(home)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// runSweep executes the actual guest script with HOME pointed at the fixture
// tree, and returns what ParseSweep makes of its output.
func runSweep(t *testing.T, home string) VMCheckouts {
	t.Helper()
	cmd := exec.Command("sh", "-c", BuildSweepCommand())
	cmd.Env = gitEnv(home)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sweep script: %v\noutput:\n%s", err, out)
	}
	return ParseSweep(string(out))
}

// findCheckout returns the swept record for the repo at path.
func findCheckout(t *testing.T, vc VMCheckouts, path string) Checkout {
	t.Helper()
	for _, c := range vc.Checkouts {
		if c.Path == path {
			return c
		}
	}
	t.Fatalf("sweep found no checkout at %s; found %+v", path, vc.Checkouts)
	return Checkout{}
}

func requireGitTools(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"git", "sh", "timeout", "find"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available; the sweep script needs it", bin)
		}
	}
}

// TestSweepAgainstRealGit walks ONE repository through the three states the
// land feature branches on, running the real script at each step:
//
//	dirty -> committed-but-never-pushed -> pushed -> committed-but-unpushed
//
// Doing it as a progression on a single repo (rather than four fixtures) is
// deliberate: it pins the TRANSITIONS, which is where a misread of git's
// output actually bites — most of all that pushing a branch flips it from
// "unpushed" to "pushed", and that committing again flips it back.
func TestSweepAgainstRealGit(t *testing.T) {
	requireGitTools(t)

	home := t.TempDir()
	remote := filepath.Join(home, "remote.git")
	work := filepath.Join(home, "work")

	runGit(t, home, home, "init", "-q", "--bare", "-b", "main", remote)
	runGit(t, home, home, "init", "-q", "-b", "main", work)
	runGit(t, work, home, "remote", "add", "origin", remote)

	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// --- State A: uncommitted changes -------------------------------------
	write("a.txt", "one\n")
	runGit(t, work, home, "add", "a.txt")
	write("b.txt", "untracked-and-unstaged\n")

	got := findCheckout(t, runSweep(t, home), work)
	if got.Dirty == 0 {
		t.Errorf("state A: Dirty = 0, want a nonzero count for a dirty working tree (%+v)", got)
	}
	if got.PushState != PushStateNever {
		t.Errorf("state A: PushState = %q, want %q — nothing has been pushed", got.PushState, PushStateNever)
	}

	// --- State B2: committed, never pushed --------------------------------
	runGit(t, work, home, "add", "-A")
	runGit(t, work, home, "commit", "-qm", "one")

	got = findCheckout(t, runSweep(t, home), work)
	if got.Dirty != 0 {
		t.Errorf("state B(never): Dirty = %d, want 0 after committing everything", got.Dirty)
	}
	if got.PushState != PushStateNever {
		t.Errorf("state B(never): PushState = %q, want %q — the branch has no remote-tracking ref",
			got.PushState, PushStateNever)
	}
	if got.Ahead != 0 {
		t.Errorf("state B(never): Ahead = %d, want 0 — there is no tracking ref to count against", got.Ahead)
	}
	if got.Branch != "main" {
		t.Errorf("state B(never): Branch = %q, want %q", got.Branch, "main")
	}

	// --- State C: pushed ---------------------------------------------------
	runGit(t, work, home, "push", "-q", "-u", "origin", "main")

	got = findCheckout(t, runSweep(t, home), work)
	if got.PushState != PushStatePushed {
		t.Errorf("state C: PushState = %q, want %q after a push", got.PushState, PushStatePushed)
	}
	if got.Ahead != 0 || got.Behind != 0 {
		t.Errorf("state C: Ahead/Behind = %d/%d, want 0/0 — HEAD is exactly the tracking ref",
			got.Ahead, got.Behind)
	}

	// --- State B1: committed, ahead of the remote --------------------------
	// Two commits, so a swapped rev-list range cannot pass by symmetry: this
	// must read as ahead=2/behind=0, never 0/2.
	write("a.txt", "two\n")
	runGit(t, work, home, "commit", "-qam", "two")
	write("a.txt", "three\n")
	runGit(t, work, home, "commit", "-qam", "three")

	got = findCheckout(t, runSweep(t, home), work)
	if got.PushState != PushStateUnpushed {
		t.Errorf("state B(unpushed): PushState = %q, want %q", got.PushState, PushStateUnpushed)
	}
	if got.Ahead != 2 {
		t.Errorf("state B(unpushed): Ahead = %d, want 2 — a swapped rev-list range would report 0 here", got.Ahead)
	}
	if got.Behind != 0 {
		t.Errorf("state B(unpushed): Behind = %d, want 0 — a swapped rev-list range would report 2 here", got.Behind)
	}
}

// TestSweepAgainstRealGitBehind pins the OTHER direction of the rev-list
// ranges, which the ahead-only case above cannot distinguish on its own: a
// checkout whose remote has moved on must read as behind, not ahead.
func TestSweepAgainstRealGitBehind(t *testing.T) {
	requireGitTools(t)

	home := t.TempDir()
	remote := filepath.Join(home, "remote.git")
	work := filepath.Join(home, "work")
	other := filepath.Join(home, "other")

	runGit(t, home, home, "init", "-q", "--bare", "-b", "main", remote)
	runGit(t, home, home, "init", "-q", "-b", "main", work)
	runGit(t, work, home, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, home, "add", "-A")
	runGit(t, work, home, "commit", "-qm", "one")
	runGit(t, work, home, "push", "-q", "-u", "origin", "main")

	// A second clone pushes a commit, then the first fetches it without
	// merging: now its tracking ref is ahead of HEAD.
	runGit(t, home, home, "clone", "-q", remote, other)
	runGit(t, other, home, "config", "user.email", "t@example.com")
	runGit(t, other, home, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(other, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, home, "commit", "-qam", "two")
	runGit(t, other, home, "push", "-q", "origin", "main")
	runGit(t, work, home, "fetch", "-q", "origin")

	got := findCheckout(t, runSweep(t, home), work)
	if got.Behind != 1 {
		t.Errorf("Behind = %d, want 1 — the remote has one commit this checkout does not", got.Behind)
	}
	if got.Ahead != 0 {
		t.Errorf("Ahead = %d, want 0 — a swapped rev-list range would report 1 here", got.Ahead)
	}
	if got.PushState != PushStatePushed {
		t.Errorf("PushState = %q, want %q — being behind is not being unpushed", got.PushState, PushStatePushed)
	}
}

// TestSweepAgainstRealGitDefaultBranch pins the origin/HEAD read that
// NothingToLand depends on, against a real clone — the one place the value
// actually comes from. A synthetic test cannot tell whether `git clone` really
// writes refs/remotes/origin/HEAD, nor whether the `$remote/` prefix is
// stripped correctly.
func TestSweepAgainstRealGitDefaultBranch(t *testing.T) {
	requireGitTools(t)

	home := t.TempDir()
	remote := filepath.Join(home, "remote.git")
	seed := filepath.Join(home, "seed")
	clone := filepath.Join(home, "clone")

	runGit(t, home, home, "init", "-q", "--bare", "-b", "main", remote)
	runGit(t, home, home, "init", "-q", "-b", "main", seed)
	runGit(t, seed, home, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(seed, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, home, "add", "-A")
	runGit(t, seed, home, "commit", "-qm", "one")
	runGit(t, seed, home, "push", "-q", "-u", "origin", "main")
	runGit(t, home, home, "clone", "-q", remote, clone)

	got := findCheckout(t, runSweep(t, home), clone)
	if got.DefaultBranch != "main" {
		t.Errorf("DefaultBranch = %q, want %q (bare, with no %q prefix)", got.DefaultBranch, "main", "origin/")
	}
	// The whole point: a pristine clone must read as having nothing to land.
	if !got.NothingToLand() {
		t.Errorf("a pristine clone reported work to land: %+v", got)
	}

	// A feature branch in that same clone must NOT be dismissed the same way.
	runGit(t, clone, home, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(clone, "b.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, clone, home, "config", "user.email", "t@example.com")
	runGit(t, clone, home, "config", "user.name", "t")
	runGit(t, clone, home, "add", "-A")
	runGit(t, clone, home, "commit", "-qm", "feature work")

	got = findCheckout(t, runSweep(t, home), clone)
	if got.Branch != "feature" {
		t.Fatalf("Branch = %q, want %q", got.Branch, "feature")
	}
	if got.DefaultBranch != "main" {
		t.Errorf("DefaultBranch = %q, want %q even while on a feature branch", got.DefaultBranch, "main")
	}
	if got.NothingToLand() {
		t.Error("a committed feature branch was dismissed as nothing to land")
	}
	if got.PushState != PushStateNever {
		t.Errorf("PushState = %q, want %q for a branch that has never been pushed", got.PushState, PushStateNever)
	}
}

// TestSweepAgainstRealGitWorktree pins the linked-worktree path — the `.git`
// FILE case, whose `gitdir:` pointer parentFromGitdirPointer resolves. Only a
// real `git worktree add` writes that file in git's actual format.
func TestSweepAgainstRealGitWorktree(t *testing.T) {
	requireGitTools(t)

	home := t.TempDir()
	work := filepath.Join(home, "work")
	wt := filepath.Join(home, "wt")

	runGit(t, home, home, "init", "-q", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, home, "add", "-A")
	runGit(t, work, home, "commit", "-qm", "one")
	runGit(t, work, home, "worktree", "add", "-q", "-b", "side", wt)

	vc := runSweep(t, home)
	got := findCheckout(t, vc, wt)
	if got.Kind != KindWorktree {
		t.Errorf("Kind = %v, want KindWorktree for a linked worktree", got.Kind)
	}
	if got.Parent != work {
		t.Errorf("Parent = %q, want %q — the gitdir pointer should resolve to the parent repo", got.Parent, work)
	}
	if got.Branch != "side" {
		t.Errorf("Branch = %q, want %q", got.Branch, "side")
	}
}
