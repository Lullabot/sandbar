package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitLabCredentialsAtGitBoundary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	entries := collectGitCredEntries(map[string]map[string]string{"git.example.com/group/subgroup": {"GH_TOKEN": "github-secret", "GITLAB_TOKEN": "lab:@/?secret"}, "gitlab.com/empty": {"GITLAB_TOKEN": ""}, "gitlab/group": {"GITLAB_TOKEN": "single-host"}, "GitHub.COM/team": {"GH_TOKEN": "gh-mixed"}, "gitlab.com": {"GITLAB_TOKEN": "cloud-host"}, "GitLab.Example.COM/team": {"GITLAB_TOKEN": "mixed-host"}})
	run := func(script, body string) {
		t.Helper()
		cmd := exec.Command("bash", "-c", script)
		cmd.Stdin = strings.NewReader(body)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("script failed: %v %s", err, out)
		}
	}
	run(`printf '[credential]\n helper = "!f() { input=$(cat); case $input in *host=github.com*|*host=git.example.com*) echo username=wrong; echo password=wrong;; esac; }; f"\n' > "$HOME/.gitconfig"`, "")
	slugs := []string{}
	for _, e := range entries {
		run(gitCredWriteScript(e.slug, e.host), renderGitCredentialLine(e))
		slugs = append(slugs, e.slug)
	}
	run(gitCredReconcileScript(slugs), renderGitconfigManagedBlock(entries, ""))
	repo := filepath.Join(home, "git.example.com/group/subgroup/repo")
	for _, scope := range []string{"git.example.com/group/subgroup", "gitlab/group", "GitHub.COM/team", "gitlab.com", "GitLab.Example.COM/team"} {
		dir := filepath.Join(home, scope, "repo")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
	}

	for _, tc := range []struct{ host, want string }{{"github.com", "github-secret"}, {"git.example.com", "lab:@/?secret"}, {"unrelated.example.com", ""}, {"gitlab.com", ""}} {
		cmd := exec.Command("git", "-C", repo, "credential", "fill")
		cmd.Stdin = strings.NewReader("protocol=https\nhost=" + tc.host + "\n\n")
		out, err := cmd.CombinedOutput()
		if tc.want == "" {
			if err == nil {
				t.Fatalf("unexpected credentials for %s: %s", tc.host, out)
			}
			continue
		}
		if err != nil || !strings.Contains(string(out), "password="+tc.want+"\n") {
			t.Fatalf("credential %s failed: %v %s", tc.host, err, out)
		}
	}

	for _, tc := range []struct{ scope, host, want string }{{"gitlab/group", "gitlab", "single-host"}, {"GitHub.COM/team", "github.com", "gh-mixed"}, {"gitlab.com", "gitlab.com", "cloud-host"}, {"GitLab.Example.COM/team", "GitLab.Example.COM", "mixed-host"}} {
		cmd := exec.Command("git", "-C", filepath.Join(home, tc.scope, "repo"), "credential", "fill")
		cmd.Stdin = strings.NewReader("protocol=https\nhost=" + tc.host + "\n\n")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "password="+tc.want+"\n") {
			t.Fatalf("%s credential failed: %v %s", tc.scope, err, out)
		}
	}
	// A rebuilt guest has no managed files. Reapplying stored scopes restores
	// the credential usable by Git, independent of the initial clone's role.
	if err := os.RemoveAll(filepath.Join(home, ".config", "sandbar")); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		run(gitCredWriteScript(e.slug, e.host), renderGitCredentialLine(e))
	}
	run(gitCredReconcileScript(slugs), renderGitconfigManagedBlock(entries, ""))
	restored := exec.Command("git", "-C", repo, "credential", "fill")
	restored.Stdin = strings.NewReader("protocol=https\nhost=git.example.com\n\n")
	if out, err := restored.CombinedOutput(); err != nil || !strings.Contains(string(out), "password=lab:@/?secret\n") {
		t.Fatalf("rebuild restoration failed: %v %s", err, out)
	}

	// Rotation replaces the credential file; removal prunes it and its include.
	// Remove the deliberately conflicting fixture helper before testing removal.
	if out, err := exec.Command("git", "config", "--global", "--unset-all", "credential.helper").CombinedOutput(); err != nil {
		t.Fatalf("clear fixture: %v %s", err, out)
	}
	for _, token := range []string{"rotated-token", ""} {
		changed := collectGitCredEntries(map[string]map[string]string{"git.example.com/group/subgroup": {"GH_TOKEN": "github-secret", "GITLAB_TOKEN": token}})
		desired := []string{}
		for _, e := range changed {
			run(gitCredWriteScript(e.slug, e.host), renderGitCredentialLine(e))
			desired = append(desired, e.slug)
		}
		run(gitCredReconcileScript(desired), renderGitconfigManagedBlock(changed, ""))
		cmd := exec.Command("git", "-C", repo, "credential", "fill")
		cmd.Stdin = strings.NewReader("protocol=https\nhost=git.example.com\n\n")
		out, err := cmd.CombinedOutput()
		if token == "" {
			if err == nil {
				t.Fatalf("removed token still resolves: %s", out)
			}
		} else if err != nil || !strings.Contains(string(out), "password="+token+"\n") {
			t.Fatalf("rotated token failed: %v %s", err, out)
		}
	}

}
