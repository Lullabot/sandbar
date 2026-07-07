package provision

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	sandbar "github.com/lullabot/sandbar"
)

// TestEmbedPlaybookFSComplete asserts the embedded playbook fileset
// (sandbar.PlaybookFS) retains every member LocatePlaybook's embedded tier
// depends on. It exists to guard against go:embed silently dropping
// dot/underscore files under roles/ or group_vars/ when the `all:` prefix is
// missing from the embed directive.
func TestEmbedPlaybookFSComplete(t *testing.T) {
	requiredFiles := []string{
		"site.yml",
		"ansible.cfg",
		"inventory",
	}
	for _, f := range requiredFiles {
		if _, err := sandbar.PlaybookFS.Open(f); err != nil {
			t.Errorf("embedded FS missing required file %q: %v", f, err)
		}
	}

	if matches, err := fs.Glob(sandbar.PlaybookFS, "roles/*/tasks/main.yml"); err != nil {
		t.Fatalf("glob roles/*/tasks/main.yml: %v", err)
	} else if len(matches) == 0 {
		t.Error("embedded FS has no roles/*/tasks/main.yml — all:roles embed pattern may have dropped files")
	}

	if matches, err := fs.Glob(sandbar.PlaybookFS, "group_vars/*"); err != nil {
		t.Fatalf("glob group_vars/*: %v", err)
	} else if len(matches) == 0 {
		t.Error("embedded FS has no group_vars/* members — all:group_vars embed pattern may have dropped files")
	}
}

// TestEmbedMaterializeToTempDir asserts the embedded fileset can be
// materialised byte-for-byte into a fresh, private temp dir, independent of
// any git checkout — this is the tier-2 resolver path exercised when
// LocatePlaybook runs outside a repository (e.g. a Homebrew install).
func TestEmbedMaterializeToTempDir(t *testing.T) {
	dir, err := materializeEmbedded(sandbar.PlaybookFS)
	if err != nil {
		t.Fatalf("materializeEmbedded: %v", err)
	}

	for _, f := range []string{"site.yml", "ansible.cfg", "inventory"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("materialised dir missing %q: %v", f, err)
		}
	}

	matches, err := filepath.Glob(filepath.Join(dir, "roles", "*", "tasks", "main.yml"))
	if err != nil {
		t.Fatalf("glob roles/*/tasks/main.yml: %v", err)
	}
	if len(matches) == 0 {
		t.Error("materialised dir has no roles/*/tasks/main.yml")
	}

	gvMatches, err := filepath.Glob(filepath.Join(dir, "group_vars", "*"))
	if err != nil {
		t.Fatalf("glob group_vars/*: %v", err)
	}
	if len(gvMatches) == 0 {
		t.Error("materialised dir has no group_vars/* members")
	}

	// Byte-identical spot check against the source tree's site.yml.
	wantSite, err := os.ReadFile("../../site.yml")
	if err != nil {
		t.Fatalf("read source site.yml: %v", err)
	}
	gotSite, err := os.ReadFile(filepath.Join(dir, "site.yml"))
	if err != nil {
		t.Fatalf("read materialised site.yml: %v", err)
	}
	if string(wantSite) != string(gotSite) {
		t.Error("materialised site.yml is not byte-identical to the source tree")
	}
}

// TestEmbedLocatePlaybookOutsideCheckout asserts LocatePlaybook falls through
// to the embedded tier — rather than hard-erroring with "not inside a git
// checkout" — when run with a working directory outside any git repository.
// This is the scenario a Homebrew-installed binary hits: no repo checkout on
// disk at all.
func TestEmbedLocatePlaybookOutsideCheckout(t *testing.T) {
	outside := t.TempDir()
	t.Chdir(outside)

	dir, err := LocatePlaybook()
	if err != nil {
		t.Fatalf("LocatePlaybook outside a checkout returned an error (should fall through to embedded): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "site.yml")); err != nil {
		t.Errorf("resolved dir %s missing site.yml: %v", dir, err)
	}
}
