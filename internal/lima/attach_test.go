package lima

import (
	"slices"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/guestsh"
)

// These tests are the executable specification of the LIMACTL WRAPPER around the
// guest attach command: the `limactl shell [--workdir H] <name>` prefix and the
// one-command RunArgv form. They assert on the ARGV — never on an exec — because
// no test in this repo may require a real limactl (AGENTS.md, hard rule).
// Real-VM behaviour is the limae2e tests' job.
//
// The guest expression itself — the tmux branch structure, destroy-unattached,
// the COLORTERM handshake — belongs to internal/guestsh and is specified there,
// once, for every backend that wraps it.

func TestAttachArgv(t *testing.T) {
	tests := []struct {
		name      string
		instance  string
		guestHome string
		colorterm string
		wantHead  []string // everything up to (not including) the guest expression
	}{
		{
			name:      "fresh attach passes the guest home as the working directory",
			instance:  "claude",
			guestHome: "/home/debian.guest",
			// --workdir comes BEFORE the instance name and there is NO `--` separator:
			// both were learned against a real VM and both are load-bearing.
			wantHead: []string{"limactl", "shell", "--workdir", "/home/debian.guest", "claude", "bash", "-c"},
		},
		{
			// A guest home that could not be determined must OMIT the flag, not pass it
			// empty: `--workdir ""` would make limactl cd to nowhere.
			name:      "unknown guest home omits the flag entirely",
			instance:  "claude",
			guestHome: "",
			wantHead:  []string{"limactl", "shell", "claude", "bash", "-c"},
		},
		{
			name:      "a guest home with a space survives as ONE argv element",
			instance:  "my vm",
			guestHome: "/home/some one.guest",
			wantHead:  []string{"limactl", "shell", "--workdir", "/home/some one.guest", "my vm", "bash", "-c"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			argv := AttachArgv(tc.instance, tc.guestHome, tc.colorterm)
			if len(argv) != len(tc.wantHead)+1 {
				t.Fatalf("AttachArgv(%q, %q) = %q\nwant %d elements (%q + the guest expression), got %d",
					tc.instance, tc.guestHome, argv, len(tc.wantHead)+1, tc.wantHead, len(argv))
			}
			if head := argv[:len(tc.wantHead)]; !slices.Equal(head, tc.wantHead) {
				t.Errorf("AttachArgv(%q, %q) head =\n\t%q\nwant\n\t%q", tc.instance, tc.guestHome, head, tc.wantHead)
			}
			// The tail is guestsh's argv verbatim: this wrapper adds a prefix and
			// changes nothing about what the guest runs. A copy that drifted here
			// is the most destructive edit possible in this codebase.
			guest := guestsh.AttachArgv(tc.colorterm)
			if tail := argv[len(argv)-len(guest):]; !slices.Equal(tail, guest) {
				t.Errorf("guest tail =\n\t%q\nwant guestsh.AttachArgv's argv verbatim:\n\t%q", tail, guest)
			}
			for i, a := range argv {
				if a == "" {
					t.Errorf("argv[%d] is empty; an empty element is a flag value that means nothing: %q", i, argv)
				}
			}
			// `--` gets forwarded to the guest's bash, which dies with
			// `/bin/bash: --: invalid option` (verified on a real VM).
			if i := slices.Index(argv, "--"); i >= 0 {
				t.Errorf("argv[%d] is a `--` separator; limactl forwards it to the guest bash, which fails with"+
					" `/bin/bash: --: invalid option`. The guest command follows the instance name directly: %q", i, argv)
			}
		})
	}
}

// TestAttachArgvWorkdirPrecedesInstanceName pins an ordering that looks cosmetic and
// is not: `limactl shell <name> --workdir <dir>` forwards --workdir to the GUEST's
// bash (`/bin/bash: --: invalid option`) instead of consuming it. Verified against a
// real VM.
func TestAttachArgvWorkdirPrecedesInstanceName(t *testing.T) {
	argv := AttachArgv("claude", "/home/debian.guest", "")

	flag := slices.Index(argv, "--workdir")
	name := slices.Index(argv, "claude")
	if flag < 0 {
		t.Fatalf("no --workdir in %q", argv)
	}
	if flag > name {
		t.Fatalf("--workdir (argv[%d]) comes AFTER the instance name (argv[%d]); limactl then forwards it to the"+
			" guest's bash, which dies with `/bin/bash: --: invalid option`. It must precede the name: %q", flag, name, argv)
	}
	if got := argv[flag+1]; got != "/home/debian.guest" {
		t.Errorf("--workdir value = %q, want the guest home; note it is /home/<user>.guest, never /home/<user>", got)
	}
}

// TestRunArgvKeepsWorkdirOutOfTheExpression is the injection guard for the
// interactive one-command path: the checkout directory comes from sweeping the
// GUEST, so it must travel as its own argv element and never appear inside the
// `bash -c` expression the guest parses.
func TestRunArgvKeepsWorkdirOutOfTheExpression(t *testing.T) {
	nasty := "/home/u/repo; rm -rf ~; echo $(whoami)"
	argv := RunArgv("web", nasty, "git status", "truecolor")

	var sawAsOwnElement bool
	for i, a := range argv {
		if a == nasty {
			sawAsOwnElement = true
			if i == 0 || argv[i-1] != "--workdir" {
				t.Fatalf("workdir must follow --workdir, got argv %q", argv)
			}
		}
	}
	if !sawAsOwnElement {
		t.Fatalf("workdir did not reach argv as its own element: %q", argv)
	}
	// The expression the guest parses must not contain it anywhere.
	expr := argv[len(argv)-1]
	if strings.Contains(expr, nasty) || strings.Contains(expr, "rm -rf") {
		t.Fatalf("workdir leaked into the guest expression: %q", expr)
	}
}

// TestRunArgvDropsAnUnsafeColorterm mirrors AttachArgv's rule: COLORTERM is
// baked into a shell expression, so an unrecognised value is dropped rather
// than escaped.
func TestRunArgvDropsAnUnsafeColorterm(t *testing.T) {
	argv := RunArgv("web", "/home/u/repo", "git status", "truecolor; rm -rf ~")
	expr := argv[len(argv)-1]
	if strings.Contains(expr, "rm -rf") {
		t.Fatalf("an unsafe COLORTERM reached the guest expression: %q", expr)
	}
	if expr != "git status" {
		t.Fatalf("expression = %q, want the caller's literal command alone", expr)
	}
}
