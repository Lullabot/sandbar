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

	"github.com/lullabot/sandbar/internal/lima"
)

func TestPreservePathRel(t *testing.T) {
	const home = "/home/andrew"
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"absolute under home", "/home/andrew/src/app", "src/app", false},
		{"absolute with redundant segments", "/home/andrew/./src//app/", "src/app", false},
		{"tilde", "~/src/app", "src/app", false},
		{"bare relative", "src/app", "src/app", false},
		{"deep worktree", "/home/andrew/src/app/.claude/worktrees/x", "src/app/.claude/worktrees/x", false},
		{"a dotfile directory", "~/.config/nvim", ".config/nvim", false},

		// Every one of these would, if it got through, hand the restore's
		// `chown -R <user>` a path outside the guest home — as root.
		{"outside home", "/etc", "", true},
		{"a sibling home", "/home/andrewsomeoneelse/src", "", true},
		{"climbing out", "../../etc", "", true},
		{"climbing out via home", "~/../../etc", "", true},
		{"absolute climbing out", "/home/andrew/../root", "", true},
		{"the home itself", "/home/andrew", "", true},
		{"the home itself as tilde", "~", "", true},
		{"the home itself as dot", ".", "", true},
		{"empty", "   ", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := preservePathRel(home, tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("preservePathRel(%q) = %q, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("preservePathRel(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("preservePathRel(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPruneCovered(t *testing.T) {
	cases := []struct {
		name    string
		rels    []string
		covered []string
		want    []string
	}{
		{
			name: "a worktree inside a selected repo is dropped",
			rels: []string{"src/app", "src/app/.claude/worktrees/x"},
			want: []string{"src/app"},
		},
		{
			name: "siblings are both kept",
			rels: []string{"src/app", "src/api"},
			want: []string{"src/api", "src/app"},
		},
		{
			name: "duplicates collapse",
			rels: []string{"src/app", "src/app"},
			want: []string{"src/app"},
		},
		{
			name:    "anything under the project's org directory is already covered",
			rels:    []string{"github.com/octocat/repo", "src/app"},
			covered: []string{"github.com/octocat"},
			want:    []string{"src/app"},
		},
		{
			name: "a prefix that is not a path boundary is not containment",
			rels: []string{"src/app", "src/app-two"},
			want: []string{"src/app", "src/app-two"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pruneCovered(tc.rels, tc.covered)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("pruneCovered(%v, %v) = %v, want %v", tc.rels, tc.covered, got, tc.want)
			}
		})
	}
}

// TestStageOutToleratesAChangingFile: the source VM is RUNNING while its data is
// copied out — that is where the data is — so a log line appended mid-read is
// ordinary, and GNU tar reports it as exit status 1 with a complete archive. A
// reset that failed on it would mean "preserve my home" only ever worked on an
// idle VM.
func TestStageOutToleratesAChangingFile(t *testing.T) {
	f := &fakeRunner{
		failOn:  isTarOut,
		failErr: errAbsent, // a real exit-1 ExitError, as tar's "file changed" is
	}
	var out bytes.Buffer
	archive := t.TempDir() + "/home.tgz"
	if err := StageOut(context.Background(), lima.New(f), "web", "/home/andrew", []string{"."}, archive, &out); err != nil {
		t.Fatalf("StageOut: %v", err)
	}
	if !strings.Contains(out.String(), "changed while they were being copied out") {
		t.Errorf("the run never mentioned the changed files:\n%s", out.String())
	}
}

// ...but every other status is a real failure, and must abort while the guest is
// still intact.
func TestStageOutFailsOnARealTarError(t *testing.T) {
	f := &fakeRunner{
		failOn:  isTarOut,
		failErr: errors.New("exit status 2: tar: Cannot write: No space left on device"),
	}
	archive := t.TempDir() + "/home.tgz"
	err := StageOut(context.Background(), lima.New(f), "web", "/home/andrew", []string{"."}, archive, io.Discard)
	if err == nil {
		t.Fatal("a failed tar was reported as a successful stage-out")
	}
}

// isTarExclude reports a stage-out that excluded the guest's authorized_keys.
func isTarExclude(c []string) bool {
	return isTarOut(c) && hasTok(c, "--exclude=./.ssh/authorized_keys")
}

// TestReset_PreserveHome is the whole-home path end to end over a fake guest:
// ONE archive of ~, restored BEFORE finalize so the playbook gets the last word
// over the files it owns, with the project clone skipped because the checkout is
// coming back inside that archive.
func TestReset_PreserveHome(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.User = "andrew"
	cfg.CloneURL = "https://github.com/lullabot/sandbar"

	if err := p.Reset(context.Background(), cfg, ResetOptions{PreserveHome: true}, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	// Exactly one archive out and one back: a whole home already contains the
	// Claude login and the project, so staging either separately would copy the
	// same bytes twice.
	if n := countCalls(f.calls, isTarOut); n != 1 {
		t.Errorf("%d archives staged out, want exactly 1 (the home)", n)
	}
	if n := countCalls(f.calls, isTarIn); n != 1 {
		t.Errorf("%d archives restored, want exactly 1 (the home)", n)
	}
	if countCalls(f.calls, isTarExclude) != 1 {
		t.Errorf("the home archive did not exclude ~/.ssh/authorized_keys; calls=%v", f.calls)
	}

	// The restore must land before the playbook, not after it.
	restore := findCall(t, f.calls, 0, "the home restore", isTarIn)
	finalize := findCall(t, f.calls, 0, "the finalize playbook", func(c []string) bool {
		return hasTok(c, "shell") && hasTok(c, "bash")
	})
	if restore > finalize {
		t.Errorf("the home was restored at call %d, after finalize at %d: the playbook can no longer re-apply the files it owns", restore, finalize)
	}

	// And the role must not clone over the checkout the restore just put back.
	if strings.Contains(finalizeStream(t, f.streams), "project_clone_url") {
		t.Error("finalize would clone over the restored checkout (project_clone_url was passed)")
	}
}

// TestReset_PreserveHomeStillClonesWhenTheCheckoutIsGone: skipping the clone is
// justified only by a checkout that is actually coming back. A home with no
// checkout in it must still get the repo cloned, or the reset leaves the user
// with neither — the same rule PlanProject enforces for the project toggle.
func TestReset_PreserveHomeStillClonesWhenTheCheckoutIsGone(t *testing.T) {
	f := &fakeRunner{
		status:  map[string][]byte{"sandbar-base": []byte("Stopped\n")},
		failOn:  absentPath,
		failErr: errAbsent,
	}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.User = "andrew"
	cfg.CloneURL = "https://github.com/lullabot/sandbar"

	if err := p.Reset(context.Background(), cfg, ResetOptions{PreserveHome: true}, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if !strings.Contains(finalizeStream(t, f.streams), "project_clone_url") {
		t.Error("a home with no checkout in it must still let finalize clone the repo")
	}
}

// TestReset_PreservePathsStagesAndRestores: a hand-picked checkout — one sand
// never cloned and knows nothing about — is archived from the running guest and
// put back AFTER finalize, where the playbook cannot touch it.
func TestReset_PreservePathsStagesAndRestores(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.User = "andrew"

	opts := ResetOptions{PreservePaths: []string{"/home/andrew/src/app"}}
	if err := p.Reset(context.Background(), cfg, opts, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	staged := false
	for _, c := range f.calls {
		if isTarOut(c) && hasTok(c, "src/app") {
			staged = true
		}
	}
	if !staged {
		t.Fatalf("the hand-picked checkout was never staged out; calls=%v", f.calls)
	}
	restore := findCall(t, f.calls, 0, "the extras restore", isTarIn)
	finalize := findCall(t, f.calls, 0, "the finalize playbook", func(c []string) bool {
		return hasTok(c, "shell") && hasTok(c, "bash")
	})
	if restore < finalize {
		t.Errorf("the checkout was restored at call %d, before finalize at %d: the playbook could write over the user's work", restore, finalize)
	}
	// The restore must re-own the extracted tree, and by its home-relative path.
	chowned := false
	for _, c := range f.calls {
		if hasTok(c, "chown") && hasTok(c, "/home/andrew/src/app") {
			chowned = true
		}
	}
	if !chowned {
		t.Errorf("the restored checkout was left owned by root; calls=%v", f.calls)
	}
}

// TestReset_PreservePathOutsideHomeIsRefusedBeforeTheDelete is the safety half of
// the feature. The paths come from a sweep of the GUEST — the lowest-trust source
// in the system — and reach a `chown -R` that runs as root, so one that escapes
// the home must stop the reset. It must stop it HERE, with the VM untouched, so
// the answer is "pick again" rather than "your VM is gone and your data with it".
func TestReset_PreservePathOutsideHomeIsRefusedBeforeTheDelete(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.User = "andrew"

	opts := ResetOptions{PreservePaths: []string{"/home/andrew/../../etc"}}
	err := p.Reset(context.Background(), cfg, opts, io.Discard)
	if err == nil {
		t.Fatal("a preserve path outside the guest home was accepted")
	}
	for _, c := range f.calls {
		if hasTok(c, "delete") {
			t.Fatalf("the VM was deleted despite the refused path: %v", c)
		}
	}
	if n := countCalls(f.calls, isTarOut); n != 0 {
		t.Errorf("%d archives were staged before the path was refused", n)
	}
	if dirs := stageDirs(t); len(dirs) != 0 {
		t.Errorf("a staging directory was left behind for a reset that never started: %v", dirs)
	}
}

// TestReset_PreservePathGoneIsANoteNotAFailure: these paths come from a cache of
// the last sweep, so one can name a directory the user has since deleted. That is
// not an error — there is nothing to lose — and the reset says so and carries on.
func TestReset_PreservePathGoneIsANoteNotAFailure(t *testing.T) {
	f := &fakeRunner{
		status:  map[string][]byte{"sandbar-base": []byte("Stopped\n")},
		failOn:  absentPath,
		failErr: errAbsent,
	}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.User = "andrew"

	var out bytes.Buffer
	opts := ResetOptions{PreservePaths: []string{"/home/andrew/src/gone"}}
	if err := p.Reset(context.Background(), cfg, opts, &out); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if n := countCalls(f.calls, isTarOut); n != 0 {
		t.Errorf("%d archives staged for a path that is not there", n)
	}
	if !strings.Contains(out.String(), "no ~/src/gone any more") {
		t.Errorf("the run never said the path had gone:\n%s", out.String())
	}
}

// TestReset_PreservePathInsideTheProjectIsNotStagedTwice: the project toggle
// already keeps the whole per-org directory, so a checkout inside it must not be
// archived a second time.
func TestReset_PreservePathInsideTheProjectIsNotStagedTwice(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.User = "andrew"
	cfg.CloneURL = "https://github.com/lullabot/sandbar"

	opts := ResetOptions{
		PreserveProject: true,
		PreservePaths:   []string{"/home/andrew/github.com/lullabot/sandbar"},
	}
	if err := p.Reset(context.Background(), cfg, opts, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if n := countCalls(f.calls, isTarOut); n != 1 {
		t.Errorf("%d archives staged out, want 1: the checkout is inside the preserved org directory", n)
	}
}

// TestStageBaseDirIsDiskBacked: a staged archive is the only copy of the user's
// work while their VM is being rebuilt, and no later error deletes it — so it
// must not live in a tmpfs, which /tmp is by default on current Debian. The
// default is XDG_STATE_HOME; an explicit TMPDIR still wins, because a user who
// set one has already said where their large temporary files go.
func TestStageBaseDirIsDiskBacked(t *testing.T) {
	state := t.TempDir()
	t.Setenv("TMPDIR", "")
	t.Setenv("XDG_STATE_HOME", state)

	got := stageBaseDir()
	want := filepath.Join(state, "sandbar", "staging")
	if got != want {
		t.Fatalf("stageBaseDir() = %q, want %q", got, want)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Fatalf("stageBaseDir did not create %s: %v", want, err)
	}
	// And a real staging directory lands inside it.
	guard, err := NewStageGuard()
	if err != nil {
		t.Fatalf("NewStageGuard: %v", err)
	}
	defer guard.Done()
	if !strings.HasPrefix(guard.Dir(), want) {
		t.Errorf("staging dir %q is not under %q", guard.Dir(), want)
	}

	t.Setenv("TMPDIR", t.TempDir())
	if got := stageBaseDir(); got != "" {
		t.Errorf("with TMPDIR set, stageBaseDir() = %q, want \"\" (os.MkdirTemp's own default)", got)
	}
}

// exitOne is a sanity check on errAbsent itself: the "file changed"/"not there"
// tolerance in this package keys on tar's and test's exit STATUS, so a fake that
// did not actually carry one would make those tests pass for the wrong reason.
func TestErrAbsentCarriesExitStatusOne(t *testing.T) {
	var ee *exec.ExitError
	if !errors.As(errAbsent, &ee) || ee.ExitCode() != 1 {
		t.Fatalf("errAbsent = %v, want an *exec.ExitError with code 1", errAbsent)
	}
	if !tarFilesChanged(errAbsent) {
		t.Error("tarFilesChanged did not recognise an exit-status-1 error")
	}
}
