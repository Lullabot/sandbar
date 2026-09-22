package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSelfReviewInstaller exercises the shipped guest command rather than a
// Go copy of its rules. The tracked-skill case is the important boundary: a
// repository-owned skill must never be overwritten by sand's staged copy.
func TestSelfReviewInstaller(t *testing.T) {
	script := filepath.Join("..", "..", "roles", "self-review", "files", "self-review-install-skills")
	home := t.TempDir()
	stage := filepath.Join(home, "staged skills")
	repo := filepath.Join(home, "repository with spaces")
	ignore := filepath.Join(home, "custom config", "global ignore")

	for _, name := range []string{"self-review-apply", "self-review-critique", "self-review-guide"} {
		dir := filepath.Join(stage, name, "assets")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeInstallerFixture(t, filepath.Join(stage, name, "SKILL.md"), "staged "+name+"\n")
		writeInstallerFixture(t, filepath.Join(dir, "schema.xsd"), "<xsd/>\n")
	}
	writeInstallerFixture(t, filepath.Join(stage, "unrelated-skill", "SKILL.md"), "not ours\n")

	runInstallerCommand(t, home, "", "git", "init", "-q", "-b", "main", repo)
	tracked := filepath.Join(repo, ".agents", "skills", "self-review-critique", "SKILL.md")
	writeInstallerFixture(t, tracked, "the repository's own copy\n")
	runInstallerCommand(t, home, "", "git", "-C", repo, "add", ".agents")
	runInstallerCommand(t, home, "", "git", "-C", repo, "commit", "-qm", "vendor review skill")

	stale := filepath.Join(repo, ".agents", "skills", "self-review-apply", "SKILL.md")
	writeInstallerFixture(t, stale, "old copy\n")
	runInstallerCommand(t, home, "", "git", "config", "--global", "core.excludesFile", ignore)

	out := runInstallerCommand(t, home, stage, "sh", script, repo)
	for _, want := range []string{
		"installed=self-review-apply",
		"skipped=self-review-critique",
		"installed=self-review-guide",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("installer output does not contain %q:\n%s", want, out)
		}
	}

	if got := readInstallerFixture(t, tracked); got != "the repository's own copy\n" {
		t.Errorf("tracked skill was overwritten: %q", got)
	}
	if got := readInstallerFixture(t, stale); got != "staged self-review-apply\n" {
		t.Errorf("stale skill was not refreshed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(repo, ".agents", "skills", "unrelated-skill")); !os.IsNotExist(err) {
		t.Errorf("unrelated staged skill was installed: %v", err)
	}

	patterns := []string{
		"review.xml",
		"review.guide.xml",
		".agents/skills/self-review-apply/",
		".agents/skills/self-review-critique/",
		".agents/skills/self-review-guide/",
	}
	ignored := readInstallerFixture(t, ignore)
	for _, pattern := range patterns {
		if strings.Count(ignored, pattern+"\n") != 1 {
			t.Errorf("global excludes contain %q %d times, want once:\n%s", pattern, strings.Count(ignored, pattern+"\n"), ignored)
		}
	}
	runInstallerCommand(t, home, "", "git", "-C", repo, "check-ignore", "-q", "review.xml")
	runInstallerCommand(t, home, "", "git", "-C", repo, "check-ignore", "-q", ".agents/skills/self-review-apply/SKILL.md")

	// A second run is idempotent: untracked copies already matching the staged
	// source are reported current and the excludes are not duplicated.
	out = runInstallerCommand(t, home, stage, "sh", script, repo)
	for _, want := range []string{"current=self-review-apply", "current=self-review-guide"} {
		if !strings.Contains(out, want) {
			t.Errorf("second installer output does not contain %q:\n%s", want, out)
		}
	}
	if got := readInstallerFixture(t, ignore); got != ignored {
		t.Errorf("second install changed global excludes:\nbefore: %q\nafter:  %q", ignored, got)
	}
}

func runInstallerCommand(t *testing.T, home, stage, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	if stage != "" {
		cmd.Env = append(cmd.Env, "SAND_SELF_REVIEW_SKILLS_DIR="+stage)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

func writeInstallerFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readInstallerFixture(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
