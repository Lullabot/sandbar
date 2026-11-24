package provider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/provision"
)

// isStopMaster reports whether argv is an `ssh … -O stop <target>` control
// command.
func isStopMaster(argv []string) bool {
	i := slices.Index(argv, "-O")
	return len(argv) > 0 && argv[0] == "ssh" && i >= 0 && i+1 < len(argv) && argv[i+1] == "stop"
}

// TestProxmoxCreateRenewsGuestLoginAfterFinalize pins the Proxmox half of the fix
// for a new VM's first shell lacking `docker`: finalize grants the group over
// the shared ssh master, so the master must be stopped after the playbook, or
// a shell opened while it persists inherits the login's old groups.
func TestProxmoxCreateRenewsGuestLoginAfterFinalize(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // the control-socket directory
	m := newPVEMock(t)
	registerBaseBuild(m, &createRecorder{})
	p := newCreateProvider(t, m)
	argvs := recordSSH(p)

	if err := p.Create(context.Background(), webConfig(), provision.CreateOptions{}, io.Discard); err != nil {
		t.Fatalf("Create: %v", err)
	}

	finalize, stop := -1, -1
	for i, argv := range *argvs {
		if strings.Contains(strings.Join(argv, " "), "ansible-playbook") {
			finalize = i
		}
		if isStopMaster(argv) {
			stop = i
		}
	}
	if finalize < 0 {
		t.Fatalf("no playbook run recorded: %q", *argvs)
	}
	if stop < finalize {
		t.Fatalf("ssh master not stopped after finalize (stop at %d, playbook at %d): %q", stop, finalize, *argvs)
	}
	// The stop must address the same master the playbook rode: same ControlPath
	// and target, or it stops nothing.
	pick := func(argv []string) (cp, target string) {
		for _, a := range argv {
			if strings.HasPrefix(a, "ControlPath=") {
				cp = a
			}
		}
		return cp, argv[len(argv)-1]
	}
	stopCP, stopTarget := pick((*argvs)[stop])
	runCP, _ := pick((*argvs)[finalize])
	if stopCP == "" || stopCP != runCP {
		t.Errorf("stop ControlPath %q != playbook's %q", stopCP, runCP)
	}
	if !slices.Contains((*argvs)[finalize], stopTarget) {
		t.Errorf("stop target %q is not the playbook's target: %q", stopTarget, (*argvs)[finalize])
	}
}

// TestProxmoxRenewGuestLoginIsBestEffort: a failed stop is reported, and the
// create still succeeds, since the VM is provisioned either way.
func TestProxmoxRenewGuestLoginIsBestEffort(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	m := newPVEMock(t)
	registerBaseBuild(m, &createRecorder{})
	p := newCreateProvider(t, m)
	p.runSSH = func(_ context.Context, argv []string, _ io.Reader, stdout, stderr io.Writer) error {
		if isStopMaster(argv) {
			_, _ = io.WriteString(stderr, "Control socket connect: No such file or directory\n")
			return errors.New("exit status 255")
		}
		_, _ = io.WriteString(stdout, "ok\n")
		return nil
	}

	var out bytes.Buffer
	if err := p.Create(context.Background(), webConfig(), provision.CreateOptions{}, &out); err != nil {
		t.Fatalf("Create failed on a best-effort ssh reset: %v", err)
	}
	if !strings.Contains(out.String(), "could not reset the ssh connection") {
		t.Errorf("failure not reported; output:\n%s", out.String())
	}
}
