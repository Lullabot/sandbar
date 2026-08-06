package provision

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	sandbar "github.com/lullabot/sandbar"
)

// rsyncFromGuestScript pulls the rsync command out of inGuestScript, joining its
// backslash continuations into one line, so the tests below run the very command
// the guest runs rather than a copy of it that could drift.
func rsyncFromGuestScript(t *testing.T) string {
	t.Helper()
	var cmd []string
	for _, line := range strings.Split(inGuestScript, "\n") {
		if len(cmd) == 0 && !strings.HasPrefix(line, "rsync ") {
			continue
		}
		cmd = append(cmd, strings.TrimSuffix(strings.TrimSpace(line), `\`))
		if !strings.HasSuffix(strings.TrimSpace(line), `\`) {
			break
		}
	}
	if len(cmd) == 0 {
		t.Fatal("no rsync command found in inGuestScript")
	}
	return strings.Join(cmd, " ")
}

// fakeCheckout builds what /mnt/playbook is in repo mode: the playbook fileset
// plus everything else a git checkout carries — including the symlinked agent
// skills, whose readlink() over the read-only Lima mount is what broke the
// unfiltered sync in CI.
func fakeCheckout(t *testing.T) string {
	t.Helper()
	dir, err := extractEmbedded(sandbar.PlaybookFS)
	if err != nil {
		t.Fatalf("extract embedded playbook: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	for _, f := range []string{".git/config", "cmd/sand/main.go", "go.mod", ".agents/skills/st-plan/SKILL.md"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", f, err)
		}
		if err := os.WriteFile(p, []byte("junk\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, ".claude/skills"), 0o755); err != nil {
		t.Fatalf("mkdir .claude/skills: %v", err)
	}
	if err := os.Symlink("../../.agents/skills/st-plan", filepath.Join(dir, ".claude/skills/st-plan")); err != nil {
		t.Fatalf("symlink skill: %v", err)
	}
	return dir
}

// walk lists every file under dir, relative and sorted; directories are implied
// by their contents.
func walk(t *testing.T, dir string) []string {
	t.Helper()
	var got []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		got = append(got, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	sort.Strings(got)
	return got
}

// TestGuestSyncCopiesOnlyThePlaybook runs the guest script's actual rsync over a
// stand-in for a repo-mode mount and asserts the result is exactly the embedded
// playbook fileset: no .git, no Go sources, and no agent-skill symlinks (whose
// readlink() over the read-only host mount fails with EPERM and, unfiltered,
// aborted the whole sync with rsync exit 23).
func TestGuestSyncCopiesOnlyThePlaybook(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	src, dst := fakeCheckout(t), t.TempDir()

	cmd := rsyncFromGuestScript(t)
	cmd = strings.ReplaceAll(cmd, "/mnt/playbook/", src+"/")
	cmd = strings.ReplaceAll(cmd, "/root/playbook/", dst+"/")

	out, err := exec.Command("bash", "-euo", "pipefail", "-c", cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("guest rsync failed: %v\n%s", err, out)
	}

	want := walk(t, mustEmbedDir(t))
	if got := walk(t, dst); !equal(got, want) {
		t.Errorf("synced tree does not match the embedded playbook fileset\ngot:  %v\nwant: %v", got, want)
	}
}

// TestGuestSyncDeletesStalePaths covers the upgrade path: a base image built
// before the filter existed baked the whole repo into /root/playbook, so the
// sync must clear what it no longer copies rather than leave it behind.
func TestGuestSyncDeletesStalePaths(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	src, dst := fakeCheckout(t), t.TempDir()

	stale := filepath.Join(dst, ".git", "config")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatalf("mkdir stale: %v", err)
	}
	if err := os.WriteFile(stale, []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}

	cmd := rsyncFromGuestScript(t)
	cmd = strings.ReplaceAll(cmd, "/mnt/playbook/", src+"/")
	cmd = strings.ReplaceAll(cmd, "/root/playbook/", dst+"/")
	if out, err := exec.Command("bash", "-euo", "pipefail", "-c", cmd).CombinedOutput(); err != nil {
		t.Fatalf("guest rsync failed: %v\n%s", err, out)
	}

	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Errorf("stale %s survived the sync (err=%v), want it deleted", stale, err)
	}
}

// mustEmbedDir extracts the embedded playbook — the fileset a Homebrew-installed
// binary provisions from, and therefore the definition of "the playbook".
func mustEmbedDir(t *testing.T) string {
	t.Helper()
	dir, err := extractEmbedded(sandbar.PlaybookFS)
	if err != nil {
		t.Fatalf("extract embedded playbook: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// seedWebappArtifacts creates the two .gitignore'd directories a contributor
// ends up with after running `npm ci` / `npm run build` in the review web
// app. They exist only in a working-tree checkout, never in the embedded FS —
// which is exactly why no existing test noticed whether they were excluded.
func seedWebappArtifacts(t *testing.T, root string) []string {
	t.Helper()
	files := []string{
		"roles/self-review/files/webapp/node_modules/.package-lock.json",
		"roles/self-review/files/webapp/node_modules/react/index.js",
		"roles/self-review/files/webapp/node_modules/@self-review/core/dist/index.js",
		"roles/self-review/files/webapp/dist/index.html",
		"roles/self-review/files/webapp/dist/assets/main-abc123.js",
	}
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", f, err)
		}
		if err := os.WriteFile(p, []byte("build artifact\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	return files
}

// TestPlaybookFilesetsAgreeOnWebappArtifacts pins the THREE hand-maintained
// lists that must agree about roles/self-review/files/webapp/{node_modules,dist}:
// playbook_embed.go's go:embed directives, provision.go's playbookSyncCmd rsync
// filter, and baseversion.go's playbookFileset/playbookExcludedDirs.
//
// The existing sync tests could not catch a regression in any of them, because
// fakeCheckout sources its tree from the EMBEDDED FS, which by construction can
// never contain node_modules — so the exclusion the filter exists for was never
// exercised. This test seeds the artifacts explicitly, the way a real repo-mode
// checkout has them, and asserts both consumers ignore them:
//
//   - rsync must not copy them, or 358 MB of node_modules lands in /root/playbook
//     on every create and in every base image built from it;
//   - the content hash must not see them, or the base-image version stamp shifts
//     on any npm activity and a perfectly good shared base is declared stale and
//     rebuilt over files that never reach a guest at all.
func TestPlaybookFilesetsAgreeOnWebappArtifacts(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync not installed")
	}
	src := fakeCheckout(t)

	// The stamp a clean checkout produces — the value that must not move.
	clean, err := playbookContentHash(os.DirFS(src))
	if err != nil {
		t.Fatalf("hash clean checkout: %v", err)
	}

	artifacts := seedWebappArtifacts(t, src)

	dirty, err := playbookContentHash(os.DirFS(src))
	if err != nil {
		t.Fatalf("hash checkout with build artifacts: %v", err)
	}
	if dirty != clean {
		t.Errorf("the playbook content hash changed when local npm build artifacts appeared\n"+
			"clean: %s\ndirty: %s\n"+
			"a contributor who has run `npm ci` would rebuild the shared base image for nothing "+
			"(see playbookExcludedDirs)", clean, dirty)
	}

	dst := t.TempDir()
	cmd := rsyncFromGuestScript(t)
	cmd = strings.ReplaceAll(cmd, "/mnt/playbook/", src+"/")
	cmd = strings.ReplaceAll(cmd, "/root/playbook/", dst+"/")
	if out, err := exec.Command("bash", "-euo", "pipefail", "-c", cmd).CombinedOutput(); err != nil {
		t.Fatalf("guest rsync failed: %v\n%s", err, out)
	}
	for _, f := range artifacts {
		if _, err := os.Lstat(filepath.Join(dst, filepath.FromSlash(f))); !os.IsNotExist(err) {
			t.Errorf("rsync copied a local build artifact into the guest playbook: %s\n"+
				"the --include list in playbookSyncCmd must enumerate the webapp's source files, "+
				"never the directory wholesale", f)
		}
	}

	// The webapp's real sources must still arrive — an over-broad exclusion
	// that dropped the server would pass every assertion above.
	for _, f := range []string{
		"roles/self-review/files/webapp/package.json",
		"roles/self-review/files/webapp/server/index.mjs",
		"roles/self-review/files/webapp/src/main.tsx",
	} {
		if _, err := os.Lstat(filepath.Join(dst, filepath.FromSlash(f))); err != nil {
			t.Errorf("rsync did not copy a webapp source file the guest needs: %s (%v)", f, err)
		}
	}
}
