package provision

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
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
	// failSubstr fails just the calls whose joined argv contains it, which is how
	// a guest that does not have a command answers a `command -v` probe. A blanket
	// err cannot express that: it would fail the tar as well.
	failSubstr string
}

// errNoSuchCommand is what a `command -v <missing>` probe comes back as.
var errNoSuchCommand = errors.New("exit status 1")

func (f *stagingFakeRunner) fails(joined string) error {
	if f.failSubstr != "" && strings.Contains(joined, f.failSubstr) {
		return errNoSuchCommand
	}
	return f.err
}

func (f *stagingFakeRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	joined := strings.Join(args, " ")
	for key, val := range f.streamOut {
		if strings.Contains(joined, key) {
			return val, f.fails(joined)
		}
	}
	return nil, f.fails(joined)
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
	return f.fails(joined)
}

func (f *stagingFakeRunner) StreamOut(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error {
	// StageOut streams the tar archive through StreamOut; mirror Stream so the
	// recorded argv and canned-stdout routing are identical.
	return f.Stream(ctx, stdin, out, args...)
}

func TestOrgRelDir(t *testing.T) {
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
			gotDir, gotOK := OrgRelDir(tc.url)
			if gotDir != tc.wantDir || gotOK != tc.wantOK {
				t.Fatalf("OrgRelDir(%q) = (%q, %v), want (%q, %v)", tc.url, gotDir, gotOK, tc.wantDir, tc.wantOK)
			}
		})
	}
}

// CheckoutRelDir extends OrgRelDir with the repo directory name, giving the
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

// TestStageOut pins the stage-out argv on a guest that HAS zstd: the probe, then
// a tar handed zstd as its compressor and told whose files these are. gzip on a
// preserved home is minutes where zstd is seconds, so which compressor gets
// chosen is worth asserting rather than assuming — and --owner/--group are what
// let the restore skip an ownership pass over every inode it writes, so their
// absence would be invisible until a restored tree came back owned by root.
func TestStageOut(t *testing.T) {
	f := &stagingFakeRunner{}
	cli := lima.New(f)
	archive := filepath.Join(t.TempDir(), "claude.tar")
	paths := []string{".claude", ".claude.json"}

	if err := StageOut(context.Background(), cli, "claude", "/home/andrew", "andrew", paths, archive, "Claude data", io.Discard); err != nil {
		t.Fatalf("StageOut: %v", err)
	}

	want := [][]string{
		{"shell", "claude", "sudo", "sh", "-c", "command -v zstd"},
		{"shell", "claude", "sudo", "tar", "-C", "/home/andrew", "--ignore-failed-read", "--owner=andrew", "--group=andrew", "-I", "zstd -T0 -3", "-cf", "-", ".claude", ".claude.json"},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("StageOut argv = %v, want %v", f.calls, want)
	}
}

// TestStageOutFallsBackToGzipWithoutZstd: the archive is written by the SOURCE
// VM, which may have been cloned from a base built before zstd was part of the
// image. A reset that refused to run there would refuse while holding the only
// copy of the user's work, so a missing compressor costs speed, not the reset.
//
// The fallback is also SAID, and that half is asserted here rather than left to
// inspection. Falling back silently is how a user ends up watching a copy run
// several times slower than the same copy on the VM beside it with no way to
// find out why — the cause is a base image built before zstd joined the package
// list, which no amount of resetting THIS VM can change.
func TestStageOutFallsBackToGzipWithoutZstd(t *testing.T) {
	f := &stagingFakeRunner{failSubstr: "command -v zstd"}
	cli := lima.New(f)
	archive := filepath.Join(t.TempDir(), "claude.tar")

	var out strings.Builder
	if err := StageOut(context.Background(), cli, "claude", "/home/andrew", "andrew", []string{".claude"}, archive, "Claude data", &out); err != nil {
		t.Fatalf("StageOut: %v", err)
	}

	want := []string{"shell", "claude", "sudo", "tar", "-C", "/home/andrew", "--ignore-failed-read", "--owner=andrew", "--group=andrew", "-z", "-cf", "-", ".claude"}
	if got := f.calls[len(f.calls)-1]; !reflect.DeepEqual(got, want) {
		t.Fatalf("StageOut argv = %v, want %v", got, want)
	}
	for _, phrase := range []string{"no zstd", "gzip", "--rebuild"} {
		if !strings.Contains(out.String(), phrase) {
			t.Fatalf("gzip fallback was not explained (%q missing); got:\n%s", phrase, out.String())
		}
	}
}

func TestStageIn(t *testing.T) {
	f := &stagingFakeRunner{}
	cli := lima.New(f)
	archive := filepath.Join(t.TempDir(), "claude.tar")
	// StageIn opens the archive for reading, so it must exist — and the extract
	// flag is chosen from its leading bytes, so they have to be a real format's.
	if err := os.WriteFile(archive, append(gzipMagic, "dummy"...), 0o600); err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	paths := []string{".claude", ".claude.json"}

	if err := StageIn(context.Background(), cli, "claude", "/home/andrew", "andrew", paths, archive, "Claude data", io.Discard); err != nil {
		t.Fatalf("StageIn: %v", err)
	}

	want := [][]string{
		// The extract is the whole restore. Nothing follows it: the archive's
		// members already name the user as their owner, so there is no ownership
		// pass to run and no per-path probe to decide one (see StageIn).
		{"shell", "claude", "sudo", "tar", "-C", "/home/andrew", "-z", "-xf", "-"},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("StageIn argv = %v, want %v", f.calls, want)
	}
}
