package provision

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
)

// hostCmdRunner is a fakeRunner that can also run host commands. Each one is
// recorded into the same call sequence, prefixed "host:", so a test can see
// where it fell relative to the guest calls.
type hostCmdRunner struct {
	*fakeRunner
	hostErr error
}

func (r hostCmdRunner) HostOutput(_ context.Context, argv ...string) ([]byte, error) {
	r.record(append([]string{"host:"}, argv...), nil)
	return nil, r.hostErr
}

// isPlaybookRun reports whether a recorded call is a playbook run: `shell <name>
// sudo bash -c <script>`, as opposed to a one-line probe.
func isPlaybookRun(call []string) bool {
	return len(call) > 0 && call[0] == "shell" && slices.Contains(call, "sudo") && slices.Contains(call, "-c")
}

// TestCreateVMRenewsGuestLoginAfterFinalize pins the fix for a new VM's first
// shell lacking the `docker` group: finalize grants it over Lima's shared ssh
// master, whose guest login predates the grant, so the master must be stopped
// once the playbook has run, or every `limactl shell` inherits the old groups.
func TestCreateVMRenewsGuestLoginAfterFinalize(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LIMA_HOME", home)
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(hostCmdRunner{fakeRunner: f}), PlaybookDir: "/playbook"}
	cfg := testConfig()

	if err := p.CreateVM(context.Background(), cfg, io.Discard); err != nil {
		t.Fatalf("CreateVM: %v", err)
	}

	calls := f.snapshot()
	lastPlaybook, stop := -1, -1
	for i, c := range calls {
		if isPlaybookRun(c) {
			lastPlaybook = i
		}
		if len(c) > 0 && c[0] == "host:" {
			stop = i
		}
	}
	if lastPlaybook < 0 {
		t.Fatalf("no playbook run recorded; calls:\n%v", calls)
	}
	if stop < lastPlaybook {
		t.Fatalf("ssh master not stopped after the finalize playbook (stop at %d, playbook at %d); calls:\n%v", stop, lastPlaybook, calls)
	}
	want := append([]string{"host:"}, lima.StopSSHMasterArgv(filepath.Join(home, cfg.Name))...)
	if !slices.Equal(calls[stop], want) {
		t.Errorf("stop call = %q, want %q", calls[stop], want)
	}
}

// TestRenewGuestLoginReportsFailureWithoutFailing checks the stop is
// best-effort: a failure is reported in the build output, never as an error,
// because the VM is provisioned either way.
func TestRenewGuestLoginReportsFailureWithoutFailing(t *testing.T) {
	t.Setenv("LIMA_HOME", t.TempDir())
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := &Provisioner{Lima: lima.New(hostCmdRunner{fakeRunner: f, hostErr: errors.New("no master")}), PlaybookDir: "/playbook"}

	var out bytes.Buffer
	if err := p.CreateVM(context.Background(), testConfig(), &out); err != nil {
		t.Fatalf("CreateVM failed on a best-effort ssh reset: %v", err)
	}
	if !strings.Contains(out.String(), "could not reset the ssh connection") {
		t.Errorf("failure not reported; output:\n%s", out.String())
	}
}

// TestRenewGuestLoginSkippedWhenPlaybookFails keeps the stop after the playbook's
// success: a failed run returns its error unchanged and touches nothing else.
func TestRenewGuestLoginSkippedWhenPlaybookFails(t *testing.T) {
	t.Setenv("LIMA_HOME", t.TempDir())
	f := &fakeRunner{failOn: isPlaybookRun}
	p := &Provisioner{Lima: lima.New(hostCmdRunner{fakeRunner: f}), PlaybookDir: "/playbook"}

	if err := p.runProvision(context.Background(), "claude", "finalize", "claude", testConfig(), false, io.Discard); err == nil {
		t.Fatal("runProvision succeeded despite a failed playbook")
	}
	for _, c := range f.snapshot() {
		if len(c) > 0 && c[0] == "host:" {
			t.Fatalf("ssh master stopped after a failed playbook: %v", c)
		}
	}
}
