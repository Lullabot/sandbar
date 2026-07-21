package landgh

import (
	"fmt"
	"regexp"
)

// orgRepoPattern matches a GitHub "owner/repo" shape: each side is one or more
// alphanumerics, dots, underscores, or hyphens, separated by exactly one
// slash. It rejects anything a shell would treat specially (spaces,
// semicolons, backticks, `$(...)`, extra slashes) BEFORE that string is ever
// used to build a URL or a `repos/<org/repo>` gh API path segment. This is
// defense in depth, not the injection guard itself: every gh call in this
// package passes orgRepo as one argv element via os/exec, never through a
// shell, so it cannot execute regardless. Validation just keeps obviously
// malformed input from becoming a broken URL or gh API call.
var orgRepoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// validateOrgRepo reports whether orgRepo has the "owner/repo" shape gh and
// GitHub's URLs expect.
func validateOrgRepo(orgRepo string) error {
	if !orgRepoPattern.MatchString(orgRepo) {
		return fmt.Errorf("landgh: invalid org/repo %q: want \"owner/repo\"", orgRepo)
	}
	return nil
}
