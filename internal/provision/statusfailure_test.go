package provision

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
)

// errListDropped is what a `limactl list <name>` that could not answer looks
// like on remote Lima: ssh's own exit status, the connection gone. It is not
// limactl's "No instance matching <name> found.", so it says nothing about
// whether the instance exists.
var errListDropped = errors.New("exit status 255: Connection to mac-mini closed by remote host.")

// seedInstanceDir puts an instance directory under the isolated LIMA_HOME, so
// cleanupInstance (which stats it before deleting) treats the name as present,
// exactly as it would for a real, healthy instance.
func seedInstanceDir(t *testing.T, name string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LIMA_HOME", home)
	dir := filepath.Join(home, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lima.yaml"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func deleted(calls [][]string, name string) bool {
	for _, c := range calls {
		if len(c) >= 2 && c[0] == "delete" && c[1] == name {
			return true
		}
	}
	return false
}

// A base-status query that FAILS is not a base that is absent. Treating it as
// absent sent the provisioner off to build a new base over the healthy one:
// `limactl start --name sandbar-base` refuses an existing instance, and the
// failed-build cleanup then force-deleted the healthy shared base.
func TestFailedBaseStatusNeverDeletesTheBase(t *testing.T) {
	for _, published := range []bool{true, false} {
		name := "legacy"
		if published {
			name = "published"
		}
		t.Run(name, func(t *testing.T) {
			dir := seedInstanceDir(t, "sandbar-base")
			f := &fakeRunner{
				status: map[string][]byte{"sandbar-base": []byte("Stopped\n")},
				failOn: func(args []string) bool {
					switch {
					case len(args) >= 2 && args[0] == "list" && args[1] == "sandbar-base":
						return true // the transient failure
					case len(args) >= 3 && args[0] == "start" && args[1] == "--name" && args[2] == "sandbar-base":
						return true // limactl: "instance `sandbar-base` already exists"
					}
					return false
				},
				failErr: errListDropped,
			}
			p := &Provisioner{Lima: lima.New(f), PlaybookDir: t.TempDir(), UsePublishedBase: published}
			p.AcquireBaseImage = func(context.Context, lima.ImageStore, string, io.Writer) (string, error) {
				return "/host/cache/verified.qcow2", nil
			}

			err := p.ensureBaseStopped(context.Background(), testConfig(), CreateOptions{}, io.Discard, newPhaseTimer(io.Discard))
			if err == nil {
				t.Error("a base whose status could not be read was treated as ready")
			}
			if deleted(f.snapshot(), "sandbar-base") {
				t.Errorf("the base was force-deleted after its status query failed: %v", f.snapshot())
			}
			if _, statErr := os.Stat(dir); statErr != nil {
				t.Errorf("the healthy base's directory is gone: %v", statErr)
			}
			for _, c := range f.snapshot() {
				if len(c) >= 2 && c[0] == "start" && c[1] == "--name" {
					t.Errorf("a base build was attempted over an existing base: %v", c)
				}
			}
		})
	}
}

// The create guard has the same shape: a target whose status could not be read
// is not a free name. Going on would clone onto it, and when limactl refuses the
// clone the failed-clone cleanup force-deletes the existing, possibly running,
// VM of that name.
func TestCreateRefusesWhenTheTargetStatusQueryFails(t *testing.T) {
	cfg := testConfig()
	dir := seedInstanceDir(t, cfg.Name)
	f := &fakeRunner{
		status: map[string][]byte{"sandbar-base": []byte("Stopped\n"), cfg.Name: []byte("Running\n")},
		failOn: func(args []string) bool {
			switch {
			case len(args) >= 2 && args[0] == "list" && args[1] == cfg.Name:
				return true // the transient failure
			case len(args) >= 1 && args[0] == "clone":
				return true // limactl: "instance `claude` already exists"
			}
			return false
		},
		failErr: errListDropped,
	}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: "/playbook"}

	if err := p.CreateVM(context.Background(), cfg, io.Discard); err == nil {
		t.Fatal("CreateVM went ahead although it could not tell whether the target exists")
	}
	if deleted(f.snapshot(), cfg.Name) {
		t.Errorf("CreateVM force-deleted the existing %q: %v", cfg.Name, f.snapshot())
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the existing VM's directory is gone: %v", err)
	}
	for _, c := range f.snapshot() {
		if c[0] == "clone" {
			t.Errorf("CreateVM cloned onto a name it could not check: %v", c)
		}
	}
}
