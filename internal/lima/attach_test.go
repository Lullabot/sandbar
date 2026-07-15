package lima

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// These tests are the executable specification of the guest attach command. They
// assert on the ARGV — never on an exec — because no test in this repo may require
// a real limactl (AGENTS.md, hard rule). Real-VM behaviour is the limae2e tests' job.

// splitGuestExpr returns the guest-side shell expression AttachArgv emitted (the
// final argv element, the string handed to the guest's `bash -c`) split into the
// branch taken when session `main` ALREADY EXISTS (grouped) and the branch taken
// when it does not (main). Every assertion about the two branches depends on being
// able to tell them apart, so a shape change fails loudly here rather than silently
// weakening the tests below.
func splitGuestExpr(t *testing.T, argv []string) (expr, grouped, mainBranch string) {
	t.Helper()
	if len(argv) < 3 || argv[len(argv)-3] != "bash" || argv[len(argv)-2] != "-c" {
		t.Fatalf("argv must end in `bash -c <expr>`, got %q", argv)
	}
	expr = argv[len(argv)-1]

	_, afterThen, ok := strings.Cut(expr, "; then ")
	if !ok {
		t.Fatalf("guest expression has no `; then ` branch, so its two branches cannot be told apart:\n%s", expr)
	}
	grouped, mainBranch, ok = strings.Cut(afterThen, "; else ")
	if !ok {
		t.Fatalf("guest expression has no `; else ` branch, so its two branches cannot be told apart:\n%s", expr)
	}
	mainBranch = strings.TrimSuffix(mainBranch, "; fi")
	return expr, grouped, mainBranch
}

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
			// both were learned against a real VM (task 01) and both are load-bearing.
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
			if expr := argv[len(argv)-1]; !strings.Contains(expr, "tmux") {
				t.Errorf("last argv element should be the guest tmux expression, got %q", expr)
			}
			for i, a := range argv {
				if a == "" {
					t.Errorf("argv[%d] is empty; an empty element is a flag value that means nothing: %q", i, argv)
				}
			}
			// `--` gets forwarded to the guest's bash, which dies with
			// `/bin/bash: --: invalid option` (task 01, verified on a real VM).
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
// real VM in task 01.
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

// TestAttachArgvDestroyUnattachedOnGroupedSessionOnly is THE test of this package.
//
// tmux's destroy-unattached kills a session the moment its last client leaves. On a
// GROUPED session that is exactly right: it is a per-client view that must not
// accumulate as an orphan. On `main` it is a catastrophe — `main` holds the user's
// long-running work, and surviving detach IS the feature. Reversing these two lines
// converts "your work survives a closed laptop" into "your work dies when you look
// away", with no error message, no crash, and nothing in any log. Hence both
// directions are asserted: present on the grouped session, ABSENT from main.
func TestAttachArgvDestroyUnattachedOnGroupedSessionOnly(t *testing.T) {
	argv := AttachArgv("claude", "/home/debian.guest", "")
	expr, grouped, mainBranch := splitGuestExpr(t, argv)

	const opt = "destroy-unattached"

	// Direction 1: the grouped session gets it, or orphan sessions pile up forever.
	if !strings.Contains(grouped, opt) {
		t.Errorf("the GROUPED branch does not set %s:\n\t%s\nEvery second attach would then leave an orphan"+
			" session behind when its terminal closes.", opt, grouped)
	}

	// Direction 2: `main` must NEVER get it. This is the one that destroys user work.
	if strings.Contains(mainBranch, opt) {
		t.Fatalf("the `main` branch sets %s:\n\t%s\n\nTHIS DESTROYS THE USER'S WORK. `main` is the session that"+
			" holds their long-running jobs; %s kills it the moment they detach or their terminal closes. Surviving"+
			" detach is the entire point of this feature. %s belongs on the GROUPED session only.", opt, mainBranch, opt, opt)
	}
	if n := strings.Count(expr, opt); n != 1 {
		t.Fatalf("%s appears %d times in the guest expression; it must appear exactly once, on the grouped"+
			" session:\n\t%s", opt, n, expr)
	}

	// And it must be TARGETED at the grouped session by name. `set-option` with no -t
	// applies to the "current" session, which is whatever tmux last touched — too
	// subtle a thing to bet the user's work on, so the target is explicit and this
	// test reads it back.
	re := regexp.MustCompile(`set-option -t (\S+) ` + opt)
	m := re.FindStringSubmatch(grouped)
	if m == nil {
		t.Fatalf("%s is not set by an explicitly targeted `set-option -t <session>` in the grouped branch:\n\t%s\n"+
			"An untargeted set-option applies to tmux's CURRENT session, which is not a guarantee worth betting the"+
			" user's work on.", opt, grouped)
	}
	if target := strings.Trim(m[1], `"'`); target == "main" || target == "=main" {
		t.Fatalf("`set-option -t %s %s` TARGETS MAIN. This destroys the user's work on detach — see above. The"+
			" target must be the grouped session.", m[1], opt)
	}
}

// TestAttachArgvGroupedSessionNamedInTheGuest: the grouped session's name must be
// chosen by the guest at attach time, not computed on the host, or two concurrent
// `sand shell` invocations can pick the same name and the second one fails.
func TestAttachArgvGroupedSessionNamedInTheGuest(t *testing.T) {
	argv := AttachArgv("claude", "/home/debian.guest", "")
	_, grouped, _ := splitGuestExpr(t, argv)

	if !strings.Contains(grouped, "$$") {
		t.Errorf("the grouped session's name is not derived from the guest shell's PID ($$):\n\t%s\nA"+
			" host-computed name can collide between two concurrent attaches.", grouped)
	}

	// Purity, and the proof there is no host-side counter, clock, or RNG in the name:
	// the same inputs must always produce the identical argv.
	a := AttachArgv("claude", "/home/debian.guest", "")
	b := AttachArgv("claude", "/home/debian.guest", "")
	if !slices.Equal(a, b) {
		t.Errorf("AttachArgv is not pure — two calls with the same inputs differ:\n\t%q\n\t%q", a, b)
	}
}

// TestAttachArgvGuestBranchesOnMain: the "does main exist?" decision has to be made
// IN THE GUEST. The host cannot see the guest's tmux server without a round trip
// that would race anyway.
func TestAttachArgvGuestBranchesOnMain(t *testing.T) {
	argv := AttachArgv("claude", "/home/debian.guest", "")
	expr, grouped, mainBranch := splitGuestExpr(t, argv)

	if !strings.Contains(expr, "tmux has-session") {
		t.Errorf("the guest expression does not branch on `tmux has-session`:\n\t%s", expr)
	}
	// The first client creates the canonical session...
	if !strings.Contains(mainBranch, "new-session -s main") {
		t.Errorf("the `main` branch does not create session `main`:\n\t%s", mainBranch)
	}
	// ...and it must NOT be grouped against anything (there is nothing to group with).
	if strings.Contains(mainBranch, "-t") {
		t.Errorf("the `main` branch groups against a target session, but it only runs when no session exists:\n\t%s", mainBranch)
	}
	// Every later client gets its OWN session sharing main's window set. A plain
	// re-attach to `main` would MIRROR the first client — same current window, display
	// clamped to the smallest client — which defeats the entire point of the feature.
	if !strings.Contains(grouped, "new-session -t") {
		t.Errorf("the grouped branch does not create a session grouped against `main` (`new-session -t`):\n\t%s\n"+
			"Attaching to `main` directly would mirror the first client and clamp both to the smaller terminal.", grouped)
	}
}

// TestAttachArgvColorterm pins the OTHER half of the truecolor handshake. The
// guest sshd carries `AcceptEnv COLORTERM` (roles/base), but `limactl shell`
// forwards no host environment, so the value has to be set on the guest tmux
// session here or Claude Code's UI silently drops to 256-color. Four things
// matter: a real value reaches BOTH new-session commands via `-e` (not a fragile
// `export`, which tmux drops once a server is already running), an absent one is
// NOT forwarded (so a non-truecolor terminal is reported honestly), a value that
// could break out of the shell expression is refused rather than escaped, and the
// tmux branch structure the other tests depend on is left intact.
func TestAttachArgvColorterm(t *testing.T) {
	// Forwarded: `-e COLORTERM=<v>` is spliced into BOTH new-session commands. It must
	// be `-e` on the session, not an `export` before the expression — see
	// guestAttachExpr: tmux hands a new pane the server's environment, so an exported
	// value is dropped for every attach after the one that started the server.
	expr, grouped, mainBranch := splitGuestExpr(t, AttachArgv("claude", "/home/debian.guest", "truecolor"))
	if strings.Contains(expr, "export COLORTERM") {
		t.Errorf("COLORTERM is set via a fragile `export` rather than `tmux new-session -e`:\n\t%s", expr)
	}
	if !strings.Contains(mainBranch, `tmux new-session -e COLORTERM=truecolor -s main`) {
		t.Errorf("the `main` branch does not set COLORTERM via `-e`:\n\t%s", mainBranch)
	}
	if !strings.Contains(grouped, `tmux new-session -e COLORTERM=truecolor -t =main`) {
		t.Errorf("the grouped branch does not set COLORTERM via `-e`:\n\t%s", grouped)
	}

	// Absent: no COLORTERM means no claim. The guest expression must be byte-for-byte
	// what it was before this feature — a terminal that sets nothing (Terminal.app)
	// is not to be told truecolor.
	if argv := AttachArgv("claude", "/home/debian.guest", ""); strings.Contains(argv[len(argv)-1], "COLORTERM") {
		t.Errorf("an empty COLORTERM was forwarded anyway:\n\t%s", argv[len(argv)-1])
	}

	// Refused: anything that is not a plain [A-Za-z0-9_-]+ token is dropped, not
	// escaped. These would otherwise inject a second command into the guest shell.
	for _, bad := range []string{
		"truecolor; rm -rf ~",
		"$(id)",
		"`id`",
		"a b",
		"x\ny",
		`x"y`,
		"x'y",
	} {
		got := AttachArgv("claude", "/home/debian.guest", bad)
		if e := got[len(got)-1]; strings.Contains(e, "COLORTERM") {
			t.Errorf("a shell-unsafe COLORTERM %q was forwarded into the guest expression:\n\t%s", bad, e)
		}
	}

	// Real-world values other than "truecolor" (e.g. 24bit) are still honored.
	got := AttachArgv("claude", "/home/debian.guest", "24bit")
	if e := got[len(got)-1]; !strings.Contains(e, "-e COLORTERM=24bit -s main") {
		t.Errorf("a valid COLORTERM=24bit was not forwarded:\n\t%s", e)
	}
}
