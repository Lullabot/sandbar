package drupalorg

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// moduleNamePattern is a drupal.org project (module) machine name: lowercase
// letters, digits, and underscores only. It backs both moduleNameRe (outbound
// validation, in ForkPath) and moduleFromPathRe (inbound parsing, in
// ModuleFromRemoteURL) so the two directions cannot drift out of sync with
// each other.
const moduleNamePattern = `[a-z0-9_]+`

// moduleNameRe matches a drupal.org project (module) machine name in full.
var moduleNameRe = regexp.MustCompile(`^` + moduleNamePattern + `$`)

// ForkPath returns the path of a drupal.org issue fork for module and issue,
// following drupal.org's documented PROJECT-ISSUE_NUMBER naming convention:
// "issue/<module>-<nid>". It VALIDATES module and issue rather than
// sanitising them: a module name is always "[a-z0-9_]+" on drupal.org and an
// issue number is always a positive integer, so a value that doesn't fit
// that shape (a "/", a "..", shell metacharacters) didn't come from a
// correct derivation and must be rejected outright rather than escaped and
// carried on with.
func ForkPath(module string, issue int) (string, error) {
	if !moduleNameRe.MatchString(module) {
		return "", fmt.Errorf("drupalorg: invalid module name %q", module)
	}
	if issue <= 0 {
		return "", fmt.Errorf("drupalorg: invalid issue number %d", issue)
	}
	return fmt.Sprintf("issue/%s-%d", module, issue), nil
}

// moduleFromPathRe matches the "project/<module>" path drupal.org gives
// every contrib module's canonical repository.
var moduleFromPathRe = regexp.MustCompile(`^project/(` + moduleNamePattern + `)$`)

// forkFromPathRe matches the "issue/<module>-<nid>" path drupal.org gives
// every issue fork. It is the inbound counterpart of ForkPath's outbound
// formatting, built from the same moduleNamePattern so the two directions
// cannot drift out of sync — the same reason moduleFromPathRe is.
//
// The module group cannot swallow the "-<nid>" separator: "-" is not in
// moduleNamePattern, so the split stays unambiguous even for a module name
// that itself ends in digits ("issue/foo2-123" is module "foo2", issue 123).
// A module name containing "-" is not a drupal.org module name at all, so a
// path like "issue/foo-bar-123" correctly fails to match rather than being
// guessed at.
var forkFromPathRe = regexp.MustCompile(`^issue/(` + moduleNamePattern + `)-([0-9]+)$`)

// RemoteTarget is what a drupal.org git remote URL identifies: always a
// module, and — when the remote names an issue fork rather than the
// canonical project — the issue that fork belongs to.
//
// The Issue field is why this type exists rather than a bare module string.
// A developer working an issue clones the issue fork, so their checkout's
// remote is "issue/<module>-<nid>", which already NAMES the issue being
// worked on. Making them retype a number their own remote spells out is both
// busywork and a chance to publish onto the wrong fork, so both surfaces read
// it from here and only prompt when the remote genuinely cannot answer.
type RemoteTarget struct {
	// Module is the project machine name, e.g. "dubbot" — the same value on
	// a canonical remote and on an issue fork's, since a fork's path embeds
	// the module it forks.
	Module string
	// Issue is the drupal.org issue node ID the remote's fork belongs to, or
	// 0 when the remote names the canonical project and so says nothing
	// about any issue. Callers MUST read 0 as "the remote cannot answer;
	// ask" and never as an issue number — ForkPath rejects a non-positive
	// issue outright, which is the backstop if one ever slips through.
	Issue int
}

// TargetFromRemoteURL derives a RemoteTarget from a git remote URL,
// accepting both of the drupal.org repositories a developer can be working
// from:
//
//   - the canonical project, ".../project/<module>.git", yielding Issue 0
//   - an issue fork, ".../issue/<module>-<nid>.git", yielding both fields
//
// in all three spellings git remotes come in:
//
//   - HTTPS: "https://git.drupalcode.org/project/<module>.git"
//   - SSH (scp-like): "git@git.drupalcode.org:issue/<module>-<nid>.git"
//   - SSH (URL form): "ssh://git@git.drupalcode.org/project/<module>.git"
//
// Accepting the fork form is not a mere convenience: refusing it made the
// common case — a checkout cloned from the very fork publication targets —
// fail with "cannot derive module from remote path", which named nothing the
// developer could act on and pointed at the one remote that was already
// correct.
//
// raw MUST be the origin remote of the checkout being targeted by the
// operation at hand — the checkout the developer is actually publishing
// from. It must NEVER be derived from a VM's create-time CloneURL instead:
// CloneURL is recorded once per VM at creation time, so in a VM holding
// several drupal.org modules (a multi-module VM, e.g. a distribution's
// component checked out alongside the distribution itself) it identifies
// only the first module ever cloned into that VM. Deriving the module from
// CloneURL would silently resolve to the wrong fork whenever a later
// checkout in the same VM is the one actually being published — and, now
// that the issue is read from the remote too, would carry a stale issue
// number along with it.
func TargetFromRemoteURL(raw string) (RemoteTarget, error) {
	path, err := repoPathFromRemoteURL(raw)
	if err != nil {
		return RemoteTarget{}, err
	}

	if m := moduleFromPathRe.FindStringSubmatch(path); m != nil {
		return RemoteTarget{Module: m[1]}, nil
	}
	if m := forkFromPathRe.FindStringSubmatch(path); m != nil {
		// strconv cannot fail on `[0-9]+`, but it CAN overflow: a nid wider
		// than an int is not a drupal.org node ID, and must be refused here
		// rather than wrapped into a negative issue number that ForkPath
		// would then reject with a far less obvious message.
		nid, convErr := strconv.Atoi(m[2])
		if convErr != nil || nid <= 0 {
			return RemoteTarget{}, fmt.Errorf("drupalorg: remote path %q has an unusable issue number %q", path, m[2])
		}
		return RemoteTarget{Module: m[1], Issue: nid}, nil
	}

	return RemoteTarget{}, fmt.Errorf(
		"drupalorg: remote path %q is neither a canonical project (%q) nor an issue fork (%q)",
		path, "project/<module>", "issue/<module>-<issue>",
	)
}

// repoPathFromRemoteURL normalises a git remote URL of any spelling down to
// the repository path it addresses ("project/drupal", "issue/dubbot-3619578"),
// having first confirmed the host is git.drupalcode.org. It is split out from
// TargetFromRemoteURL so that URL shape and repository shape are decided
// separately: everything here is about git and SSH, everything in the caller
// is about drupal.org's naming conventions.
func repoPathFromRemoteURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("drupalorg: empty remote URL")
	}

	parseable := raw
	if !strings.Contains(raw, "://") {
		// scp-like SSH form ("user@host:path") is not a URL at all. OpenSSH
		// (and GitLab's own remote-URL display) treats this shorthand as
		// equivalent to "ssh://user@host/path", so it is normalised to that
		// form here — turning the FIRST ":" after "@" into "/" and
		// prepending the scheme — letting a single url.Parse call below
		// handle every input shape uniformly instead of hand-splitting this
		// one as a separate code path.
		at := strings.Index(raw, "@")
		colon := strings.Index(raw, ":")
		if at < 0 || colon < 0 || colon < at {
			return "", fmt.Errorf("drupalorg: unrecognized remote URL %q", raw)
		}
		parseable = "ssh://" + raw[:colon] + "/" + raw[colon+1:]
	}

	u, err := url.Parse(parseable)
	if err != nil {
		return "", fmt.Errorf("drupalorg: parsing remote URL %q: %w", raw, err)
	}
	host := u.Hostname()
	path := strings.TrimPrefix(u.Path, "/")

	if !strings.EqualFold(host, "git.drupalcode.org") {
		return "", fmt.Errorf("drupalorg: remote host %q is not git.drupalcode.org", host)
	}

	return strings.Trim(strings.TrimSuffix(path, ".git"), "/"), nil
}

// encodedProjectPath returns the "/projects/<escaped path>" segment for
// GitLab's project-scoped endpoints. GitLab's project endpoints accept a
// URL-encoded "namespace/project" path in place of a numeric project ID, so
// no project-ID lookup is ever needed to address a project by path. path is
// escaped with url.PathEscape (never string-concatenated raw) so that "/",
// "..", and any other character with URL meaning is percent-encoded rather
// than able to alter the request's path structure — this is the boundary
// between guest-influenced project-path text (a module name read from a
// checkout's remote) and the URL sand actually sends. A branch name is
// guest-influenced too and gets the same url.PathEscape treatment where it
// is used (see BranchExists), just not through this helper, since it is
// never part of a "/projects/..." segment itself.
func encodedProjectPath(path string) string {
	return "/projects/" + url.PathEscape(path)
}

// ProjectInfo is the subset of a GitLab project resource this package reads.
type ProjectInfo struct {
	ID                int                `json:"id"`
	DefaultBranch     string             `json:"default_branch"`
	ForkedFromProject *ForkedFromProject `json:"forked_from_project"`
}

// ForkedFromProject identifies an issue fork's canonical parent project —
// the project that actually holds merge requests, and the one authenticated
// writes in a later task target. It is nil when path does not name a fork
// (or the fork hasn't recorded a parent), which callers must treat as "no
// resolvable parent" rather than assuming one.
type ForkedFromProject struct {
	ID                int    `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	DefaultBranch     string `json:"default_branch"`
}

// Project fetches the project at path (e.g. a fork path from ForkPath, or a
// canonical module path like "project/drupal"), using no credential. A path
// that does not exist yields *APIError with Status 404 — checkable with
// IsNotFound — which is how callers confirm an issue fork exists before
// doing anything with it.
func (c *Client) Project(ctx context.Context, path string) (*ProjectInfo, error) {
	var p ProjectInfo
	if err := c.do(ctx, http.MethodGet, encodedProjectPath(path), nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// BranchExists reports whether branch exists on the project at path, using
// no credential. This is the check that later decides whether a
// commit-replay call needs to pass start_branch (branch missing) or can
// target the existing branch directly (branch present).
func (c *Client) BranchExists(ctx context.Context, path, branch string) (bool, error) {
	err := c.do(ctx, http.MethodGet, encodedProjectPath(path)+"/repository/branches/"+url.PathEscape(branch), nil, nil)
	switch {
	case err == nil:
		return true, nil
	case IsNotFound(err):
		return false, nil
	default:
		return false, err
	}
}

// MergeRequest is the subset of a GitLab merge request resource this package
// reads.
type MergeRequest struct {
	IID             int    `json:"iid"`
	WebURL          string `json:"web_url"`
	SourceProjectID int    `json:"source_project_id"`
	TargetProjectID int    `json:"target_project_id"`
	SourceBranch    string `json:"source_branch"`
	TargetBranch    string `json:"target_branch"`
	State           string `json:"state"`
}

// OpenMergeRequest looks up an already-open merge request whose source is
// sourceProjectID/sourceBranch, using no credential.
//
// parentPath MUST be the canonical parent's path (ForkedFromProject's
// PathWithNamespace), never the fork's own path. On drupal.org, merge
// requests are cross-project: an issue fork holds none of its own — its
// /merge_requests endpoint always answers "[]" — because opening a merge
// request from a fork files it against the project it was forked FROM. The
// verified example: on project/dubbot, a merge request opened from a fork
// carries source_project_id 241450 (the fork) but target_project_id 106348
// (dubbot itself), and it is listed only under dubbot's endpoint. Listing on
// the parent and filtering by source_project_id is therefore the only way to
// find "is there already an open MR from this fork's branch" — asking the
// fork itself would always report none, whether or not one exists.
func (c *Client) OpenMergeRequest(ctx context.Context, parentPath string, sourceProjectID int, sourceBranch string) (*MergeRequest, error) {
	query := url.Values{
		"state":         {"opened"},
		"source_branch": {sourceBranch},
		// GitLab's default page size (20) is not enough to guarantee this
		// call sees every open MR whose source_branch matches: several forks
		// of the same issue commonly land on the identical branch name
		// (drupal.org's own MR-creation UI derives it from the issue title),
		// so more than 20 candidates is realistic on a busy issue. Requesting
		// the maximum page size makes a silent, pagination-hidden "no
		// existing MR" — which would cause a duplicate MR to be opened —
		// far less likely.
		"per_page": {"100"},
	}
	var mrs []MergeRequest
	if err := c.do(ctx, http.MethodGet, encodedProjectPath(parentPath)+"/merge_requests", query, &mrs); err != nil {
		return nil, err
	}
	for _, mr := range mrs {
		if mr.SourceProjectID == sourceProjectID {
			m := mr
			return &m, nil
		}
	}
	return nil, nil
}
