package provision

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
)

// fakeHostFiles is a minimal lima.HostFiles double whose every method call is
// tagged with its own id and appended to a log SHARED across every fake in a
// test (a pointer to one slice), so a test can prove which handle a given
// Provisioner operation actually reached.
type fakeHostFiles struct {
	id   string
	home string
	log  *[]string
}

// note records "<id>:<method>:<path>" so a test can tell not just THAT a
// method fired, but on WHICH fake's data (path/home) it operated.
func (f *fakeHostFiles) note(method, detail string) {
	*f.log = append(*f.log, f.id+":"+method+":"+detail)
}

func (f *fakeHostFiles) ReadFile(path string) ([]byte, error) {
	f.note("ReadFile", path)
	return nil, fs.ErrNotExist
}

func (f *fakeHostFiles) Stat(path string) (fs.FileInfo, error) {
	f.note("Stat", path)
	return nil, nil // present: nil error, as cleanupInstance only checks err
}

func (f *fakeHostFiles) WriteFile(path string, _ []byte, _, _ fs.FileMode) error {
	f.note("WriteFile", path)
	return nil
}

func (f *fakeHostFiles) MkdirAll(path string, _ fs.FileMode) error {
	f.note("MkdirAll", path)
	return nil
}

func (f *fakeHostFiles) RemoveAll(path string) error {
	f.note("RemoveAll", path)
	return nil
}

func (f *fakeHostFiles) DiskAllocBytes(path string) int64 {
	f.note("DiskAllocBytes", path)
	return -1
}

func (f *fakeHostFiles) LimaHome() string {
	f.note("LimaHome", f.home)
	return f.home
}

func (f *fakeHostFiles) StagePlaybook(_ context.Context, localDir string) (string, error) {
	f.note("StagePlaybook", localDir)
	return localDir, nil
}

func (f *fakeHostFiles) OpenLock(path string, _ fs.FileMode) (lima.LockFile, error) {
	f.note("OpenLock", path)
	return nil, errors.New("fakeHostFiles does not support locking")
}

func (f *fakeHostFiles) ReadInstanceMarkers(_ context.Context, limaHome, filename string) (map[string][]byte, error) {
	f.note("ReadInstanceMarkers", limaHome+"/"+filename)
	return map[string][]byte{}, nil
}

var _ lima.HostFiles = (*fakeHostFiles)(nil)

// TestProvisioner_TwoHostFilesNoCrossTalk is this task's acceptance test: it
// proves the process-global provision.hostFiles is gone by constructing TWO
// Provisioner instances in the SAME process, each wired to its OWN fake
// lima.HostFiles, and driving a real provisioning operation (cleanupInstance,
// which reaches HostFiles through instanceDir/Stat/RemoveAll exactly like the
// create/reset paths do) through each. If either Provisioner read a shared
// global instead of p.HostFiles, one of two things would happen: either both
// operations would show up against the SAME fake (the global, whichever one
// last set it), or — since neither fake IS the global — neither log would
// show the expected entries at all. Both are failures this test would catch.
func TestProvisioner_TwoHostFilesNoCrossTalk(t *testing.T) {
	var log []string
	hf1 := &fakeHostFiles{id: "host1", home: "/home/one/.lima", log: &log}
	hf2 := &fakeHostFiles{id: "host2", home: "/home/two/.lima", log: &log}

	// Delete errors on both fake Lima cores, so cleanupInstance falls straight
	// through to hf.RemoveAll(dir) without a second Stat call — deterministic
	// with a single fakeRunner call each.
	p1 := &Provisioner{Lima: lima.New(&fakeRunner{err: errors.New("delete unsupported by this fake")}), HostFiles: hf1}
	p2 := &Provisioner{Lima: lima.New(&fakeRunner{err: errors.New("delete unsupported by this fake")}), HostFiles: hf2}

	p1.cleanupInstance("vm-one", io.Discard)
	p2.cleanupInstance("vm-two", io.Discard)

	if len(log) == 0 {
		t.Fatal("neither Provisioner's cleanupInstance touched its HostFiles at all")
	}

	var host1Saw, host2Saw bool
	for _, entry := range log {
		switch {
		case strings.HasPrefix(entry, "host1:"):
			host1Saw = true
			if strings.Contains(entry, "two") {
				t.Errorf("p1 (hf1, home %q) touched host2's data: %q", hf1.home, entry)
			}
		case strings.HasPrefix(entry, "host2:"):
			host2Saw = true
			if strings.Contains(entry, "one") {
				t.Errorf("p2 (hf2, home %q) touched host1's data: %q", hf2.home, entry)
			}
		default:
			t.Errorf("log entry tagged with neither fake's id: %q", entry)
		}
	}
	if !host1Saw {
		t.Error("p1.cleanupInstance never reached hf1 — HostFiles field is not being read")
	}
	if !host2Saw {
		t.Error("p2.cleanupInstance never reached hf2 — HostFiles field is not being read")
	}
}

// TestProvisioner_NilHostFilesDefaultsToLocal pins the backward-compatible
// default this task's design depends on: every existing caller that
// constructs a Provisioner without setting HostFiles (every test in this
// package, every local-Lima construction before this task) must keep behaving
// exactly as before — reading/writing the real local filesystem — rather than
// panicking on a nil interface.
func TestProvisioner_NilHostFilesDefaultsToLocal(t *testing.T) {
	p := &Provisioner{}
	hf := p.hostFiles()
	if hf == nil {
		t.Fatal("hostFiles() returned nil for a Provisioner with no HostFiles set")
	}
	if _, ok := hf.(interface{ LimaHome() string }); !ok {
		t.Fatal("hostFiles() default does not satisfy lima.HostFiles")
	}
}
