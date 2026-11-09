package provision

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// reviewSkillsGuest is a guestRunner that records every Shell argv and fails
// the calls whose joined argv contains failSubstr.
type reviewSkillsGuest struct {
	calls      [][]string
	failSubstr string
}

func (g *reviewSkillsGuest) Shell(_ context.Context, _ string, stdin io.Reader, _ io.Writer, argv ...string) error {
	if stdin != nil {
		_, _ = io.Copy(io.Discard, stdin)
	}
	g.calls = append(g.calls, argv)
	if g.failSubstr != "" && strings.Contains(strings.Join(argv, " "), g.failSubstr) {
		return errors.New("exit status 1")
	}
	return nil
}

func (g *reviewSkillsGuest) ShellStreamOut(ctx context.Context, name string, stdin io.Reader, out io.Writer, argv ...string) error {
	return g.Shell(ctx, name, stdin, out, argv...)
}

func (g *reviewSkillsGuest) ShellOut(context.Context, string, ...string) ([]byte, error) {
	return nil, nil
}

// refreshCalls returns the recorded calls that ran the refresh script.
func (g *reviewSkillsGuest) refreshCalls() [][]string {
	var got [][]string
	for _, c := range g.calls {
		for _, a := range c {
			if a == refreshReviewSkillsScript {
				got = append(got, c)
				break
			}
		}
	}
	return got
}

func TestRefreshReviewSkillsArgvPerPlan(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plan  PreservePlan
		depth string
		roots []string
	}{
		{"project", PreservePlan{Project: ProjectPlan{OrgRel: "github.com/org", Staged: true, RestoresCheckout: true}}, "3", []string{"github.com/org"}},
		{"extras", PreservePlan{Extras: []string{"src/app", "gitlab.com/g/s/repo"}}, "3", []string{"src/app", "gitlab.com/g/s/repo"}},
		{"project and extras", PreservePlan{Project: ProjectPlan{OrgRel: "github.com/org", Staged: true}, Extras: []string{"src/app"}}, "3", []string{"github.com/org", "src/app"}},
		{"whole home", PreservePlan{WholeHome: true, Project: ProjectPlan{OrgRel: "github.com/org"}}, "5", []string{"."}},
		{"none", PreservePlan{}, "", nil},
		{"agents only", PreservePlan{Agents: true}, "", nil},
		{"project not staged", PreservePlan{Project: ProjectPlan{OrgRel: "github.com/org"}}, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &reviewSkillsGuest{}
			var out bytes.Buffer
			RefreshReviewSkills(context.Background(), g, "dev", "/home/tester", "tester", tc.plan, &out)
			if tc.roots == nil {
				if len(g.calls) != 0 || out.Len() != 0 {
					t.Fatalf("plan with no restored checkout ran %v and printed %q", g.calls, out.String())
				}
				return
			}
			if len(g.calls) != 1 {
				t.Fatalf("calls = %v, want exactly one", g.calls)
			}
			want := append([]string{
				"sudo", "-u", "tester", "--", "env", "HOME=/home/tester",
				"bash", "-c", refreshReviewSkillsScript, "sand-review-skills",
				"/home/tester", tc.depth,
			}, tc.roots...)
			if !reflect.DeepEqual(g.calls[0], want) {
				t.Fatalf("argv = %q\nwant   %q", g.calls[0], want)
			}
			if !strings.Contains(out.String(), "==> Refreshing review skills") {
				t.Errorf("no progress line: %q", out.String())
			}
		})
	}
}

// The plan's paths reach the guest as argv only. A root carrying shell syntax
// must appear verbatim as its own element and never inside the script text.
func TestRefreshReviewSkillsNeverInterpolatesPaths(t *testing.T) {
	hostile := "src/$(touch pwned);`id`"
	g := &reviewSkillsGuest{}
	RefreshReviewSkills(context.Background(), g, "dev", "/home/tester", "tester", PreservePlan{Extras: []string{hostile}}, io.Discard)
	argv := g.calls[0]
	if argv[len(argv)-1] != hostile {
		t.Fatalf("hostile root not passed verbatim as the last argument: %q", argv)
	}
	if strings.Contains(refreshReviewSkillsScript, "pwned") {
		t.Fatal("path leaked into the script text")
	}
}

func TestRefreshReviewSkillsFailureIsAWarning(t *testing.T) {
	g := &reviewSkillsGuest{failSubstr: "sand-review-skills"}
	var out bytes.Buffer
	RefreshReviewSkills(context.Background(), g, "dev", "/home/tester", "tester", PreservePlan{Extras: []string{"src/app"}}, &out)
	for _, want := range []string{"==> Warning: skills not refreshed", "exit status 1", "self-review-install-skills <checkout>"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("warning output missing %q:\n%s", want, out.String())
		}
	}
	// The tile shows ~36 columns of the latest ==> line.
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "==> ") && len([]rune(strings.TrimPrefix(line, "==> "))) > 36 {
			t.Errorf("progress line wider than a tile: %q", line)
		}
	}
}

// A refresh failure must not fail the restore it follows: by then the old VM
// is gone and the data is back.
func TestRestoreAfterFinalizeRefreshesSkillsWithoutFailing(t *testing.T) {
	stage := &StageGuard{dir: t.TempDir(), name: "dev"}
	stage.DestroyingGuest()
	for _, archive := range []string{projectArchive, extrasArchive} {
		if err := os.WriteFile(stage.Path(archive), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plan := PreservePlan{Project: ProjectPlan{OrgRel: "github.com/org", Staged: true, RestoresCheckout: true}, Extras: []string{"src/app"}}
	g := &reviewSkillsGuest{failSubstr: "sand-review-skills"}
	var out bytes.Buffer
	if err := RestoreAfterFinalize(context.Background(), g, "dev", "/home/tester", "tester", plan, stage, &out); err != nil {
		t.Fatalf("RestoreAfterFinalize = %v, want nil despite the refresh failing", err)
	}
	calls := g.refreshCalls()
	if len(calls) != 1 {
		t.Fatalf("refresh calls = %d, want 1: %v", len(calls), g.calls)
	}
	// The refresh runs after both extractions, so it sees the restored trees.
	if last := g.calls[len(g.calls)-1]; !reflect.DeepEqual(last, calls[0]) {
		t.Fatalf("refresh was not the last guest call: %q", last)
	}
	if !strings.Contains(out.String(), "Warning: skills not refreshed") {
		t.Errorf("no warning printed:\n%s", out.String())
	}
}

// TestRefreshReviewSkillsScript runs the literal guest script against real Git
// repositories, with the shipped installer reached through a PATH stub.
func TestRefreshReviewSkillsScript(t *testing.T) {
	for _, tool := range []string{"bash", "git", "find", "sort"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available: %v", tool, err)
		}
	}
	installer, err := filepath.Abs(filepath.Join("..", "..", "roles", "self-review", "files", "self-review-install-skills"))
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home dir")
	outside := filepath.Join(tmp, "outside")
	stage := filepath.Join(tmp, "staged")
	bin := filepath.Join(tmp, "bin")
	for _, name := range []string{"self-review-apply", "self-review-guide"} {
		writeInstallerFixture(t, filepath.Join(stage, name, "SKILL.md"), "new "+name+"\n")
	}
	writeInstallerFixture(t, filepath.Join(bin, reviewSkillsCommand), "#!/bin/sh\nexec sh '"+installer+"' \"$@\"\n")
	if err := os.Chmod(filepath.Join(bin, reviewSkillsCommand), 0o755); err != nil {
		t.Fatal(err)
	}

	newRepo := func(dir string) {
		t.Helper()
		runInstallerCommand(t, home, "", "git", "init", "-q", "-b", "main", dir)
	}
	skillFile := func(repo, name string) string {
		return filepath.Join(repo, ".agents", "skills", name, "SKILL.md")
	}

	// A checkout with a stale sandbar-installed copy: refreshed.
	stale := filepath.Join(home, "github.com", "org", "stale")
	newRepo(stale)
	writeInstallerFixture(t, skillFile(stale, "self-review-apply"), "old\n")

	// A checkout that never had the skills: untouched.
	plain := filepath.Join(home, "github.com", "org", "plain")
	newRepo(plain)
	writeInstallerFixture(t, filepath.Join(plain, "README"), "hi\n")

	// A checkout whose only copy is tracked by Git: the repository's own.
	vendored := filepath.Join(home, "github.com", "org", "vendored")
	newRepo(vendored)
	writeInstallerFixture(t, skillFile(vendored, "self-review-apply"), "vendored\n")
	runInstallerCommand(t, home, "", "git", "-C", vendored, "add", ".agents")
	runInstallerCommand(t, home, "", "git", "-C", vendored, "commit", "-qm", "vendor")

	// A stale checkout reachable only through a symlink: not followed.
	linked := filepath.Join(outside, "linked")
	newRepo(linked)
	writeInstallerFixture(t, skillFile(linked, "self-review-apply"), "old\n")
	if err := os.Symlink(linked, filepath.Join(home, "github.com", "org", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "outside-link")); err != nil {
		t.Fatal(err)
	}

	// A stale checkout under ~/.cache: pruned.
	cached := filepath.Join(home, ".cache", "repo")
	newRepo(cached)
	writeInstallerFixture(t, skillFile(cached, "self-review-apply"), "old\n")

	run := func(t *testing.T, path string, roots ...string) (string, error) {
		t.Helper()
		args := append([]string{"-c", refreshReviewSkillsScript, "sand-review-skills", home, "5"}, roots...)
		cmd := exec.Command("bash", args...)
		cmd.Env = append(os.Environ(),
			"PATH="+path,
			"HOME="+home,
			"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
			"GIT_CONFIG_NOSYSTEM=1",
			"SAND_SELF_REVIEW_SKILLS_DIR="+stage,
		)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	sysPath := os.Getenv("PATH")

	t.Run("installer missing", func(t *testing.T) {
		// An empty PATH: bash itself is resolved by the test, and the script
		// must stop at its command -v probe before needing anything else.
		out, err := run(t, t.TempDir(), ".")
		if err != nil || out != "" {
			t.Fatalf("missing installer: out=%q err=%v, want a quiet success", out, err)
		}
		if got := readInstallerFixture(t, skillFile(stale, "self-review-apply")); got != "old\n" {
			t.Fatalf("stale copy changed without an installer: %q", got)
		}
	})

	t.Run("symlinked root", func(t *testing.T) {
		out, err := run(t, bin+string(os.PathListSeparator)+sysPath, "outside-link", "github.com/org/link")
		if err != nil || out != "" {
			t.Fatalf("symlinked roots: out=%q err=%v, want nothing done", out, err)
		}
	})

	t.Run("whole home", func(t *testing.T) {
		out, err := run(t, bin+string(os.PathListSeparator)+sysPath, ".")
		if err != nil {
			t.Fatalf("refresh failed: %v\n%s", err, out)
		}
		if want := "skills refreshed: ~/github.com/org/stale\n"; out != want {
			t.Fatalf("output = %q, want %q", out, want)
		}
		if got := readInstallerFixture(t, skillFile(stale, "self-review-apply")); got != "new self-review-apply\n" {
			t.Errorf("stale copy not refreshed: %q", got)
		}
		if got, err := os.Readlink(filepath.Join(stale, ".claude", "skills", "self-review-apply")); err != nil || got != "../../.agents/skills/self-review-apply" {
			t.Errorf("Claude link = %q, %v", got, err)
		}
		if _, err := os.Stat(filepath.Join(plain, ".agents")); !os.IsNotExist(err) {
			t.Errorf("skills were installed into a checkout that never had them: %v", err)
		}
		if got := readInstallerFixture(t, skillFile(vendored, "self-review-apply")); got != "vendored\n" {
			t.Errorf("tracked copy overwritten: %q", got)
		}
		if _, err := os.Lstat(filepath.Join(vendored, ".claude")); !os.IsNotExist(err) {
			t.Errorf("a vendored-only checkout was treated as sandbar's: %v", err)
		}
		for _, repo := range []string{linked, cached} {
			if got := readInstallerFixture(t, skillFile(repo, "self-review-apply")); got != "old\n" {
				t.Errorf("%s was refreshed through a symlink or cache: %q", repo, got)
			}
		}
		if ignore := readInstallerFixture(t, filepath.Join(home, ".config", "git", "ignore")); !strings.Contains(ignore, ".agents/skills/self-review-apply/\n") {
			t.Errorf("global excludes not written under HOME:\n%s", ignore)
		}
	})

	t.Run("failure is reported and others continue", func(t *testing.T) {
		writeInstallerFixture(t, skillFile(stale, "self-review-apply"), "old again\n")
		other := filepath.Join(home, "src", "other")
		newRepo(other)
		writeInstallerFixture(t, skillFile(other, "self-review-guide"), "old\n")
		failing := filepath.Join(tmp, "failbin")
		writeInstallerFixture(t, filepath.Join(failing, reviewSkillsCommand),
			"#!/bin/sh\ncase \"$1\" in */stale) echo boom >&2; exit 1 ;; esac\nexec sh '"+installer+"' \"$@\"\n")
		if err := os.Chmod(filepath.Join(failing, reviewSkillsCommand), 0o755); err != nil {
			t.Fatal(err)
		}
		out, err := run(t, failing+string(os.PathListSeparator)+sysPath, "github.com/org", "src/other")
		if err == nil {
			t.Fatalf("a failed checkout must fail the script so the caller warns:\n%s", out)
		}
		if !strings.Contains(out, "skills not refreshed: ~/github.com/org/stale: boom") || !strings.Contains(out, "skills refreshed: ~/src/other") {
			t.Errorf("output = %q", out)
		}
		if got := readInstallerFixture(t, skillFile(other, "self-review-guide")); got != "new self-review-guide\n" {
			t.Errorf("other checkout not refreshed after a failure: %q", got)
		}
	})
}
