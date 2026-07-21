package landgh

import (
	"context"
	"encoding/json"
	"fmt"
)

// PR is the authoritative state of a branch's pull request, as reported by
// `gh pr list`. A nil *PR (returned alongside a nil error) means no PR exists
// for the branch.
type PR struct {
	Number int
	URL    string
	State  string // e.g. "OPEN", "CLOSED", "MERGED"
	Draft  bool
}

// prListArgs builds the argv for `gh pr list -R <orgRepo> --head <branch>
// --json ...`. orgRepo and branch are passed straight through as their own
// slice elements — never concatenated into a shell string — so a branch name
// containing shell metacharacters reaches gh as inert text, not a command.
func prListArgs(orgRepo, branch string) []string {
	return []string{"pr", "list", "-R", orgRepo, "--head", branch, "--json", "number,url,state,isDraft"}
}

// prListEntry mirrors one element of `gh pr list --json
// number,url,state,isDraft`'s JSON array.
type prListEntry struct {
	Number  int    `json:"number"`
	URL     string `json:"url"`
	State   string `json:"state"`
	IsDraft bool   `json:"isDraft"`
}

// PRState runs the AUTHORITATIVE branch/PR check on the workstation: `gh pr
// list -R orgRepo --head branch --json number,url,state,isDraft`. An empty
// result means no PR exists for branch (nil, nil) — that is not an error.
func (c *Client) PRState(ctx context.Context, orgRepo, branch string) (*PR, error) {
	out, err := c.run.Output(ctx, prListArgs(orgRepo, branch)...)
	if err != nil {
		return nil, fmt.Errorf("landgh: gh pr list: %w", err)
	}
	var entries []prListEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("landgh: decode gh pr list output: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil
	}
	e := entries[0]
	return &PR{Number: e.Number, URL: e.URL, State: e.State, Draft: e.IsDraft}, nil
}
