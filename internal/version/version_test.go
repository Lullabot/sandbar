package version

import (
	"regexp"
	"testing"
)

// A released binary says its tag and nothing else.
func TestReleaseTagWins(t *testing.T) {
	if got := String("v1.4.0"); got != "v1.4.0" {
		t.Fatalf("String(%q) = %q, want the tag", "v1.4.0", got)
	}
}

// A build from source has no tag, so it falls back to the revision the Go toolchain
// embeds automatically — including the -dirty suffix, which is the important half:
// "1a2b3c4" and "1a2b3c4-dirty" are different binaries and only one of them
// corresponds to anything in the history.
func TestSourceBuildFallsBackToTheRevision(t *testing.T) {
	got := String("dev")
	// `go test` builds from the working tree, so this binary carries VCS info.
	if !regexp.MustCompile(`^([0-9a-f]{7}(-dirty)?|dev)$`).MatchString(got) {
		t.Fatalf("String(%q) = %q, want a short revision (optionally -dirty), or dev outside a repo", "dev", got)
	}
}
