package provision

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
)

// absentPath makes the guest report every `sudo test -e` probe as "not there",
// which is how Reset asks whether the project directory it was told to preserve
// actually exists.
func absentPath(c []string) bool { return hasTok(c, "test") && hasTok(c, "-e") }

// TestReset_PreserveProjectWithNothingThere is the regression test for a reset
// that left the VM with NO PROJECT AT ALL. `tar --ignore-failed-read` produces a
// perfectly good empty archive for a directory that is not there, so a
// preserve-project reset used to stage nothing, skip the finalize clone anyway
// (to protect the tree it believed it had staged), and restore nothing — and it
// reached that state through three ordinary doors: the reset form's clone URL
// edited to a different org, an original clone that failed, or a user who
// deleted the checkout. Observed on a real VM: `~/github.com` empty afterwards,
// with the run reported as failed at `stage in chown` on the path that was never
// created.
func TestReset_PreserveProjectWithNothingThere(t *testing.T) {
	f := &fakeRunner{
		status:  map[string][]byte{"sandbar-base": []byte("Stopped\n")},
		failOn:  absentPath,
		failErr: errAbsent, // a real exit-1: "not there", not "the probe broke"
	}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.User = "andrew"
	cfg.CloneURL = "https://github.com/lullabot/sandbar"

	var out bytes.Buffer
	if err := p.Reset(context.Background(), cfg, ResetOptions{PreserveProject: true}, &out); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if n := countCalls(f.calls, isTarOut); n != 0 {
		t.Errorf("nothing to preserve, yet %d archives were staged out", n)
	}
	if n := countCalls(f.calls, isTarIn); n != 0 {
		t.Errorf("nothing was staged, yet %d archives were restored", n)
	}
	for _, c := range f.calls {
		if hasTok(c, "direnv") {
			t.Errorf("nothing was restored, yet direnv ran: %v", c)
		}
	}
	// The whole point: finalize must still clone the repo the config names.
	if !strings.Contains(finalizeStream(t, f.streams), "project_clone_url") {
		t.Error("with nothing to preserve, finalize must fall back to the role's clone (keep project_clone_url)")
	}
	// And the user is told, rather than left to discover an empty ~/github.com.
	if !strings.Contains(out.String(), "no ~/github.com/lullabot to preserve") {
		t.Errorf("the run never said the project could not be preserved:\n%s", out.String())
	}
}

// TestReset_PreserveProjectWithoutTheCheckout covers the half-there case: the org
// directory survives (it holds the .env the project role wrote, which is the
// token the user would otherwise have to re-supply) but the checkout inside it
// is gone. Both halves must happen — preserve the directory AND clone the repo —
// because doing only the first is the "no project" outcome again.
func TestReset_PreserveProjectWithoutTheCheckout(t *testing.T) {
	f := &fakeRunner{
		status: map[string][]byte{"sandbar-base": []byte("Stopped\n")},
		// The org dir is there; the checkout under it is not.
		failOn:  func(c []string) bool { return absentPath(c) && hasTok(c, "/home/andrew/github.com/lullabot/sandbar") },
		failErr: errAbsent,
	}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.User = "andrew"
	cfg.CloneURL = "https://github.com/lullabot/sandbar"

	if err := p.Reset(context.Background(), cfg, ResetOptions{PreserveProject: true}, io.Discard); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if n := countCalls(f.calls, isTarOut); n != 1 {
		t.Errorf("the org directory was not preserved (%d archives staged out, want 1)", n)
	}
	if n := countCalls(f.calls, isTarIn); n != 1 {
		t.Errorf("the org directory was not restored (%d extracts, want 1)", n)
	}
	if !strings.Contains(finalizeStream(t, f.streams), "project_clone_url") {
		t.Error("a preserved org directory with no checkout in it must still let finalize clone the repo")
	}
}

// TestReset_AbortsWhenTheProbeCannotRun is the guard on the step that comes
// next: Reset DELETES the VM immediately after deciding what to preserve, so a
// probe that could not run must stop the run rather than be read as "there is
// nothing here". Only `test`'s own exit 1 means absent; an ssh transport that
// dropped, or a limactl that could not attach, is an unknown — and acting on it
// would destroy the tree the user asked to keep.
func TestReset_AbortsWhenTheProbeCannotRun(t *testing.T) {
	f := &fakeRunner{
		status:  map[string][]byte{"sandbar-base": []byte("Stopped\n")},
		failOn:  absentPath,
		failErr: errors.New("ssh: connection closed by remote host"),
	}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	cfg := testConfig()
	cfg.User = "andrew"
	cfg.CloneURL = "https://github.com/lullabot/sandbar"

	err := p.Reset(context.Background(), cfg, ResetOptions{PreserveProject: true}, io.Discard)
	if err == nil {
		t.Fatal("an unreadable guest was treated as an answer; the reset went ahead and deleted the VM")
	}
	for _, c := range f.calls {
		if hasTok(c, "delete") {
			t.Fatalf("the VM was deleted despite an unusable probe: %v", c)
		}
	}
}
