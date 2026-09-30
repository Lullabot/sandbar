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

// publishedResetProvisioner is a Provisioner over the published-base path whose
// image acquisition is under the test's control, with host state isolated.
func publishedResetProvisioner(t *testing.T, f *fakeRunner, acquireErr error) *Provisioner {
	t.Helper()
	t.Setenv("LIMA_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	p := &Provisioner{Lima: lima.New(f), PlaybookDir: t.TempDir(), UsePublishedBase: true}
	p.AcquireBaseImage = func(context.Context, lima.ImageStore, string, io.Writer) (string, error) {
		if acquireErr != nil {
			return "", acquireErr
		}
		return "/host/cache/verified.qcow2", nil
	}
	return p
}

func stateDirs(t *testing.T) []string {
	t.Helper()
	g, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "sand-reset-*"))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// A stale base whose replacement cannot be downloaded must not cost the user
// their VM. The base is prepared before anything is staged or deleted, so the
// failure leaves the VM, its data and the old base exactly as they were.
func TestReset_BaseDownloadFailureTouchesNothing(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{
		"sandbar-base": []byte("Stopped\n"),
		"claude":       []byte("Running\n"),
	}}
	p := publishedResetProvisioner(t, f, errors.New("dial tcp: no route to host"))
	stubBaseVersion(t, "x", nil, map[string]string{"sandbar-base": "base-image-OLD"})

	err := p.Reset(context.Background(), testConfig(), ResetOptions{PreserveHome: true}, io.Discard)
	if err == nil {
		t.Fatal("Reset should fail when the replacement base cannot be downloaded")
	}
	if !strings.Contains(err.Error(), `"claude" was not touched`) {
		t.Errorf("the error does not say the VM was left alone: %v", err)
	}
	for _, c := range f.calls {
		if c[0] == "delete" {
			t.Fatalf("nothing may be deleted when the base cannot be prepared: %v", f.calls)
		}
		if isTarOut(c) || c[0] == "start" || c[0] == "stop" {
			t.Fatalf("the VM must not be started or staged before the base is ready: %v", c)
		}
	}
	if dirs := stateDirs(t); len(dirs) != 0 {
		t.Errorf("a staging directory was created before the base was ready: %v", dirs)
	}
}

// The same rule one level down: replacing a stale base fetches the new image
// first, so a failed download leaves the old base in place rather than deleting
// it and leaving the host with none.
func TestPublishedBaseReplacementKeepsTheOldBaseWhenTheDownloadFails(t *testing.T) {
	f := &fakeRunner{status: map[string][]byte{"sandbar-base": []byte("Stopped\n")}}
	p := publishedResetProvisioner(t, f, errors.New("connection reset"))
	stubBaseVersion(t, "x", nil, map[string]string{"sandbar-base": "base-image-OLD"})

	err := p.ensureBaseStopped(context.Background(), testConfig(), CreateOptions{}, io.Discard, newPhaseTimer(io.Discard))
	if err == nil {
		t.Fatal("expected the download failure to surface")
	}
	if !strings.Contains(err.Error(), "left in place") {
		t.Errorf("the error does not say the old base survived: %v", err)
	}
	for _, c := range f.calls {
		if c[0] == "delete" {
			t.Fatalf("the old base was deleted before its replacement was in hand: %v", f.calls)
		}
	}
}
