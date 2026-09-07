package drupalorg

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// defaultIssueBaseURL is drupal.org's public, credential-free node API root.
//
// It is a DIFFERENT host from defaultBaseURL: git.drupalcode.org serves the
// GitLab API that publication writes through, while www.drupal.org serves
// the issue nodes themselves. An issue's title lives only on the latter —
// GitLab knows the fork "issue/<module>-<nid>" but nothing about the issue
// that named it — so titling a merge request after its issue means reading
// from both.
const defaultIssueBaseURL = "https://www.drupal.org/api-d7"

// issueLookupTimeout bounds the issue-title read specifically, independently
// of whatever longer deadline the caller's context carries.
//
// The title is a convenience: publication is entirely correct without it,
// falling back to the branch name. So this read must never be the thing that
// makes a publish hang, and a caller's generous end-to-end budget (the TUI
// allows five minutes per resolve step, for a change-set collection that
// reads whole blobs out of a guest over ssh) is far too much rope for one
// small JSON GET. Exceeding it is not an error worth failing a publish over
// — see LookupIssueTitle, which reports it as "no title", not as a fault.
const issueLookupTimeout = 15 * time.Second

// maxMergeRequestTitle is GitLab's limit on a merge request title. A longer
// one is rejected outright, which would turn a cosmetic improvement into a
// failed publish — so MergeRequestTitle truncates rather than risking it.
const maxMergeRequestTitle = 255

// IssueInfo is the subset of a drupal.org issue node this package reads.
// Everything here is public: the node API takes no credential, and the
// account PAT publication holds is never attached to a request for it.
type IssueInfo struct {
	// NID is the issue's node ID, as confirmed by the response rather than
	// as asked for.
	NID int
	// Title is the issue's own title, exactly as drupal.org holds it.
	//
	// It is third-party text — anyone with a drupal.org account can file an
	// issue and title it — so although it is not GUEST-supplied (which is
	// the distinction the package doc comment turns on), it is not this
	// program's text either. MergeRequestTitle sanitizes it before it can
	// reach either a rendered confirmation or a merge request.
	Title string
	// Project is the machine name of the project the issue was filed
	// against, e.g. "dubbot". Callers compare it against the module being
	// published to: an issue belonging to a different project is not the
	// issue this publication is about, whatever its number.
	Project string
}

// issueNode is the wire shape of drupal.org's api-d7 node resource, narrowed
// to the three facts IssueInfo carries.
//
// nid arrives as a JSON STRING ("3619578"), not a number, which is a d7
// REST-server convention rather than an oversight — decoding it into an int
// would fail on every response. FieldProject is json.RawMessage for a
// related reason: the same API renders an absent object-valued field as an
// empty ARRAY ([]), so a *struct there would fail to decode on exactly the
// nodes that have no project, and take the whole lookup down with it.
type issueNode struct {
	Title        string          `json:"title"`
	Type         string          `json:"type"`
	NID          string          `json:"nid"`
	FieldProject json.RawMessage `json:"field_project"`
}

// issueNodeType is the drupal.org content type an issue node has. A node ID
// that resolves to anything else (a project page, a forum post, a book page)
// is not an issue, and its title must not be used to name a merge request
// about one.
const issueNodeType = "project_issue"

// Issue fetches the public drupal.org issue node nid, anonymously.
//
// It is a READ of a public web resource and attaches no credential, exactly
// like Project. It also refuses to return anything but an actual issue: a
// node of another type, or one whose own nid does not match the one asked
// for, yields an error rather than a title that would go on to name a merge
// request after the wrong thing.
func (c *Client) Issue(ctx context.Context, nid int) (*IssueInfo, error) {
	if nid <= 0 {
		return nil, fmt.Errorf("drupalorg: invalid issue number %d", nid)
	}

	path := "/node/" + strconv.Itoa(nid) + ".json"
	full, err := c.issueEndpoint(path)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, fmt.Errorf("drupalorg: building GET %s request: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")

	httpClient := c.http
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("drupalorg: GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	// interpretResponse is reused verbatim: www.drupal.org is the very site
	// whose edge serves the HTML that looksBlocked recognises, so an issue
	// read benefits from exactly the same "this is a web page, not an API
	// response" detection a GitLab read does — and gets the same *APIError
	// for a 404, which is what a wrong node ID produces.
	var node issueNode
	if err := interpretResponse(http.MethodGet, path, resp, &node); err != nil {
		return nil, err
	}

	if node.Type != issueNodeType {
		return nil, fmt.Errorf("drupalorg: node %d is a %q, not a %q", nid, node.Type, issueNodeType)
	}
	gotNID, convErr := strconv.Atoi(strings.TrimSpace(node.NID))
	if convErr != nil || gotNID != nid {
		return nil, fmt.Errorf("drupalorg: node %d answered with nid %q", nid, node.NID)
	}

	return &IssueInfo{NID: gotNID, Title: node.Title, Project: issueProject(node.FieldProject)}, nil
}

// issueProject reads field_project's machine name, tolerating the two shapes
// drupal.org's API renders that field in: the object it is when the node
// belongs to a project, and the empty array it is when the node does not.
// An unreadable field yields "" — "this node names no project" — never an
// error: the caller's own comparison against the module already treats "" as
// "cannot vouch for this issue", which is the same conclusion.
func issueProject(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var p struct {
		MachineName string `json:"machine_name"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ""
	}
	return p.MachineName
}

// issueEndpoint renders the absolute URL for a node-API path. It mirrors
// endpoint's string-concatenation approach and its reason (see that method:
// assigning to url.URL.Path would double-encode an already-escaped path),
// against the issue base rather than the GitLab one.
func (c *Client) issueEndpoint(path string) (string, error) {
	base := defaultIssueBaseURL
	if c.issueBase != "" {
		base = c.issueBase
	}
	return strings.TrimSuffix(base, "/") + path, nil
}

// LookupIssueTitle returns the merge-request title issue nid warrants for
// module, or "" when it cannot establish one.
//
// EVERY failure yields "" rather than an error, and that is the point of the
// function existing at all: publication is completely correct with the
// branch-name title it has always used, so a title lookup must never be able
// to fail a publish. A drupal.org outage, an edge refusal, a node that turns
// out not to be an issue, a slow response — each simply means "no better
// title is available", and the caller falls back.
//
// The issue must also belong to module. An issue number that resolves to
// some OTHER project's issue is not this publication's issue whatever its
// number, and naming a merge request after it would put a confidently wrong
// title on a permanent, public write. Refusing a title there leaves the
// branch name, which is never wrong, only terse.
func LookupIssueTitle(ctx context.Context, c *Client, module string, nid int) string {
	if c == nil || nid <= 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, issueLookupTimeout)
	defer cancel()

	info, err := c.Issue(ctx, nid)
	if err != nil || info == nil {
		return ""
	}
	if info.Project != module {
		return ""
	}
	return MergeRequestTitle(nid, info.Title)
}

// MergeRequestTitle formats an issue's own title into the merge-request
// title drupal.org's conventions expect — "Issue #<nid>: <title>", the same
// shape the project's commit messages use — or "" when there is no title to
// format.
//
// The title is sanitized (sanitizeLine, the confirmation renderer's own) and
// truncated to GitLab's limit. Sanitizing matters even though this text is
// not guest-supplied: it is still third-party text, it is rendered into the
// confirmation a human approves a permanent public write from, and a newline
// in it could otherwise forge a line of that confirmation. This is the same
// rule RenderConfirmation applies at the same kind of boundary, applied
// once, here, so that the confirmation and the merge request are guaranteed
// to carry the identical string.
func MergeRequestTitle(nid int, issueTitle string) string {
	clean := strings.TrimSpace(sanitizeLine(issueTitle))
	if clean == "" {
		return ""
	}
	title := fmt.Sprintf("Issue #%d: %s", nid, clean)
	if len(title) > maxMergeRequestTitle {
		title = strings.TrimSpace(title[:maxMergeRequestTitle])
	}
	return title
}
