package provision

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
)

// errAbsent is a REAL *exec.ExitError carrying status 1 — what `sudo test -e`
// returns for a path that is not there, and the only failure GuestPathExists
// reads as "absent". Anything else means the probe never ran, which it reports
// as an error rather than as a missing path, so a fake that answered with a
// plain errors.New would be testing the wrong branch.
var errAbsent = exec.Command("sh", "-c", "exit 1").Run()

func TestAncestorDirs(t *testing.T) {
	cases := []struct {
		path string
		want []string
	}{
		{".claude", nil},
		{".claude.json", nil},
		{"github.com/octocat", []string{"github.com"}},
		{"a/b/c", []string{"a", "a/b"}},
		{"/github.com/octocat/", []string{"github.com"}},
	}
	for _, tc := range cases {
		if got := ancestorDirs(tc.path); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ancestorDirs(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestStageInCreatesParentsAsTheUser pins the ordering that keeps a restored
// project reachable: the org directory's PARENT is created (or repaired) as the
// user BEFORE the root-run extract, because tar would otherwise create it
// root-owned and the chown that follows only reaches the restored paths
// themselves. Without it, a preserve-project reset left ~/github.com as
// root:root — on a VM where a plain create leaves it owned by the user — and the
// next clone into a sibling org directory failed with "Permission denied".
func TestStageInCreatesParentsAsTheUser(t *testing.T) {
	f := &stagingFakeRunner{}
	cli := lima.New(f)
	archive := filepath.Join(t.TempDir(), "project.tar.gz")
	if err := os.WriteFile(archive, []byte("dummy"), 0o600); err != nil {
		t.Fatalf("seed archive: %v", err)
	}

	if err := StageIn(context.Background(), cli, "claude", "/home/andrew", "andrew", []string{"github.com/octocat"}, archive); err != nil {
		t.Fatalf("StageIn: %v", err)
	}

	want := [][]string{
		{"shell", "claude", "sudo", "install", "-d", "-o", "andrew", "-g", "andrew", "-m", "755", "/home/andrew/github.com"},
		{"shell", "claude", "sudo", "tar", "-C", "/home/andrew", "-xzf", "-"},
		// The chown covers only what the extract actually produced, so each path
		// is probed first (see StageIn).
		{"shell", "claude", "sudo", "test", "-e", "/home/andrew/github.com/octocat"},
		{"shell", "claude", "sudo", "chown", "-R", "andrew:andrew", "/home/andrew/github.com/octocat"},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("StageIn argv = %v, want %v", f.calls, want)
	}
}

// direnvFakeRunner answers `sudo test -e <path>` from a set of paths that exist
// and records every call, so a test can assert both the probe and whether
// direnv itself was reached.
type direnvFakeRunner struct {
	calls  [][]string
	exists map[string]bool
}

func (f *direnvFakeRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	if len(args) > 0 && f.exists[args[len(args)-1]] {
		return nil, nil
	}
	return nil, errAbsent
}

func (f *direnvFakeRunner) Stream(_ context.Context, _ io.Reader, _ io.Writer, args ...string) error {
	f.calls = append(f.calls, args)
	return nil
}

func (f *direnvFakeRunner) StreamOut(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error {
	return f.Stream(ctx, stdin, out, args...)
}

// TestAllowDirenvSkipsWithNothingToApprove is the regression test for a reset
// that failed at its very last step. A VM that cloned a PUBLIC repo has no
// per-org .env — roles/project writes one only when a clone token was supplied —
// and `direnv allow` on a directory holding neither .envrc nor .env exits 1. The
// reset had by then already deleted, re-cloned, finalized and restored the VM,
// so a completely healthy VM was reported as a failed run.
func TestAllowDirenvSkipsWithNothingToApprove(t *testing.T) {
	f := &direnvFakeRunner{}
	cli := lima.New(f)

	if err := AllowDirenv(context.Background(), cli, "claude", "andrew", "/home/andrew/github.com/octocat", io.Discard); err != nil {
		t.Fatalf("AllowDirenv with nothing to approve: %v", err)
	}

	want := [][]string{
		{"shell", "claude", "sudo", "test", "-e", "/home/andrew/github.com/octocat/.envrc"},
		{"shell", "claude", "sudo", "test", "-e", "/home/andrew/github.com/octocat/.env"},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("argv = %v, want just the two probes %v", f.calls, want)
	}
}

// TestAllowDirenvApprovesWhatIsThere is the other half: when there IS something
// to approve, direnv still runs — and either file is enough, since a restored
// checkout may carry a .envrc the user wrote themselves.
func TestAllowDirenvApprovesWhatIsThere(t *testing.T) {
	for _, present := range []string{".envrc", ".env"} {
		t.Run(present, func(t *testing.T) {
			dir := "/home/andrew/github.com/octocat"
			f := &direnvFakeRunner{exists: map[string]bool{dir + "/" + present: true}}
			cli := lima.New(f)

			if err := AllowDirenv(context.Background(), cli, "claude", "andrew", dir, io.Discard); err != nil {
				t.Fatalf("AllowDirenv: %v", err)
			}

			last := f.calls[len(f.calls)-1]
			want := []string{"shell", "claude", "sudo", "-iu", "andrew", "direnv", "allow", dir}
			if !reflect.DeepEqual(last, want) {
				t.Fatalf("last call = %v, want %v", last, want)
			}
		})
	}
}

// TestStageInSkipsTheChownForAPathThatWasNeverThere is the regression test for
// a reset that failed at its very last step with nothing wrong. StageOut's
// --ignore-failed-read drops a top-level path that is absent in the SOURCE VM,
// so it is absent from the archive too — and `chown -R` on a path that is not
// there fails, taking the whole call with it. Any VM whose user had never
// launched Claude Code has no ~/.claude.json, so a preserve-Claude reset of one
// reported a failure over a VM that was entirely fine.
func TestStageInSkipsTheChownForAPathThatWasNeverThere(t *testing.T) {
	home := "/home/andrew"
	f := &direnvFakeRunner{exists: map[string]bool{home + "/.claude": true}}
	cli := lima.New(f)
	archive := filepath.Join(t.TempDir(), "claude.tar.gz")
	if err := os.WriteFile(archive, []byte("dummy"), 0o600); err != nil {
		t.Fatalf("seed archive: %v", err)
	}

	if err := StageIn(context.Background(), cli, "claude", home, "andrew", []string{".claude", ".claude.json"}, archive); err != nil {
		t.Fatalf("StageIn: %v", err)
	}

	last := f.calls[len(f.calls)-1]
	want := []string{"shell", "claude", "sudo", "chown", "-R", "andrew:andrew", home + "/.claude"}
	if !reflect.DeepEqual(last, want) {
		t.Fatalf("chown = %v, want only the path the extract produced: %v", last, want)
	}
}
