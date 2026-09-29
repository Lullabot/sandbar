package provision

import (
	"bytes"
	"context"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/baseimage"
	"github.com/lullabot/sandbar/internal/lima"
)

func TestPublishedBaseBuildAndReuse(t *testing.T) {
	t.Setenv("LIMA_HOME", t.TempDir())
	playbook := t.TempDir()
	f := &fakeRunner{status: map[string][]byte{}}
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: playbook, UsePublishedBase: true}
	var captured []byte
	f.hook = func(args []string) {
		if len(args) >= 5 && args[0] == "start" && args[1] == "--name" {
			var err error
			captured, err = os.ReadFile(args[4])
			if err != nil {
				t.Errorf("read overlay passed to Lima: %v", err)
			}
		}
	}
	acquires := 0
	p.AcquireBaseImage = func(_ context.Context, _ lima.ImageStore, arch string, _ io.Writer) (string, error) {
		acquires++
		if arch != "amd64" {
			t.Errorf("architecture = %q, want amd64", arch)
		}
		return "/host/cache/verified.qcow2", nil
	}

	cfg := testConfig()
	var log bytes.Buffer
	if err := p.ensureBaseStopped(context.Background(), cfg, CreateOptions{}, &log, newPhaseTimer(&log)); err != nil {
		t.Fatal(err)
	}
	if acquires != 1 {
		t.Fatalf("acquisitions = %d, want 1", acquires)
	}
	if !bytes.Contains(captured, []byte(`location: "/host/cache/verified.qcow2"`)) ||
		!bytes.Contains(captured, []byte(`digest: "sha256:`)) ||
		!bytes.Contains(captured, []byte("user:\n  name: \"andrew\"")) ||
		bytes.Contains(captured, []byte("template:_images/debian-13")) {
		t.Errorf("wrong image in Lima overlay:\n%s", captured)
	}
	if strings.Contains(log.String(), "TASK [base :") {
		t.Errorf("base Ansible ran:\n%s", log.String())
	}
	generalizeAt, stopAt := -1, -1
	for i, args := range f.snapshot() {
		if slices.Contains(args, GeneralizeScript) {
			if generalizeAt >= 0 {
				t.Fatal("published base generalization ran more than once")
			}
			generalizeAt = i
		}
		if len(args) >= 2 && args[0] == "stop" && args[1] == cfg.BaseName {
			stopAt = i
		}
	}
	if generalizeAt < 0 || stopAt < 0 || generalizeAt >= stopAt {
		t.Fatalf("published base identity must be reset before it is stopped; calls: %v", f.snapshot())
	}
	if got := readBaseVersion(lima.LocalFiles(), cfg.BaseName); got != baseimage.PinnedManifest.Version {
		t.Errorf("stamp = %q, want image version %q", got, baseimage.PinnedManifest.Version)
	}

	f.status[cfg.BaseName] = []byte("Stopped\n")
	before := len(f.snapshot())
	if err := p.ensureBaseStopped(context.Background(), cfg, CreateOptions{}, &log, newPhaseTimer(&log)); err != nil {
		t.Fatal(err)
	}
	if acquires != 1 || len(f.snapshot()) != before+1 {
		t.Errorf("second create did more than inspect base: acquisitions=%d calls=%v", acquires, f.snapshot()[before:])
	}
}

type remoteArchRunner struct{ *fakeRunner }

func (remoteArchRunner) HostOutput(_ context.Context, argv ...string) ([]byte, error) {
	if len(argv) != 2 || argv[0] != "uname" || argv[1] != "-m" {
		return nil, os.ErrInvalid
	}
	return []byte("aarch64\n"), nil
}

func TestPublishedBaseUsesRemoteLimaArchitecture(t *testing.T) {
	p := &Provisioner{
		Lima:      lima.New(remoteArchRunner{fakeRunner: &fakeRunner{}}),
		HostFiles: lima.NewSSHHost(lima.SSHConfig{Host: "remote.example"}),
	}
	got, err := p.baseImageArch(context.Background())
	if err != nil || got != "arm64" {
		t.Fatalf("remote image architecture = %q, %v; want arm64", got, err)
	}
}
