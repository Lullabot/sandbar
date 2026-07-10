package provision

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
)

// TestApplySecrets_SecretNeverOnArgv is the load-bearing security property: the
// rendered secret text must be streamed over stdin only. It must never appear in
// any element of the command's argv, where a host `ps` would leak it to every
// user on the machine.
func TestApplySecrets_SecretNeverOnArgv(t *testing.T) {
	const secret = "SENTINEL_SECRET_VALUE"
	f := &fakeRunner{}
	cli := lima.New(f)

	scopes := map[string]map[string]string{"": {"TOK": secret}}
	if err := ApplySecrets(context.Background(), cli, "claude", "andrew", scopes, io.Discard); err != nil {
		t.Fatalf("ApplySecrets: %v", err)
	}

	// Global-only apply: the write call plus the profile/bashrc ensure call.
	if len(f.calls) != 2 {
		t.Fatalf("want exactly 2 limactl calls (write + ensure-profile), got %d: %v", len(f.calls), f.calls)
	}
	if len(f.streams) != 1 {
		t.Fatalf("want exactly 1 streamed stdin, got %d", len(f.streams))
	}

	// The secret MUST be on stdin (the file body streamed into the guest).
	if !strings.Contains(f.streams[0], secret) {
		t.Fatalf("secret must appear in the streamed stdin; stdin=%q", f.streams[0])
	}
	// The rendered, single-quote-wrapped export line is what lands on stdin.
	if want := "export TOK='" + secret + "'\n"; !strings.Contains(f.streams[0], want) {
		t.Fatalf("stdin missing rendered export line %q; stdin=%q", want, f.streams[0])
	}

	// The secret MUST NOT appear on argv — not in any single element, and not in
	// the joined argv (the exact form the acceptance criterion names), across
	// EVERY call ApplySecrets made.
	for _, argv := range f.calls {
		for _, a := range argv {
			if strings.Contains(a, secret) {
				t.Fatalf("secret leaked onto an argv element %q; full argv=%v", a, argv)
			}
		}
		if strings.Contains(strings.Join(argv, " "), secret) {
			t.Fatalf("secret leaked into the joined argv: %v", argv)
		}
	}

	// Sanity on the transport: it is a guest shell that writes the file from stdin
	// as the target user, with the 0600 install-before-write hygiene.
	argv := f.calls[0]
	joined := strings.Join(argv, " ")
	if !hasTok(argv, "shell") || !hasTok(argv, "claude") {
		t.Fatalf("expected a `shell claude ...` call, got %v", argv)
	}
	if !hasTok(argv, "andrew") {
		t.Fatalf("expected the target user on argv, got %v", argv)
	}
	if !strings.Contains(joined, "install -m 600 /dev/null") {
		t.Fatalf("apply script must create the file 0600 before writing; argv=%v", argv)
	}
	if !strings.Contains(joined, "cat > ") {
		t.Fatalf("apply script must stream the body via cat; argv=%v", argv)
	}
	if !strings.Contains(joined, "secrets.env") || !strings.Contains(joined, ".config/sandbar") {
		t.Fatalf("apply script must target ~/.config/sandbar/secrets.env; argv=%v", argv)
	}
}

// TestApplySecrets_EnsuresProfileAndBashrcSourcing covers the AC bullet that the
// global apply (re)establishes source lines in BOTH ~/.profile and ~/.bashrc,
// idempotently (a grep-guarded marker, not a blind append every time).
func TestApplySecrets_EnsuresProfileAndBashrcSourcing(t *testing.T) {
	f := &fakeRunner{}
	cli := lima.New(f)

	scopes := map[string]map[string]string{"": {"TOK": "v"}}
	if err := ApplySecrets(context.Background(), cli, "claude", "andrew", scopes, io.Discard); err != nil {
		t.Fatalf("ApplySecrets: %v", err)
	}

	var found bool
	for _, argv := range f.calls {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, ".profile") && strings.Contains(joined, ".bashrc") {
			found = true
			if !strings.Contains(joined, "grep") {
				t.Fatalf("profile/bashrc ensure step must be idempotent (grep-guarded); argv=%v", argv)
			}
		}
	}
	if !found {
		t.Fatalf("expected a call referencing both .profile and .bashrc; calls=%v", f.calls)
	}
}

// TestApplySecrets_SudoDoesNotUseLoginShell guards a bug found only against a
// live guest: `sudo -i` runs the target user's LOGIN shell, which re-parses the
// command, so the multi-line script arrived mangled ("set: -c: invalid option")
// and the write silently failed. `sudo -H -u` sets HOME without that extra shell
// layer. A fake runner cannot catch this by executing the script, so pin the
// flags instead — for every multi-line call ApplySecrets makes (write, clear,
// ensure-profile, and the scoped write).
func TestApplySecrets_SudoDoesNotUseLoginShell(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scopes map[string]map[string]string
	}{
		{"apply", map[string]map[string]string{"": {"TOK": "v"}}},
		{"clear", map[string]map[string]string{}},
		{"scoped", map[string]map[string]string{"github.com/acme": {"GH_TOKEN": "v"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{}
			if err := ApplySecrets(context.Background(), lima.New(f), "claude", "andrew", tc.scopes, io.Discard); err != nil {
				t.Fatalf("ApplySecrets: %v", err)
			}
			for _, argv := range f.calls {
				// The single-word `direnv allow` call legitimately uses `-iu`; the
				// guest-home lookup (getent) is a plain, non-sudo call; skip both.
				if hasTok(argv, "direnv") || hasTok(argv, "getent") {
					continue
				}
				for _, bad := range []string{"-i", "-iu"} {
					if hasTok(argv, bad) {
						t.Fatalf("sudo %s runs a login shell that re-parses the script and mangles it; argv=%v", bad, argv)
					}
				}
				if !hasTok(argv, "-H") || !hasTok(argv, "-u") {
					t.Fatalf("expected `sudo -H -u <user>` so HOME is the target user's home; argv=%v", argv)
				}
			}
		})
	}
}

// TestApplySecrets_EmptyRemovesGuestFile: with no global pairs, ApplySecrets
// removes the guest secrets.env file (rm -f) instead of writing an empty one,
// and streams no stdin for that call.
func TestApplySecrets_EmptyRemovesGuestFile(t *testing.T) {
	f := &fakeRunner{}
	cli := lima.New(f)

	if err := ApplySecrets(context.Background(), cli, "claude", "andrew", map[string]map[string]string{}, io.Discard); err != nil {
		t.Fatalf("ApplySecrets: %v", err)
	}

	if len(f.calls) != 2 {
		t.Fatalf("want exactly 2 limactl calls (clear + ensure-profile), got %d: %v", len(f.calls), f.calls)
	}
	argv := f.calls[0]
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "rm -f") || !strings.Contains(joined, "secrets.env") {
		t.Fatalf("empty apply must run the rm -f clear script for secrets.env; argv=%v", argv)
	}
	// It must NOT run the write path.
	if strings.Contains(joined, "cat > ") || strings.Contains(joined, "install -m 600") {
		t.Fatalf("empty apply must not run the write script; argv=%v", argv)
	}
	// The clear path streams no stdin (fakeRunner only records non-nil stdin).
	if len(f.streams) != 0 {
		t.Fatalf("clear path must not stream any stdin, got %d: %v", len(f.streams), f.streams)
	}
}

// TestApplySecrets_NilScopesRemovesGuestFile: a nil map is treated the same as
// an empty one — the global guest file is removed, nothing is streamed, and no
// scope is applied.
func TestApplySecrets_NilScopesRemovesGuestFile(t *testing.T) {
	f := &fakeRunner{}
	cli := lima.New(f)

	if err := ApplySecrets(context.Background(), cli, "claude", "andrew", nil, io.Discard); err != nil {
		t.Fatalf("ApplySecrets(nil): %v", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("want 2 calls, got %d: %v", len(f.calls), f.calls)
	}
	if !strings.Contains(strings.Join(f.calls[0], " "), "rm -f") {
		t.Fatalf("nil scopes must run the clear script; argv=%v", f.calls[0])
	}
	if len(f.streams) != 0 {
		t.Fatalf("nil scopes must not stream stdin, got %v", f.streams)
	}
}

// TestApplySecrets_ScopedWritesEnvAndDirenvAllow covers a non-empty scope: the
// scope renders to ~/<scope>/.env (0600, dir 0755) with the body over stdin,
// followed by `direnv allow` on the resolved guest home + scope path.
func TestApplySecrets_ScopedWritesEnvAndDirenvAllow(t *testing.T) {
	const secret = "SCOPED_SENTINEL"
	f := &fakeRunner{}
	cli := lima.New(f)

	scopes := map[string]map[string]string{
		"github.com/acme": {"GH_TOKEN": secret},
	}
	if err := ApplySecrets(context.Background(), cli, "claude", "andrew", scopes, io.Discard); err != nil {
		t.Fatalf("ApplySecrets: %v", err)
	}

	// global clear, ensure-profile, guest-home lookup (getent), scoped write,
	// direnv allow.
	if len(f.calls) != 5 {
		t.Fatalf("want 5 calls (clear, ensure-profile, getent, scoped write, direnv allow), got %d: %v", len(f.calls), f.calls)
	}

	var wroteScope, ranDirenv bool
	for _, argv := range f.calls {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "install -d -m 755") && strings.Contains(joined, "github.com/acme") {
			wroteScope = true
			if !strings.Contains(joined, "install -m 600 /dev/null") {
				t.Fatalf("scoped .env must be created 0600 before writing; argv=%v", argv)
			}
			if !strings.Contains(joined, "cat > ") {
				t.Fatalf("scoped .env body must stream via cat; argv=%v", argv)
			}
		}
		if hasTok(argv, "direnv") {
			ranDirenv = true
			if !hasTok(argv, "allow") {
				t.Fatalf("expected `direnv allow`, got %v", argv)
			}
			// The path passed to direnv allow must be the resolved guest home
			// (from getent, canned as /home/andrew by fakeRunner) plus the scope.
			if !hasTok(argv, "/home/andrew/github.com/acme") {
				t.Fatalf("expected direnv allow on /home/andrew/github.com/acme; argv=%v", argv)
			}
			if !hasTok(argv, "-iu") {
				t.Fatalf("direnv allow (single word) should use sudo -iu; argv=%v", argv)
			}
		}
	}
	if !wroteScope {
		t.Fatalf("expected a call writing ~/github.com/acme/.env; calls=%v", f.calls)
	}
	if !ranDirenv {
		t.Fatalf("expected a direnv allow call; calls=%v", f.calls)
	}

	// The secret must be on stdin for the scoped write, in dotenv KEY=VALUE form
	// (not the `export ...` form used for the global secrets.env).
	var sawDotenvLine bool
	for _, s := range f.streams {
		if strings.Contains(s, "GH_TOKEN='"+secret+"'") {
			sawDotenvLine = true
		}
		if strings.Contains(s, "export ") {
			t.Fatalf("scoped .env must use dotenv KEY=VALUE form, not `export ...`; stream=%q", s)
		}
	}
	if !sawDotenvLine {
		t.Fatalf("expected the scoped stream to contain GH_TOKEN='%s'; streams=%v", secret, f.streams)
	}
}

// TestApplySecrets_EmptyScopesMapAppliesNoScopedFiles ensures a scope key that
// maps to an empty/nil pairs map (which secrets.Store never persists, but a
// caller could still hand in) is skipped rather than producing an empty write.
func TestApplySecrets_EmptyScopesMapAppliesNoScopedFiles(t *testing.T) {
	f := &fakeRunner{}
	cli := lima.New(f)

	scopes := map[string]map[string]string{"github.com/acme": {}}
	if err := ApplySecrets(context.Background(), cli, "claude", "andrew", scopes, io.Discard); err != nil {
		t.Fatalf("ApplySecrets: %v", err)
	}
	// Only the global clear + ensure-profile calls; no scoped write, no direnv,
	// no guest-home lookup.
	if len(f.calls) != 2 {
		t.Fatalf("want 2 calls, got %d: %v", len(f.calls), f.calls)
	}
	for _, argv := range f.calls {
		if hasTok(argv, "direnv") || hasTok(argv, "getent") {
			t.Fatalf("an empty scope must not trigger a scoped write or direnv allow; argv=%v", argv)
		}
	}
}

// TestRenderDotenv_LiteralSafety is the meaningful test the plan calls out
// explicitly: a value containing a single quote, a command substitution, and a
// backtick must reach the rendered dotenv text completely literally — none of
// them may be interpreted as shell/dotenv syntax by direnv's loader.
func TestRenderDotenv_LiteralSafety(t *testing.T) {
	pairs := map[string]string{
		"QUOTE":    `it's a test`,
		"CMDSUB":   "$(id)",
		"BACKTICK": "`whoami`",
	}
	got := RenderDotenv(pairs)

	// Each value must appear byte-for-byte inside its single-quoted KEY='...'
	// line, proving no expansion syntax was interpreted or stripped.
	cases := []struct{ key, want string }{
		{"QUOTE", `QUOTE='it'\''s a test'` + "\n"},
		{"CMDSUB", `CMDSUB='$(id)'` + "\n"},
		{"BACKTICK", "BACKTICK='`whoami`'\n"},
	}
	for _, c := range cases {
		if !strings.Contains(got, c.want) {
			t.Fatalf("RenderDotenv missing literal-safe line %q for key %s; got=%q", c.want, c.key, got)
		}
	}
	// Sanity: dotenv KEY=VALUE form (no `export ` prefix — that's Render's
	// POSIX-source form, a different destination/consumer).
	if strings.Contains(got, "export ") {
		t.Fatalf("RenderDotenv must not emit the `export` form; got=%q", got)
	}
}

// TestRenderDotenv_SkipsInvalidKeys mirrors secrets.Render's second line of
// defense: a key that is not secrets.ValidKey is emitted unquoted and so could
// never be represented safely, so it is dropped rather than rendered.
func TestRenderDotenv_SkipsInvalidKeys(t *testing.T) {
	got := RenderDotenv(map[string]string{"bad-key": "x", "GOOD_KEY": "y"})
	if strings.Contains(got, "bad-key") {
		t.Fatalf("RenderDotenv must skip an invalid key; got=%q", got)
	}
	if !strings.Contains(got, "GOOD_KEY='y'\n") {
		t.Fatalf("RenderDotenv must still render a valid key; got=%q", got)
	}
}

// TestRenderDotenv_ByteStable pins sorted-key, trailing-newline output so
// equal input always renders identical bytes (matters for idempotent guest
// writes and for diffing in tests).
func TestRenderDotenv_ByteStable(t *testing.T) {
	got := RenderDotenv(map[string]string{"B": "2", "A": "1"})
	want := "A='1'\nB='2'\n"
	if got != want {
		t.Fatalf("RenderDotenv not byte-stable/sorted: got=%q want=%q", got, want)
	}
}
