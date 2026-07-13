// Package version answers "which sand is this?" — the one question a bug report
// always needs and a user can never remember.
package version

import (
	"runtime/debug"
	"strings"
)

// String resolves the build's identity, preferring the most specific thing it has.
//
// release is what GoReleaser stamps into main.version at build time
// (`-ldflags "-X main.version=…"`, see .goreleaser.yaml). It is the answer for a
// released binary and there is nothing better to say.
//
// A build from source has no tag, so it falls back to the git revision the Go
// toolchain embeds automatically — no ldflags, no Makefile, nothing to remember to
// pass — plus "-dirty" when the tree had uncommitted changes at build time. That
// suffix is the important half: "1a2b3c4" and "1a2b3c4-dirty" are different
// binaries, and only one of them corresponds to anything in the history.
func String(release string) string {
	if release != "" && release != "dev" {
		return release
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return "dev" // built outside a repo (go install of a tarball, some sandboxes)
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if dirty {
		rev += "-dirty"
	}
	return strings.TrimSpace(rev)
}
