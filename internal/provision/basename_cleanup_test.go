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

// These tests drive the cleanup of a failed create with instance names that
// are not plain directory names. Every one of them goes through fakeHostFiles,
// whose RemoveAll only RECORDS the path it was handed: the bug they pin turned
// `--base-name ..` into a RemoveAll of the user's home directory, so a test that
// let the real filesystem answer would, on a regression, delete real files.

// unsafeInstanceNames are names that filepath.Join(limaHome, name) does not
// resolve to a direct child of the Lima home. limactl refuses every one of them,
// which is exactly what sends a failed create down cleanupInstance's RemoveAll
// fallback.
var unsafeInstanceNames = []string{
	"..",          // the Lima home's parent: the user's home directory
	".",           // the Lima home itself, holding every VM
	"../../x",     // a sibling of the user's home
	"/home/one",   // absolute: Join nests it, but it is still not a child
	"a/b",         // a grandchild
	"web/",        // cleans to "web", so the path no longer names what was asked
	"web/../../x", // escapes after a legitimate-looking first component
}

const cleanupTestHome = "/home/one/.lima"

// removedPaths returns every path the fake was asked to RemoveAll.
func removedPaths(log []string) []string {
	var paths []string
	for _, entry := range log {
		if _, path, ok := strings.Cut(entry, ":RemoveAll:"); ok {
			paths = append(paths, path)
		}
	}
	return paths
}

// insideLimaHome reports whether path is strictly below the Lima home: the home
// itself and anything above or beside it are not.
func insideLimaHome(path string) bool {
	return strings.HasPrefix(filepath.Clean(path), cleanupTestHome+string(filepath.Separator))
}

func TestInstanceDirOnlyNamesADirectChildOfTheLimaHome(t *testing.T) {
	var log []string
	hf := &fakeHostFiles{id: "host", home: cleanupTestHome, log: &log}

	for _, name := range unsafeInstanceNames {
		if got := instanceDir(hf, name); got != "" {
			t.Errorf("instanceDir(%q) = %q; want \"\" for a name that is not a direct child of %s", name, got, cleanupTestHome)
		}
	}
	// The negative control: without it, an instanceDir that refused every name
	// would pass the loop above.
	for _, name := range []string{"web", "sandbar-base", "sandbar-tmpl-golden", "a.b_c-d"} {
		if got, want := instanceDir(hf, name), filepath.Join(cleanupTestHome, name); got != want {
			t.Errorf("instanceDir(%q) = %q, want %q", name, got, want)
		}
	}
	// A Lima home spelled with a trailing slash is still the same directory.
	slash := &fakeHostFiles{id: "host", home: cleanupTestHome + "/", log: &log}
	if got, want := instanceDir(slash, "web"), filepath.Join(cleanupTestHome, "web"); got != want {
		t.Errorf("instanceDir with a trailing-slash home = %q, want %q", got, want)
	}
}

func TestCleanupInstanceNeverRemovesOutsideTheLimaHome(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	for _, name := range unsafeInstanceNames {
		var log []string
		hf := &fakeHostFiles{id: "host", home: cleanupTestHome, log: &log}
		var out bytes.Buffer
		failingCleanupProvisioner(hf).cleanupInstance(name, &out)

		if removed := removedPaths(log); len(removed) > 0 {
			t.Errorf("cleanupInstance(%q) removed %v; it must remove nothing for a name outside the Lima home", name, removed)
		}
		if !strings.Contains(out.String(), "Not cleaning up") {
			t.Errorf("cleanupInstance(%q) said nothing about leaving the path alone; output:\n%s", name, out.String())
		}
	}

	// The negative control: an ordinary name is still cleaned up, at its own
	// directory and nowhere else.
	var log []string
	hf := &fakeHostFiles{id: "host", home: cleanupTestHome, log: &log}
	failingCleanupProvisioner(hf).cleanupInstance("web", io.Discard)
	if got, want := removedPaths(log), []string{filepath.Join(cleanupTestHome, "web")}; !slices.Equal(got, want) {
		t.Errorf("cleanupInstance(web) removed %v, want %v", got, want)
	}
}

// recordingImageStore adds the ImageStore methods to fakeHostFiles so the
// published-base path can run against it. None of them touch the filesystem.
type recordingImageStore struct{ *fakeHostFiles }

func (s recordingImageStore) WriteStream(_ context.Context, path string, _ io.Reader) error {
	s.note("WriteStream", path)
	return nil
}

func (s recordingImageStore) SHA256(_ context.Context, path string) (string, error) {
	s.note("SHA256", path)
	return "", nil
}

func (s recordingImageStore) Rename(_ context.Context, oldPath, newPath string) error {
	s.note("Rename", oldPath+" -> "+newPath)
	return nil
}

// TestFailedBaseBuildWithTraversalNameRemovesNothingOutsideTheLimaHome walks the
// whole chain a `--base-name ..` create took: the base reads as absent, the image
// is acquired, `limactl start --name ..` is refused (as the real limactl refuses
// it: "instance name `..` is not a valid identifier"), and the failed build's
// cleanup runs. It must not hand RemoveAll anything outside the Lima home.
func TestFailedBaseBuildWithTraversalNameRemovesNothingOutsideTheLimaHome(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	for _, base := range []string{"..", "."} {
		var log []string
		hf := recordingImageStore{&fakeHostFiles{id: "host", home: cleanupTestHome, log: &log}}
		f := &fakeRunner{
			status:  map[string][]byte{},
			failOn:  func(args []string) bool { return args[0] == "start" || args[0] == "delete" },
			failErr: errors.New("instance name is not a valid identifier"),
		}
		p := &Provisioner{
			Lima:             lima.New(f),
			HostFiles:        hf,
			PlaybookDir:      t.TempDir(),
			UsePublishedBase: true,
			AcquireBaseImage: func(context.Context, lima.ImageStore, string, io.Writer) (string, error) {
				return "/host/cache/verified.qcow2", nil
			},
		}
		cfg := testConfig()
		cfg.BaseName = base

		var out bytes.Buffer
		err := p.ensureBaseStopped(context.Background(), cfg, CreateOptions{}, &out, newPhaseTimer(&out))
		if err == nil {
			t.Fatalf("base %q: the build succeeded, so the cleanup path under test never ran", base)
		}
		started := false
		for _, args := range f.snapshot() {
			if args[0] == "start" {
				started = true
			}
		}
		if !started {
			t.Fatalf("base %q: limactl start was never attempted; calls: %v", base, f.snapshot())
		}
		for _, path := range removedPaths(log) {
			if !insideLimaHome(path) {
				t.Errorf("base %q: a failed build removed %s, which is not inside the Lima home %s", base, path, cleanupTestHome)
			}
		}
	}
}
