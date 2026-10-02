package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The reset executes this exact script in the newly cloned guest. A golden
// template can have symlinked config parents; clearing a baseline through one
// would delete data outside the guest home before the archive is restored.
func TestClearFreshBaselinesRefusesSymlinkParents(t *testing.T) {
	for _, rel := range []string{".config", ".config/sandbar", ".config/sandbar/home-baselines"} {
		t.Run(rel, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			outside := filepath.Join(root, "outside")
			if err := os.MkdirAll(home, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(outside, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("user data"), 0600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(home, rel)
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			if err := exec.Command("bash", "-c", clearFreshBaselinesScript, "sand-home", home).Run(); err == nil {
				t.Fatal("cleared through a symlink")
			}
			body, err := os.ReadFile(filepath.Join(outside, "keep"))
			if err != nil || string(body) != "user data" {
				t.Fatalf("outside data changed: %q, %v", body, err)
			}
		})
	}
}

func TestClearFreshBaselinesRemovesOnlyBaselineDirectory(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	base := filepath.Join(home, ".config/sandbar/home-baselines")
	other := filepath.Join(home, ".config/sandbar/proposed-defaults")
	for _, dir := range []string{base, other} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("bash", "-c", clearFreshBaselinesScript, "sand-home", home).CombinedOutput(); err != nil {
		t.Fatalf("clear: %v: %s", err, out)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatalf("baseline remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "keep")); err != nil {
		t.Fatalf("other home data removed: %v", err)
	}
}
