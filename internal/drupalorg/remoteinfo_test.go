package drupalorg

import (
	"strings"
	"testing"
)

// TestParseRemoteInfo covers the parser both surfaces now share. It lived as
// two identical private copies — one in cmd/sand, one in internal/ui — whose
// error text had already drifted apart; this is the single test that replaces
// both.
func TestParseRemoteInfo(t *testing.T) {
	t.Run("valid two-line output", func(t *testing.T) {
		remote, upstream, err := ParseRemoteInfo([]byte("https://git.drupalcode.org/project/foo.git\norigin/1.0.x\n"))
		if err != nil {
			t.Fatalf("ParseRemoteInfo: unexpected error: %v", err)
		}
		if remote != "https://git.drupalcode.org/project/foo.git" || upstream != "origin/1.0.x" {
			t.Errorf("ParseRemoteInfo = (%q, %q), want the two lines verbatim", remote, upstream)
		}
	})

	t.Run("malformed output is refused, never guessed at", func(t *testing.T) {
		for _, out := range []string{
			"only one line\n",
			"",
			"\n\n",
			"https://git.drupalcode.org/project/foo.git\n\n",
			"one\ntwo\nthree\n",
		} {
			if _, _, err := ParseRemoteInfo([]byte(out)); err == nil {
				t.Errorf("ParseRemoteInfo(%q): want an error, got none — a half-read remote must not become a destination", out)
			}
		}
	})
}

// TestBuildRemoteInfoCommandIsFixedAndReadOnly pins two properties the script
// depends on for its safety story: it is a fixed literal with nothing spliced
// into it (the checkout is selected by the provider's --workdir, not by
// interpolation), and every git verb in it only reads.
func TestBuildRemoteInfoCommandIsFixedAndReadOnly(t *testing.T) {
	got := BuildRemoteInfoCommand()
	if got != BuildRemoteInfoCommand() {
		t.Fatal("BuildRemoteInfoCommand is not deterministic")
	}
	// Write and network verbs must be absent: this script runs in a guest that
	// holds no drupal.org credential and must only ever read local git state.
	for _, forbidden := range []string{"push", "fetch", "clone", "git commit"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("remote-info script contains %q; it must be read-only and never contact a remote", forbidden)
		}
	}
	// "__" is BuildCollectCommand's token-substitution marker. Its absence here
	// is the point: unlike that script, this one has nothing spliced into it,
	// so no checkout-derived text can reach the shell. (A bare "%s" IS present
	// — printf's own format string — which is exactly why the check is for the
	// substitution marker rather than for a percent sign.)
	if strings.Contains(got, "__") {
		t.Error("remote-info script carries a substitution token; it must be a fixed literal")
	}
}
