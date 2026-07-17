package landgh

import (
	"context"
	"fmt"
)

// CompareURL is gh-free: it constructs the GitHub compare/create-PR URL for
// branch by string concatenation only, so it works even with no host gh at
// all — the graceful-degradation fallback for the one-key draft-create
// action. branch is never shell-interpreted; it becomes URL text.
func CompareURL(orgRepo, branch string) (string, error) {
	if err := validateOrgRepo(orgRepo); err != nil {
		return "", err
	}
	if branch == "" {
		return "", fmt.Errorf("landgh: CompareURL: branch is empty")
	}
	return fmt.Sprintf("https://github.com/%s/pull/new/%s", orgRepo, branch), nil
}

// PRURL is gh-free: it constructs the URL of an existing PR from orgRepo and
// its number, for the "open in browser" action once a PR is known to exist.
func PRURL(orgRepo string, number int) (string, error) {
	if err := validateOrgRepo(orgRepo); err != nil {
		return "", err
	}
	if number <= 0 {
		return "", fmt.Errorf("landgh: PRURL: invalid PR number %d", number)
	}
	return fmt.Sprintf("https://github.com/%s/pull/%d", orgRepo, number), nil
}

// OpenInBrowser opens target — a URL built by CompareURL/PRURL, or the URL
// field gh returned for an existing PR — in the user's browser via the
// injected Opener. It is gh-free: no gh call is made here.
func (c *Client) OpenInBrowser(ctx context.Context, target string) error {
	if target == "" {
		return fmt.Errorf("landgh: OpenInBrowser: empty URL")
	}
	return c.open(ctx, target)
}
