package provision

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
)

func isFinalize(c []string) bool {
	return hasTok(c, "shell") && strings.Contains(strings.Join(c, " "), "ansible-playbook")
}

// A reset that staged a backup hands its clone a RebuildID and records the
// same ID beside the backup — the pairing that lets a later resume recognise
// the half-built replacement as its own. The failure after the delete comes
// back as a StagedResetError naming the backup.
func TestReset_RecordsTheRebuildIDItsCloneCarries(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	f := &fakeRunner{
		status:  map[string][]byte{"sandbar-base": []byte("Stopped\n"), "claude": []byte("Running\n")},
		failOn:  isFinalize,
		failErr: errors.New("exit status 2"),
	}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	var got []string
	opts := ResetOptions{PreserveAgents: true, OnCloned: func(_ context.Context, id string) error {
		got = append(got, id)
		return nil
	}}
	err := p.Reset(context.Background(), testConfig(), opts, io.Discard)

	var staged *StagedResetError
	if !errors.As(err, &staged) {
		t.Fatalf("want a StagedResetError, got %v", err)
	}
	if len(got) != 1 || got[0] == "" {
		t.Fatalf("OnCloned got %q, want exactly one non-empty RebuildID", got)
	}
	recorded, rerr := RebuildID(staged.Dir)
	if rerr != nil || recorded != got[0] {
		t.Errorf("RebuildID(%s) = %q, %v; want the clone's %q", staged.Dir, recorded, rerr, got[0])
	}
}

// With nothing staged there is no backup to resume from, so the clone is
// marked in flight (the fix for adoption stamping a half-built VM ready) with
// no RebuildID.
func TestReset_NoStageStillMarksTheCloneInFlight(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n"), "claude": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	var got []string
	opts := ResetOptions{OnCloned: func(_ context.Context, id string) error {
		got = append(got, id)
		return nil
	}}
	if err := p.Reset(context.Background(), testConfig(), opts, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if len(got) != 1 || got[0] != "" {
		t.Errorf("OnCloned got %q, want one call with no RebuildID", got)
	}
}

// A marker that could not be written must not leave a record behind: a resume
// would trust a token no VM carries.
func TestMarkRebuildCloned_NoRecordWithoutTheMarker(t *testing.T) {
	stage := &StageGuard{dir: t.TempDir()}
	opts := ResetOptions{OnCloned: func(context.Context, string) error { return errors.New("permission denied") }}
	var out strings.Builder
	MarkRebuildCloned(context.Background(), stage, opts, "claude", &out)

	if id, err := RebuildID(stage.Dir()); err != nil || id != "" {
		t.Errorf("RebuildID = %q, %v; want nothing recorded", id, err)
	}
	if !strings.Contains(out.String(), "could not write the in-flight provenance marker") {
		t.Errorf("the failed marker write was not reported:\n%s", out.String())
	}
}

// A clone that never came up holds none of the user's data yet, so it is
// removed — as a failed create's is — rather than left for a resume to trip
// over. The backup is still kept.
func TestReset_RemovesAReplacementThatNeverBooted(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	limaHome := t.TempDir()
	t.Setenv("LIMA_HOME", limaHome)
	f := &fakeRunner{
		status:  map[string][]byte{"sandbar-base": []byte("Stopped\n"), "claude": []byte("Running\n")},
		failOn:  func(c []string) bool { return c[0] == "start" && c[1] == "claude" },
		failErr: errors.New("boot timed out"),
	}
	// The clone is what creates the instance directory cleanup looks for.
	f.hook = func(c []string) {
		if c[0] == "clone" {
			_ = os.MkdirAll(filepath.Join(limaHome, "claude"), 0o700)
		}
	}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	err := p.Reset(context.Background(), testConfig(), ResetOptions{PreserveAgents: true}, io.Discard)
	var staged *StagedResetError
	if !errors.As(err, &staged) {
		t.Fatalf("want a StagedResetError, got %v", err)
	}
	clone := findCall(t, f.calls, 0, "the clone", func(c []string) bool { return c[0] == "clone" })
	findCall(t, f.calls, clone, "the cleanup delete", func(c []string) bool { return c[0] == "delete" && c[1] == "claude" })
	if _, serr := os.Stat(staged.Dir); serr != nil {
		t.Errorf("the backup was removed along with the clone: %v", serr)
	}
}

// A rerun of a reset whose VM is already gone builds it fresh instead of
// failing on the delete — the state a retry after a dropped connection finds.
func TestReset_InstanceAlreadyGoneIsBuiltFresh(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	if err := p.Reset(context.Background(), testConfig(), ResetOptions{}, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	for _, c := range f.calls {
		if c[0] == "delete" {
			t.Fatalf("deleted an instance that does not exist: %v", c)
		}
	}
	findCall(t, f.calls, 0, "the clone", func(c []string) bool { return c[0] == "clone" })
}

// A resume that cannot tell whether the name is free does not build: the
// clone-failure cleanup would otherwise be free to remove an instance it
// merely failed to see.
func TestReset_RestoreFromRefusesWhenExistenceIsUnknown(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	f := &fakeRunner{
		status:  map[string][]byte{"sandbar-base": []byte("Stopped\n")},
		failOn:  func(c []string) bool { return c[0] == "list" && c[1] == "claude" },
		failErr: errors.New("exit status 255"),
	}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}
	cfg := testConfig()
	m := NewResetManifest(cfg, PreservePlan{WholeHome: true}, ResetOptions{})
	dir := writeBackup(t, &m, homeArchive)

	err := p.Reset(context.Background(), cfg, ResetOptions{RestoreFrom: dir}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "check whether") {
		t.Fatalf("want a refusal naming the failed existence check, got %v", err)
	}
	for _, c := range f.calls {
		if c[0] == "clone" || c[0] == "delete" {
			t.Fatalf("a resume that could not check the name touched the host: %v", c)
		}
	}
}
