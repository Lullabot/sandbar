package landgh

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// CreateDraftPR opens a ONE-SHOT draft pull request for branch, entirely via
// `gh api` — never `gh pr create`, and never a local git checkout.
//
// # Which mechanism, and why
//
// The plan's two candidate mechanisms are `gh pr create --fill --draft` and
// the direct `gh api --method POST repos/<orgRepo>/pulls` path. This
// implementation uses the LATTER (the api path) exclusively, because it is
// fully deterministic from (org/repo, branch) alone: `gh pr create --fill`
// derives its title/body and base by reading the LOCAL git repository (the
// current checkout's HEAD and its remote), which land deliberately does not
// have — a headless host action over (org/repo, branch) has no working
// directory to inspect. `gh api` calls, by contrast, take every input as an
// explicit argument or JSON field, so this function resolves the same
// information gh's `--fill` would have inferred locally: the repo's default
// branch (from a GET to /repos/<orgRepo>) and the head commit's message
// (from a GET to /repos/<orgRepo>/commits/<branch>) — and passes them
// explicitly to the POST. All three calls are pure GitHub API reads/writes;
// none touches a local clone.
//
// # Steps
//
//  1. Resolve the base branch: `gh api repos/<orgRepo> --jq .default_branch`.
//  2. Resolve title/body from the branch's head commit message: `gh api
//     repos/<orgRepo>/commits/<branch> --jq .commit.message`, split on the
//     first blank line the way a git commit's subject/body split works. An
//     empty message (or a lookup that yields nothing) falls back to the
//     branch name as the title, so a create never fails purely for lack of a
//     commit message.
//  3. Create: `gh api --method POST repos/<orgRepo>/pulls -f head=<branch>
//     -f base=<default> -f title=<title> -f body=<body> -F draft=true`.
//
// Every value — orgRepo, branch, the resolved base, and the derived
// title/body — reaches gh as its own argv element via Runner.Output, never
// through a shell string, so an attacker-controlled branch name or commit
// message can only ever become PR text.
func (c *Client) CreateDraftPR(ctx context.Context, orgRepo, branch string) (*PR, error) {
	base, err := c.resolveDefaultBranch(ctx, orgRepo)
	if err != nil {
		return nil, err
	}

	title, body, err := c.resolveTitleBody(ctx, orgRepo, branch)
	if err != nil {
		return nil, err
	}

	out, err := c.run.Output(ctx, createDraftPRArgs(orgRepo, branch, base, title, body)...)
	if err != nil {
		return nil, fmt.Errorf("landgh: gh api create pull request: %w", err)
	}
	var resp struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"`
		Draft   bool   `json:"draft"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("landgh: decode gh api create pull request response: %w", err)
	}
	return &PR{Number: resp.Number, URL: resp.HTMLURL, State: resp.State, Draft: resp.Draft}, nil
}

// defaultBranchArgs builds the argv for `gh api repos/<orgRepo> --jq
// .default_branch`. orgRepo is concatenated into the API path as ONE argv
// element (still no shell), never split into separate arguments a shell
// could reinterpret.
func defaultBranchArgs(orgRepo string) []string {
	return []string{"api", "repos/" + orgRepo, "--jq", ".default_branch"}
}

func (c *Client) resolveDefaultBranch(ctx context.Context, orgRepo string) (string, error) {
	out, err := c.run.Output(ctx, defaultBranchArgs(orgRepo)...)
	if err != nil {
		return "", fmt.Errorf("landgh: resolve default branch: %w", err)
	}
	base := strings.TrimSpace(string(out))
	if base == "" {
		return "", fmt.Errorf("landgh: resolve default branch: gh returned an empty default_branch for %q", orgRepo)
	}
	return base, nil
}

// headCommitMessageArgs builds the argv for `gh api
// repos/<orgRepo>/commits/<branch> --jq .commit.message`. Both orgRepo and
// branch reach gh as text inside a single argv element — never
// shell-interpreted — so a branch name containing shell metacharacters
// cannot escape it.
func headCommitMessageArgs(orgRepo, branch string) []string {
	return []string{"api", "repos/" + orgRepo + "/commits/" + branch, "--jq", ".commit.message"}
}

func (c *Client) resolveTitleBody(ctx context.Context, orgRepo, branch string) (title, body string, err error) {
	out, runErr := c.run.Output(ctx, headCommitMessageArgs(orgRepo, branch)...)
	if runErr != nil {
		return "", "", fmt.Errorf("landgh: resolve head commit message: %w", runErr)
	}
	title, body = splitCommitMessage(string(out))
	if title == "" {
		// No commit message to derive a title from (or an empty repo state gh
		// api didn't error on) — fall back to the branch name so create never
		// fails purely for lack of a commit message.
		title = branch
	}
	return title, body, nil
}

// splitCommitMessage splits a git commit message into its subject (first
// line) and body (everything after the first blank line, trimmed), the same
// convention `gh pr create --fill` uses when deriving a PR title/body from a
// commit.
func splitCommitMessage(msg string) (title, body string) {
	msg = strings.TrimRight(msg, "\n")
	if msg == "" {
		return "", ""
	}
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		return msg[:i], strings.TrimSpace(msg[i+1:])
	}
	return msg, ""
}

// createDraftPRArgs builds the argv for `gh api --method POST
// repos/<orgRepo>/pulls -f head=<branch> -f base=<base> -f title=<title> -f
// body=<body> -F draft=true`. Every value — including attacker-influenced
// branch/title/body — is its own argv element; there is no shell to
// interpolate into, so it can only ever become field data in the POST body.
func createDraftPRArgs(orgRepo, head, base, title, body string) []string {
	return []string{
		"api", "--method", "POST", "repos/" + orgRepo + "/pulls",
		"-f", "head=" + head,
		"-f", "base=" + base,
		"-f", "title=" + title,
		"-f", "body=" + body,
		"-F", "draft=true",
	}
}
