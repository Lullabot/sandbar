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
