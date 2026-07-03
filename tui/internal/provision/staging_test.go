package provision

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/tui/internal/lima"
)

// stagingFakeRunner records argv (and streamed stdin) and, unlike the package's
// fakeRunner, can write canned stdout to a Stream call's out writer so guestHome
// can parse a getent line. Canned output is keyed by a substring of the joined
// argv (e.g. "getent").
type stagingFakeRunner struct {
	calls     [][]string
	streams   []string
	streamOut map[string][]byte
	err       error
}

func (f *stagingFakeRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	return nil, f.err
}

func (f *stagingFakeRunner) Stream(_ context.Context, stdin io.Reader, out io.Writer, args ...string) error {
	f.calls = append(f.calls, args)
	if stdin != nil {
		data, _ := io.ReadAll(stdin)
		f.streams = append(f.streams, string(data))
	}
	joined := strings.Join(args, " ")
	if out != nil {
		for key, val := range f.streamOut {
			if strings.Contains(joined, key) {
				_, _ = out.Write(val)
			}
		}
	}
	return f.err
}

func TestCloneOrgRelDir(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantDir string
		wantOK  bool
	}{
		{"https github org repo", "https://github.com/lullabot/sandbar", "github.com/lullabot", true},
		{"trailing .git", "https://github.com/org/repo.git", "github.com/org", true},
		{"trailing slash", "https://github.com/org/repo/", "github.com/org", true},
		{"trailing .git and slash", "https://github.com/org/repo.git/", "github.com/org", true},
		{"nested group", "https://gitlab.com/group/sub/repo", "gitlab.com/group/sub", true},
		{"no org segment", "https://github.com/justrepo", "", false},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotDir, gotOK := cloneOrgRelDir(tc.url)
			if gotDir != tc.wantDir || gotOK != tc.wantOK {
				t.Fatalf("cloneOrgRelDir(%q) = (%q, %v), want (%q, %v)", tc.url, gotDir, gotOK, tc.wantDir, tc.wantOK)
			}
		})
	}
}

// CheckoutRelDir extends cloneOrgRelDir with the repo directory name, giving the
// full guest-home-relative checkout path the TUI opens the guest browser at.
func TestCheckoutRelDir(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		wantDir string
		wantOK  bool
	}{
		{"https github org repo", "https://github.com/lullabot/sandbar", "github.com/lullabot/sandbar", true},
		{"trailing .git", "https://github.com/org/repo.git", "github.com/org/repo", true},
		{"trailing slash", "https://github.com/org/repo/", "github.com/org/repo", true},
		{"nested group", "https://gitlab.com/group/sub/repo", "gitlab.com/group/sub/repo", true},
		{"no org segment", "https://github.com/justrepo", "", false},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotDir, gotOK := CheckoutRelDir(tc.url)
			if gotDir != tc.wantDir || gotOK != tc.wantOK {
				t.Fatalf("CheckoutRelDir(%q) = (%q, %v), want (%q, %v)", tc.url, gotDir, gotOK, tc.wantDir, tc.wantOK)
			}
		})
	}
}

func TestGuestHome(t *testing.T) {
	f := &stagingFakeRunner{streamOut: map[string][]byte{
		"getent": []byte("andrew:x:1000:1000:Andrew Berry:/home/andrew:/bin/bash\n"),
	}}
	cli := lima.New(f)

	home, err := guestHome(context.Background(), cli, "claude", "andrew")
	if err != nil {
		t.Fatalf("guestHome: %v", err)
	}
	if home != "/home/andrew" {
		t.Fatalf("home = %q, want /home/andrew", home)
	}

	want := [][]string{{"shell", "claude", "getent", "passwd", "andrew"}}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("guestHome argv = %v, want %v", f.calls, want)
	}
}

func TestStageOut(t *testing.T) {
	f := &stagingFakeRunner{}
	cli := lima.New(f)
	archive := filepath.Join(t.TempDir(), "claude.tar.gz")
	paths := []string{".claude", ".claude.json"}

	if err := StageOut(context.Background(), cli, "claude", "/home/andrew", paths, archive); err != nil {
		t.Fatalf("StageOut: %v", err)
	}

	want := [][]string{
		{"shell", "claude", "sudo", "tar", "-C", "/home/andrew", "--ignore-failed-read", "-czf", "-", ".claude", ".claude.json"},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("StageOut argv = %v, want %v", f.calls, want)
	}
}

func TestStageIn(t *testing.T) {
	f := &stagingFakeRunner{}
	cli := lima.New(f)
	archive := filepath.Join(t.TempDir(), "claude.tar.gz")
	// StageIn opens the archive for reading, so it must exist.
	if err := os.WriteFile(archive, []byte("dummy"), 0o600); err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	paths := []string{".claude", ".claude.json"}

	if err := StageIn(context.Background(), cli, "claude", "/home/andrew", "andrew", paths, archive); err != nil {
		t.Fatalf("StageIn: %v", err)
	}

	want := [][]string{
		// Extract MUST precede chown.
		{"shell", "claude", "sudo", "tar", "-C", "/home/andrew", "-xzf", "-"},
		{"shell", "claude", "sudo", "chown", "-R", "andrew:andrew", "/home/andrew/.claude", "/home/andrew/.claude.json"},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("StageIn argv = %v, want %v", f.calls, want)
	}
}
