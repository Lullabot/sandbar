package provision

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
)

// TestGitCredSlug pins the slug grammar the task spells out verbatim: the
// empty (global) scope slugs to "default", and a non-empty scope collapses any
// run of non-alphanumerics to a single '-'.
func TestGitCredSlug(t *testing.T) {
	cases := []struct{ scope, want string }{
		{"", "default"},
		{"github.com/acme", "github-com-acme"},
		{"a/b.c__d", "a-b-c-d"},
		{"simple", "simple"},
	}
	for _, c := range cases {
		if got := gitCredSlug(c.scope); got != c.want {
			t.Errorf("gitCredSlug(%q) = %q, want %q", c.scope, got, c.want)
		}
	}
}

// TestCollectGitCredEntries_RecognizedScopedKey confirms a recognized KEY
// (GH_TOKEN) in a non-empty scope produces exactly one wiring entry, using the
// table's host/user and the correct slug.
func TestCollectGitCredEntries_RecognizedScopedKey(t *testing.T) {
	scopes := map[string]map[string]string{
		"github.com/acme": {"GH_TOKEN": "tok-value"},
	}
	entries := collectGitCredEntries(scopes)
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 entry, got %d: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.scope != "github.com/acme" || e.slug != "github-com-acme" || e.host != "github.com" || e.user != "x-access-token" || e.token != "tok-value" {
		t.Fatalf("unexpected entry: %+v", e)
	}
}

// TestCollectGitCredEntries_NonRecognizedKeyProducesNoWiring is the explicit
// negative case the task calls out: a scoped secret whose KEY is not in
// recognizedForgeTokens must produce zero git-credential wiring, even though it
// is still delivered as a plain scoped env var by the (unrelated) dotenv path.
func TestCollectGitCredEntries_NonRecognizedKeyProducesNoWiring(t *testing.T) {
	scopes := map[string]map[string]string{
		"github.com/acme": {"SOME_OTHER_TOKEN": "v"},
	}
	entries := collectGitCredEntries(scopes)
	if len(entries) != 0 {
		t.Fatalf("non-recognized key must produce no wiring, got %+v", entries)
	}
}

// TestCollectGitCredEntries_GlobalScopeNotWired: this redesign's scope for
// credential wiring is the non-empty-scope case only (see the task's
// implementation notes); a GH_TOKEN in the global ("") scope must not produce a
// git-credential entry.
func TestCollectGitCredEntries_GlobalScopeNotWired(t *testing.T) {
	scopes := map[string]map[string]string{
		"": {"GH_TOKEN": "v"},
	}
	entries := collectGitCredEntries(scopes)
	if len(entries) != 0 {
		t.Fatalf("global-scope GH_TOKEN must not be wired into git credentials, got %+v", entries)
	}
}

// TestRenderGitCredentialLine pins main's proven git-credential-store line
// format verbatim: "https://<user>:<token>@<host>\n".
func TestRenderGitCredentialLine(t *testing.T) {
	e := gitCredEntry{scope: "github.com/acme", slug: "github-com-acme", host: "github.com", user: "x-access-token", token: "SECRETVALUE"}
	got := renderGitCredentialLine(e)
	want := "https://x-access-token:SECRETVALUE@github.com\n"
	if got != want {
		t.Fatalf("renderGitCredentialLine = %q, want %q", got, want)
	}
}

// TestRenderGitconfigInclude_FileArgIsAbsoluteNotTilde is the correctness
// property main proved the hard way: credential.helper's --file= argument is a
// plain shell word, not a config path value, so git does NOT tilde-expand it
// (unlike includeIf's gitdir:/path=). A rendered include must therefore use an
// absolute path and must contain no '~'.
func TestRenderGitconfigInclude_FileArgIsAbsoluteNotTilde(t *testing.T) {
	got := renderGitconfigInclude("/home/andrew", "github-com-acme")
	if !strings.Contains(got, "--file=/home/andrew/.config/sandbar/git-credentials/github-com-acme") {
		t.Fatalf("include must contain the absolute --file= path; got=%q", got)
	}
	if strings.Contains(got, "~") {
		t.Fatalf("include's --file= must be absolute, not tilde-relative; got=%q", got)
	}
}

// TestRenderGitconfigManagedBlock_ScopedBeforeDefault asserts the ordering
// property the task calls out explicitly: git's credential subsystem stops at
// the FIRST helper that returns a full credential — it does not prefer a more
// specific one found later — so every scoped includeIf stanza must precede any
// unconditional default [credential] helper line.
func TestRenderGitconfigManagedBlock_ScopedBeforeDefault(t *testing.T) {
	entries := []gitCredEntry{
		{scope: "", slug: "default", host: "github.com", user: "x-access-token", token: "v"},
		{scope: "github.com/acme", slug: "github-com-acme", host: "github.com", user: "x-access-token", token: "v"},
	}
	got := renderGitconfigManagedBlock(entries, "/home/andrew")

	includeIdx := strings.Index(got, `includeIf "gitdir:~/github.com/acme/"`)
	defaultIdx := strings.Index(got, "[credential]")
	if includeIdx < 0 {
		t.Fatalf("expected an includeIf stanza for the scoped entry; got=%q", got)
	}
	if defaultIdx < 0 {
		t.Fatalf("expected an unconditional default [credential] line; got=%q", got)
	}
	if includeIdx > defaultIdx {
		t.Fatalf("scoped includeIf must precede the default [credential] helper; got=%q", got)
	}
	// includeIf's gitdir:/path= ARE tilde-expanded by git, so ~/ is correct here
	// (the opposite rule from the --file= case above).
	if !strings.Contains(got, "path = ~/.config/sandbar/gitconfig.d/github-com-acme") {
		t.Fatalf("expected a tilde-relative path= line for the includeIf stanza; got=%q", got)
	}
}

// TestRenderGitconfigManagedBlock_EmptyClearsBlock: an empty entry set renders
// an empty string, so applying it against the guest's marker-delimited section
// clears any previously-written block (a removed token stops authenticating).
func TestRenderGitconfigManagedBlock_EmptyClearsBlock(t *testing.T) {
	got := renderGitconfigManagedBlock(nil, "/home/andrew")
	if got != "" {
		t.Fatalf("empty entries must render an empty block, got %q", got)
	}
}

// TestApplySecrets_ScopedGHTokenWritesGitCredentials covers the end-to-end
// wiring for a scoped GH_TOKEN: it must still be delivered as a plain env var
// (task 03, unaffected) AND additionally produce a per-scope git-credentials
// write (token over stdin, never argv) and a gitconfig reconcile pass.
func TestApplySecrets_ScopedGHTokenWritesGitCredentials(t *testing.T) {
	const token = "GH_TOKEN_SENTINEL"
	f := &fakeRunner{}
	cli := lima.New(f)

	scopes := map[string]map[string]string{
		"github.com/acme": {"GH_TOKEN": token},
	}
	if err := ApplySecrets(context.Background(), cli, "claude", "andrew", scopes, io.Discard); err != nil {
		t.Fatalf("ApplySecrets: %v", err)
	}

	var wroteCred, ranReconcile bool
	for _, argv := range f.calls {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "git-credentials") && strings.Contains(joined, "install -m 600 /dev/null") {
			wroteCred = true
			if !strings.Contains(joined, "-H") || !strings.Contains(joined, "-u") {
				t.Fatalf("git-credential write must run as sudo -H -u <user>; argv=%v", argv)
			}
			if !hasTok(argv, "-i") && !hasTok(argv, "-iu") {
				// good: must not be a login shell
			} else {
				t.Fatalf("git-credential write must not use a login shell; argv=%v", argv)
			}
			// The token must be on stdin for this call, never on argv.
			for _, a := range argv {
				if strings.Contains(a, token) {
					t.Fatalf("token leaked onto argv element %q", a)
				}
			}
		}
		if strings.Contains(joined, "is_desired") {
			ranReconcile = true
		}
		for _, a := range argv {
			if strings.Contains(a, token) {
				t.Fatalf("token leaked onto argv in call %v", argv)
			}
		}
	}
	if !wroteCred {
		t.Fatalf("expected a git-credentials write call; calls=%v", f.calls)
	}
	if !ranReconcile {
		t.Fatalf("expected a reconcile call (prune + regenerate ~/.gitconfig); calls=%v", f.calls)
	}

	// The token is expected on TWO streams: the plain scoped .env write
	// (task 03, unaffected KEY=VALUE dotenv form) and the git-credential
	// write (the proven https://user:token@host line). Only the latter is
	// asserted for exact form here.
	var sawCredLine bool
	for _, s := range f.streams {
		if strings.Contains(s, "https://x-access-token:"+token+"@github.com") {
			sawCredLine = true
		}
	}
	if !sawCredLine {
		t.Fatalf("expected the proven https://user:token@host credential line on some stdin stream; streams=%v", f.streams)
	}
}

// TestApplySecrets_GitCredReconcileRunsUnconditionally covers the
// reconciliation requirement: even with zero scoped secrets (nothing to wire),
// the reconcile pass still runs so a PREVIOUSLY-wired, now-removed token's
// on-disk files are pruned and the managed ~/.gitconfig block is cleared.
func TestApplySecrets_GitCredReconcileRunsUnconditionally(t *testing.T) {
	f := &fakeRunner{}
	cli := lima.New(f)

	if err := ApplySecrets(context.Background(), cli, "claude", "andrew", map[string]map[string]string{}, io.Discard); err != nil {
		t.Fatalf("ApplySecrets: %v", err)
	}
	var ranReconcile bool
	for _, argv := range f.calls {
		if strings.Contains(strings.Join(argv, " "), "is_desired") {
			ranReconcile = true
		}
	}
	if !ranReconcile {
		t.Fatalf("expected the reconcile pass to run even with no scoped secrets; calls=%v", f.calls)
	}
}
