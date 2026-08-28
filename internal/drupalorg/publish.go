package drupalorg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// This file holds the only code in sand that WRITES to drupal.org, plus the
// two anonymous reads that exist solely to serve those writes
// (branchCommits and lastCommitForPath — the package's other credential-free
// reads live in fork.go). Three behaviours of GitLab's content API differ
// observably from a git push, and they are recorded here so nobody
// rediscovers them as bugs:
//
//   - Commits created this way are NOT GPG-signed by the developer. The
//     commit's author is whatever the payload said; its committer is always
//     the owner of the personal access token.
//   - last_commit_id is the ONLY concurrency guard if the fork moved
//     underneath. GitLab treats it as the last known commit of the FILE named
//     by the action ("Last known file commit ID" in its own documentation),
//     not as the branch tip — see lastCommitIDs.
//   - Requests are rate-limited above 20 MB and rejected above 300 MB, so
//     size is checked per call before anything is sent.
//
// And one behaviour of this package: there is NO ROLLBACK. A replay of N
// commits is N sequential public writes, so call 2 of 3 can fail with call 1
// already published and unrevocable. Nothing here attempts to undo a landed
// commit — instead a partial run is reported exactly (see Result) and a
// re-run resumes from where it stopped (see alreadyLandedCount).

// ErrForkMoved reports that the fork changed underneath a publish: a file an
// update action targeted is no longer at the commit the change set was
// derived against. It is a "re-derive and retry" outcome rather than a
// failure of sand — the change set must be collected again from a guest that
// has fetched the fork's current state, and the publish re-run.
//
// It is recognised from GitLab's own wording for that condition plus an
// HTTP 409, since the API offers no machine-readable code for it. A refusal
// GitLab words differently therefore surfaces as a plain *APIError; that
// degrades the label, never the honesty of the report.
var ErrForkMoved = errors.New("the fork moved underneath this publish; re-derive the change set and retry")

// staleFileMarker is the distinguishing fragment of GitLab's message when an
// action's last_commit_id no longer matches the file's last commit ("You are
// attempting to update a file <path> that has changed since you started
// editing it.").
const staleFileMarker = "has changed since you started editing it"

// commitRateLimitBytes and commitRejectBytes are the content API's two size
// thresholds. They are vars, not consts, only so tests can lower them: a test
// that allocated 300 MB to prove a refusal would be worse than the bug it
// guards against.
//
// Both are measured PER CALL, against one commit's encoded content, which is
// why a change set too large to send as a single commit may still publish
// perfectly well as a replay.
var (
	commitRateLimitBytes int64 = 20 << 20
	commitRejectBytes    int64 = 300 << 20
)

// commitsPerPage is GitLab's maximum page size for the commits endpoint.
const commitsPerPage = 100

// CommitStatus is what became of one change-set commit during a publish run.
type CommitStatus string

const (
	// CommitAlreadyPresent means the commit was found already on the branch
	// and was not sent again — the resumption case. See alreadyLandedCount
	// for exactly how "already present" is decided.
	CommitAlreadyPresent CommitStatus = "already-present"
	// CommitLanded means this run created the commit on the fork branch.
	CommitLanded CommitStatus = "landed"
	// CommitFailed means this run tried to create the commit and the API
	// refused. At most one commit in a run carries this status: the replay
	// stops at the first failure.
	CommitFailed CommitStatus = "failed"
	// CommitNotAttempted means the commit was never sent, because an earlier
	// one failed. Its work is still entirely unpublished.
	CommitNotAttempted CommitStatus = "not-attempted"
)

// CommitResult is what happened to one commit of the change set. Index is
// the commit's position in the ChangeSet, so a caller can line the report up
// against what it confirmed. Subject is the message's first line, carried
// so a report can name a commit without holding the change set.
type CommitResult struct {
	Index   int
	Subject string
	Status  CommitStatus
	// SHA is the commit's id on the fork: the one the API returned for a
	// landed commit, or the one already on the branch for an already-present
	// one. It is empty for a failed or unattempted commit.
	SHA string
	// Err is the cause, set only on the one CommitFailed entry.
	Err error
}

// Result reports what a publish run actually did. It exists because an error
// alone cannot describe a partial public write: "commit 1 landed as abc123,
// commit 2 was refused, commit 3 was never sent" is a fact a caller must be
// able to show a human, and there is no rollback that would make it moot.
//
// Publish always returns a Result, and returns it alongside any error rather
// than instead of one, so a partial run can never be presented as either a
// clean success or a clean failure.
type Result struct {
	// Commits has one entry per change-set commit, in change-set order,
	// naming which landed (with their SHAs), which did not, and why the
	// first failure happened.
	Commits []CommitResult
	// MergeRequest is the merge request now covering this branch: the one
	// this run opened, or the one that was already open. It is nil when the
	// run stopped before that point.
	MergeRequest *MergeRequest
	// MergeRequestOpened distinguishes the two: true only when this run
	// created it. A publish onto a branch whose merge request is already
	// open must never open a second one.
	MergeRequestOpened bool
	// Warnings carries non-fatal observations, such as a commit large enough
	// to be rate-limited by the content API. They are computed over the whole
	// change set before publication begins — deliberately, since every check
	// that could refuse a publish must run before the first write — so a
	// resumed run may warn about a commit it then finds already present and
	// never sends.
	Warnings []string
}

// FirstFailure returns the commit whose publication failed, or nil if none
// did.
func (r Result) FirstFailure() *CommitResult {
	for i := range r.Commits {
		if r.Commits[i].Status == CommitFailed {
			return &r.Commits[i]
		}
	}
	return nil
}

// Publisher performs authenticated writes to drupal.org. It is deliberately
// a separate type from Client: Client is credential-free by construction and
// every method on it reads anonymously, while a Publisher is the only thing
// in this package that ever attaches the account PAT to a request.
type Publisher struct {
	client *Client
	// authHTTP is the transport used for credential-bearing requests. It is
	// the anonymous client's, copied with redirects refused. That matters:
	// Go strips only its own list of sensitive headers (Authorization,
	// Cookie, ...) when following a cross-host redirect, and PRIVATE-TOKEN
	// is not on that list — so a redirect would hand the account token to
	// whatever host it pointed at. Refusing to follow makes that
	// unreachable; the 3xx is reported as an ordinary API error instead.
	authHTTP *http.Client
}

// NewPublisher returns a Publisher that reads anonymously through c and
// writes with the workstation's drupal.org PAT, loaded lazily at the moment
// of the first authenticated call (see LoadToken). No token is read, held,
// or cached by construction.
func NewPublisher(c *Client) *Publisher {
	// A copy, so c's own client is never mutated, and a copy of c's rather
	// than a fresh one, so any transport, timeout, or cookie jar c was
	// configured with applies to authenticated calls too. Only CheckRedirect
	// is overridden.
	auth := *c.http
	auth.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Publisher{client: c, authHTTP: &auth}
}

// Publish replays cs onto dest, one authenticated POST per commit, and then
// opens a merge request against dest's canonical parent unless one is
// already open.
//
// The order of operations is load-bearing. Everything knowable anonymously —
// the fork's identity, whether the branch exists, what is already on it,
// whether a merge request is already open, and each updated file's last
// commit — is resolved BEFORE the first authenticated call, and every
// host-side check that could refuse the change set runs before that too. A
// publish that is going to be refused must be refused while nothing has been
// written, because after the first successful call there is nothing to undo.
//
// It returns a Result in every case, including alongside an error.
func (p *Publisher) Publish(ctx context.Context, dest Destination, cs ChangeSet) (Result, error) {
	var res Result

	if err := validateForPublication(dest, cs); err != nil {
		return res, err
	}
	// One entry per change-set commit, always: sized up front because that
	// is exactly what Result.Commits ends up holding on every path that
	// reaches the replay.
	res.Commits = make([]CommitResult, 0, len(cs.Commits))

	warnings, err := checkCommitSizes(cs)
	if err != nil {
		return res, err
	}
	res.Warnings = warnings

	// --- Anonymous phase. No credential is read or sent below this line
	// until the replay begins. ---

	fork, err := p.client.Project(ctx, dest.ForkPath)
	if err != nil {
		return res, fmt.Errorf("drupalorg: resolving fork %q: %w", dest.ForkPath, err)
	}
	if fork.ID <= 0 {
		return res, fmt.Errorf("drupalorg: fork %q reported no project id; cannot identify its merge requests", dest.ForkPath)
	}

	// drupal.org creates the issue branch alongside the fork, so it almost
	// always exists already and start_branch must then be omitted — sending
	// it against an existing branch is an error. The uncommon case starts
	// from the fork's own default_branch, which GitLab sets to the branch
	// the fork was taken from.
	branchExists, err := p.client.BranchExists(ctx, dest.ForkPath, dest.Branch)
	if err != nil {
		return res, fmt.Errorf("drupalorg: checking whether %q exists on %q: %w", dest.Branch, dest.ForkPath, err)
	}
	var startBranch string
	if !branchExists {
		if fork.DefaultBranch == "" {
			return res, fmt.Errorf("drupalorg: branch %q does not exist on %q and the fork reports no default_branch to create it from", dest.Branch, dest.ForkPath)
		}
		startBranch = fork.DefaultBranch
	}

	var history []remoteCommit
	if branchExists {
		history, err = p.client.branchCommits(ctx, dest.ForkPath, dest.Branch, len(cs.Commits))
		if err != nil {
			return res, fmt.Errorf("drupalorg: reading %q on %q to resume: %w", dest.Branch, dest.ForkPath, err)
		}
	}
	present := alreadyLandedCount(history, cs.Commits)
	pending := cs.Commits[present:]
	for i := range present {
		res.Commits = append(res.Commits, CommitResult{
			Index:   i,
			Subject: subjectOf(cs.Commits[i].Message),
			Status:  CommitAlreadyPresent,
			SHA:     history[present-1-i].ID,
		})
	}

	// Looked up before the replay rather than after it, so that a publish
	// which could not then propose its work — no merge request open and no
	// parent branch to target — is refused while nothing has been written.
	// The cost is a vanishingly small race: a merge request opened by
	// someone else during the replay itself would not be seen. Preferring
	// that to an unrevocable write is the whole point.
	existingMR, err := p.client.OpenMergeRequest(ctx, dest.ParentPath, fork.ID, dest.Branch)
	if err != nil {
		return res, fmt.Errorf("drupalorg: looking for an open merge request from %q on %q: %w", dest.Branch, dest.ParentPath, err)
	}
	if existingMR == nil && dest.ParentBranch == "" {
		return res, fmt.Errorf("drupalorg: no merge request is open from %q and the canonical parent %q reports no branch to target; refusing to publish commits that could not then be proposed", dest.Branch, dest.ParentPath)
	}

	lastCommits, err := p.lastCommitIDs(ctx, dest, pending, branchExists)
	if err != nil {
		return res, err
	}

	// --- Authenticated phase. ---

	var token string
	credential := func() (string, error) {
		if token == "" {
			loaded, err := LoadToken()
			if err != nil {
				return "", err
			}
			token = loaded
		}
		return token, nil
	}

	// notAttempted records the tail of a replay that was never sent, so a
	// failure report distinguishes "refused" from "never tried".
	notAttempted := func(from int) {
		for j := from; j < len(pending); j++ {
			res.Commits = append(res.Commits, CommitResult{
				Index:   present + j,
				Subject: subjectOf(pending[j].Message),
				Status:  CommitNotAttempted,
			})
		}
	}

	for i, commit := range pending {
		index := present + i

		tok, err := credential()
		if err != nil {
			notAttempted(i)
			return res, err
		}

		sha, err := p.postCommit(ctx, dest, commit, startBranch, lastCommits, tok)
		if err != nil {
			failed := CommitResult{
				Index:   index,
				Subject: subjectOf(commit.Message),
				Status:  CommitFailed,
				Err:     err,
			}
			res.Commits = append(res.Commits, failed)
			notAttempted(i + 1)
			return res, publicationError(dest, res, failed, len(cs.Commits))
		}

		res.Commits = append(res.Commits, CommitResult{
			Index:   index,
			Subject: subjectOf(commit.Message),
			Status:  CommitLanded,
			SHA:     sha,
		})
		if sha == "" {
			// The commit is public but unidentified, so the rest of the
			// replay cannot be chained to it. Stopping here leaves a state a
			// re-run can resume from; guessing would not.
			notAttempted(i + 1)
			return res, fmt.Errorf("drupalorg: %s landed on %s but the API returned no commit id, so the remaining commits cannot be chained to it; re-run the publish to resume", commitLabel(index, len(cs.Commits), subjectOf(commit.Message)), dest.Branch)
		}

		// Every path this commit touched now has this commit as its last —
		// which is what chains the replay: the next call's expected parent
		// for those files is the commit this call just created.
		for _, a := range commit.Actions {
			lastCommits[a.Path] = sha
			if a.PreviousPath != "" {
				lastCommits[a.PreviousPath] = sha
			}
		}
		// The branch exists from here on, whether or not it did before.
		startBranch = ""
	}

	if existingMR != nil {
		mr := *existingMR
		res.MergeRequest = &mr
		return res, nil
	}

	tok, err := credential()
	if err != nil {
		return res, err
	}
	mr, err := p.postMergeRequest(ctx, dest, tok)
	if err != nil {
		return res, fmt.Errorf("drupalorg: opening a merge request from %q against %q: %w", dest.Branch, dest.ParentPath, err)
	}
	res.MergeRequest = mr
	res.MergeRequestOpened = true
	return res, nil
}

// publicationError renders a replay failure as an error that cannot be
// mistaken for a clean one: it names which call failed, what was already
// published by this run before it did, and that there is no rollback.
func publicationError(dest Destination, res Result, failed CommitResult, total int) error {
	var landed int
	for _, c := range res.Commits {
		if c.Status == CommitLanded {
			landed++
		}
	}

	partial := ""
	if landed > 0 {
		partial = fmt.Sprintf("; %d commit(s) from this run are already public on %s and there is no rollback — re-run the publish to land the rest", landed, dest.Branch)
	}
	return fmt.Errorf("drupalorg: publishing %s to %s on %s: %w%s", commitLabel(failed.Index, total, failed.Subject), dest.Branch, dest.ForkPath, failed.Err, partial)
}

// validateForPublication refuses a change set or destination the API would
// reject anyway, before any of it is sent. Doing it here rather than
// discovering it call by call is what stops a publish from landing commit 1
// and then failing host-side validation on commit 2.
func validateForPublication(dest Destination, cs ChangeSet) error {
	switch {
	case dest.ForkPath == "":
		return errors.New("drupalorg: destination has no fork path")
	case dest.Branch == "":
		return errors.New("drupalorg: destination has no branch")
	case dest.ParentPath == "" || dest.ParentID <= 0:
		return fmt.Errorf("drupalorg: destination has no canonical parent (id %d, path %q)", dest.ParentID, dest.ParentPath)
	case len(cs.Commits) == 0:
		return errors.New("drupalorg: change set contains no commits; nothing to publish")
	}

	for i, c := range cs.Commits {
		where := commitLabel(i, len(cs.Commits), subjectOf(c.Message))
		if strings.TrimSpace(c.Message) == "" {
			return fmt.Errorf("drupalorg: %s has an empty commit message", where)
		}
		if len(c.Actions) == 0 {
			return fmt.Errorf("drupalorg: %s has no file actions; the content API cannot create an empty commit", where)
		}
		for _, a := range c.Actions {
			if err := ValidateFileAction(a); err != nil {
				return fmt.Errorf("drupalorg: %s: %w", where, err)
			}
		}
	}
	return nil
}

// checkCommitSizes weighs each commit against the content API's two
// thresholds before anything is sent, and returns the rate-limit warnings.
// The measure is the commit's encoded content (CommitEncodedSize), which is
// the dominant and only unbounded term in the request body.
func checkCommitSizes(cs ChangeSet) ([]string, error) {
	var warnings []string
	for i, c := range cs.Commits {
		size := CommitEncodedSize(c)
		where := commitLabel(i, len(cs.Commits), subjectOf(c.Message))
		switch {
		case size > commitRejectBytes:
			return nil, fmt.Errorf("drupalorg: %s carries %d bytes of encoded content, over the content API's %d byte rejection threshold; each commit is one request, so splitting this commit — not the change set, which is already replayed one commit per request — is what would make it publishable", where, size, commitRejectBytes)
		case size > commitRateLimitBytes:
			warnings = append(warnings, fmt.Sprintf("%s carries %d bytes of encoded content, over the content API's %d byte rate-limit threshold; this request may be throttled", where, size, commitRateLimitBytes))
		}
	}
	return warnings, nil
}

// remoteCommit is the subset of a GitLab commit resource resumption reads.
type remoteCommit struct {
	ID          string `json:"id"`
	Message     string `json:"message"`
	AuthorName  string `json:"author_name"`
	AuthorEmail string `json:"author_email"`
}

// branchCommits returns at most limit of branch's newest commits, newest
// first, using no credential.
//
// It lives in publish.go rather than fork.go because it exists solely for
// resumption, and it is bounded by limit for a concrete reason: an issue
// fork's branch carries its project's ENTIRE upstream history — hundreds of
// thousands of commits on a fork of project/drupal — and resumption can
// never need more than the change set's own length, since only a prefix of
// the change set can be already-present.
func (c *Client) branchCommits(ctx context.Context, projectPath, branch string, limit int) ([]remoteCommit, error) {
	// per_page is computed ONCE and held constant for every page, because
	// GitLab paginates this endpoint by offset — page N starts at
	// (N-1)*per_page. Shrinking per_page on a later page to "just the
	// remainder" would move that offset backwards and re-read commits
	// already collected, producing a history with duplicates in it and so
	// silently defeating resumption for any change set longer than one page
	// (which would then replay already-public commits a second time). The
	// last page is trimmed to limit afterwards instead.
	perPage := min(limit, commitsPerPage)
	var out []remoteCommit
	for page := 1; len(out) < limit; page++ {
		query := url.Values{
			"ref_name": {branch},
			"per_page": {strconv.Itoa(perPage)},
			"page":     {strconv.Itoa(page)},
		}
		var got []remoteCommit
		if err := c.do(ctx, http.MethodGet, encodedProjectPath(projectPath)+"/repository/commits", query, &got); err != nil {
			return nil, err
		}
		out = append(out, got...)
		if len(got) < perPage {
			break
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// lastCommitForPath returns the id of the newest commit on branch that
// touched filePath, or "" if none did, using no credential.
func (c *Client) lastCommitForPath(ctx context.Context, projectPath, branch, filePath string) (string, error) {
	query := url.Values{
		"ref_name": {branch},
		"path":     {filePath},
		"per_page": {"1"},
	}
	var got []remoteCommit
	if err := c.do(ctx, http.MethodGet, encodedProjectPath(projectPath)+"/repository/commits", query, &got); err != nil {
		return "", err
	}
	if len(got) == 0 {
		return "", nil
	}
	return got[0].ID, nil
}

// lastCommitIDs seeds the path -> last-known-commit map that update actions
// send as last_commit_id.
//
// GitLab defines last_commit_id as the last known commit of the FILE the
// action names — not the branch tip and not, on its own, the previous call
// in a replay. Sending a replay's previous SHA for a file that commit did
// not touch would be rejected as a conflict on the very first commit that
// edits a file the one before it left alone, which is most of them. So each
// updated path is seeded here with its own last commit, read anonymously,
// and the replay then advances an entry only when a call actually touches
// that path. The chain the plan asks for falls out of that: a file updated
// by two consecutive commits carries the first call's returned SHA on the
// second, while a file's first touch carries the value that detects the fork
// having moved underneath — the one thing last_commit_id can detect at all.
func (p *Publisher) lastCommitIDs(ctx context.Context, dest Destination, pending []Commit, branchExists bool) (map[string]string, error) {
	seeds := map[string]string{}
	if !branchExists {
		// Nothing can have a last commit on a branch that does not exist.
		return seeds, nil
	}
	seen := map[string]bool{}
	for _, c := range pending {
		for _, a := range c.Actions {
			if a.Kind != ActionUpdate || seen[a.Path] {
				continue
			}
			seen[a.Path] = true
			sha, err := p.client.lastCommitForPath(ctx, dest.ForkPath, dest.Branch, a.Path)
			if err != nil {
				return nil, fmt.Errorf("drupalorg: reading the last commit for %q on %q: %w", a.Path, dest.Branch, err)
			}
			if sha != "" {
				seeds[a.Path] = sha
			}
		}
	}
	return seeds, nil
}

// commitIdentity is how a change-set commit is recognised as already being
// on the fork branch. See alreadyLandedCount for why it is these fields.
type commitIdentity struct {
	message     string
	authorName  string
	authorEmail string
}

func identityOf(message, authorName, authorEmail string) commitIdentity {
	return commitIdentity{
		// Trailing whitespace is normalised away because a forge stores a
		// commit message with a trailing newline whether or not one was
		// sent, so the message read back is not byte-identical to the one
		// posted.
		message:     strings.TrimRight(strings.ReplaceAll(message, "\r\n", "\n"), " \t\n"),
		authorName:  strings.TrimSpace(authorName),
		authorEmail: strings.TrimSpace(authorEmail),
	}
}

// alreadyLandedCount returns how many of the change set's LEADING commits
// are already the tail of the branch's history, and so must not be sent
// again. history is newest-first; commits is the change set in order.
//
// # The resumption-identity rule, and why it is this one
//
// A replayed commit's SHA is not the guest's local SHA — the content API
// creates a new commit with a new tree and a new parent — and the payload
// type carries no SHA at all, by design. So identity is (commit message,
// author name, author email), normalised, matched as an ordered run:
// history[0] must be the change set's commit k-1, history[1] its k-2, and
// so on down to history[k-1] == commits[0]. The largest such k wins.
//
// Three properties follow from matching an ordered run anchored at the tip
// rather than testing set membership commit by commit:
//
//   - A change set that legitimately repeats a message (two "Fix typo"
//     commits) resumes correctly. Membership would skip both once one had
//     landed.
//   - An unrelated older commit deep in the fork's history that happens to
//     share a message and author cannot be mistaken for this run's work.
//   - A first run and an nth run are the same code path. Publishing
//     iteratively — where the guest's change set still starts at the
//     commits an earlier publish already replayed — is the ordinary case,
//     not just the failure case.
//
// What it deliberately does NOT do is compare the commit's tree effect. That
// would need one diff request per candidate commit, against an endpoint
// drupal.org's edge allowlist has not been verified to route: the probe in
// plan 21 covers GET /repository/commits (which this uses) and does not
// cover the per-commit diff endpoint, and an unroutable resumption read
// would break resumption altogether rather than merely weaken it.
//
// The accepted cost is one case: a commit amended locally so that its
// content changed while its message and author did not is treated as already
// landed, and the amendment is not published. That is visible in the Result,
// which reports every skipped commit by subject, and it is the same class of
// judgement the confirmation surface already puts in front of a human. Every
// OTHER kind of divergence — a branch someone else has pushed to, a change
// set derived against stale state — is caught by last_commit_id instead, and
// surfaces as ErrForkMoved rather than as a silent skip.
func alreadyLandedCount(history []remoteCommit, commits []Commit) int {
	n := min(len(history), len(commits))

	// Identities are normalised once each, outside the k loop: an entry's
	// identity does not depend on k, so computing it inside would rescan
	// every message once per candidate k.
	onBranch := make([]commitIdentity, n)
	wanted := make([]commitIdentity, n)
	for i := range n {
		onBranch[i] = identityOf(history[i].Message, history[i].AuthorName, history[i].AuthorEmail)
		wanted[i] = identityOf(commits[i].Message, commits[i].AuthorName, commits[i].AuthorEmail)
	}

	for k := n; k > 0; k-- {
		match := true
		for i := range k {
			if onBranch[i] != wanted[k-1-i] {
				match = false
				break
			}
		}
		if match {
			return k
		}
	}
	return 0
}

// commitAction is one action in a POST /repository/commits body. Content is
// a pointer so that a genuinely empty file is sent as "" rather than being
// dropped by omitempty, while a delete carries no content key at all.
type commitAction struct {
	Action       string  `json:"action"`
	FilePath     string  `json:"file_path"`
	PreviousPath string  `json:"previous_path,omitempty"`
	Content      *string `json:"content,omitempty"`
	Encoding     string  `json:"encoding,omitempty"`
	LastCommitID string  `json:"last_commit_id,omitempty"`
}

// commitRequest is a POST /repository/commits body. StartBranch is omitempty
// because sending it against an existing branch is an error, and the branch
// usually exists.
type commitRequest struct {
	Branch        string         `json:"branch"`
	StartBranch   string         `json:"start_branch,omitempty"`
	CommitMessage string         `json:"commit_message"`
	AuthorName    string         `json:"author_name,omitempty"`
	AuthorEmail   string         `json:"author_email,omitempty"`
	Actions       []commitAction `json:"actions"`
}

// postCommit replays one commit as one authenticated call and returns the
// commit id the API assigned it.
func (p *Publisher) postCommit(ctx context.Context, dest Destination, commit Commit, startBranch string, lastCommits map[string]string, token string) (string, error) {
	body := commitRequest{
		Branch:        dest.Branch,
		StartBranch:   startBranch,
		CommitMessage: commit.Message,
		AuthorName:    commit.AuthorName,
		AuthorEmail:   commit.AuthorEmail,
		Actions:       make([]commitAction, 0, len(commit.Actions)),
	}
	for _, a := range commit.Actions {
		body.Actions = append(body.Actions, actionFor(a, lastCommits))
	}

	var out struct {
		ID string `json:"id"`
	}
	if err := p.post(ctx, encodedProjectPath(dest.ForkPath)+"/repository/commits", token, body, &out); err != nil {
		// asForkMoved is applied HERE rather than in post(), because a 409
		// means "the fork moved" only on the content API. On
		// POST /merge_requests a 409 is GitLab's "Another open merge request
		// already exists for this source branch" — the race the anonymous
		// OpenMergeRequest lookup above openly accepts — and labelling that
		// ErrForkMoved would tell a developer whose commits all landed to
		// re-derive a change set that is perfectly current.
		return "", asForkMoved(err)
	}
	return out.ID, nil
}

// actionFor maps one payload FileAction onto the content API's action shape.
func actionFor(a FileAction, lastCommits map[string]string) commitAction {
	out := commitAction{Action: string(a.Kind), FilePath: a.Path}
	withContent := func() {
		content := a.Content
		out.Content = &content
		out.Encoding = string(a.Encoding)
	}
	switch a.Kind {
	case ActionCreate, ActionUpdate:
		withContent()
		if a.Kind == ActionUpdate {
			out.LastCommitID = lastCommits[a.Path]
		}
	case ActionMove:
		out.PreviousPath = a.PreviousPath
		// A move's content is optional: with it the file is moved and
		// edited, without it the bytes are carried across unchanged.
		// ValidateFileAction applies the same "empty means none" reading.
		if a.Content != "" {
			withContent()
		}
	case ActionDelete:
		// No content, no encoding.
	}
	return out
}

// mergeRequestRequest is a POST /merge_requests body. Every field naming a
// destination comes from the Destination — never from the payload.
type mergeRequestRequest struct {
	SourceBranch    string `json:"source_branch"`
	TargetBranch    string `json:"target_branch"`
	TargetProjectID int    `json:"target_project_id"`
	Title           string `json:"title"`
}

// postMergeRequest opens the cross-project merge request: from the fork's
// branch to the canonical parent named by the Destination. On drupal.org
// every merge request runs fork -> parent, so target_project_id necessarily
// names a canonical project; see Destination.ParentID for why that is a
// proposal rather than a write to it.
//
// The title is the source branch, which is host-derived, stable across
// re-runs, and drupal.org's own convention for an issue branch. Deriving it
// from the payload would make a merge request's title depend on guest text
// and would change between a first publish and a resumed one.
func (p *Publisher) postMergeRequest(ctx context.Context, dest Destination, token string) (*MergeRequest, error) {
	body := mergeRequestRequest{
		SourceBranch:    dest.Branch,
		TargetBranch:    dest.ParentBranch,
		TargetProjectID: dest.ParentID,
		Title:           dest.Branch,
	}
	var mr MergeRequest
	if err := p.post(ctx, encodedProjectPath(dest.ForkPath)+"/merge_requests", token, body, &mr); err != nil {
		return nil, err
	}
	return &mr, nil
}

// post issues one authenticated POST of a JSON body and decodes the JSON
// response into out. The credential travels in the PRIVATE-TOKEN header and
// nowhere else: never in the path, never in the query, never in the body,
// and never in an error — every error below names only the method and path.
func (p *Publisher) post(ctx context.Context, path, token string, body, out any) error {
	full, err := p.client.endpoint(http.MethodPost, path, nil)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("drupalorg: encoding POST %s body: %w", path, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, full, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("drupalorg: building POST %s request: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("PRIVATE-TOKEN", token)

	return send(p.authHTTP, req, http.MethodPost, path, out)
}

// asForkMoved labels the one API refusal that is a "re-derive and retry"
// outcome rather than a fault, wrapping it so both ErrForkMoved and the
// original *APIError remain visible to errors.Is and errors.As.
//
// It is applied ONLY to the content API's commit replay (see postCommit),
// never to every authenticated POST: a 409 from the merge-request endpoint
// is an already-open merge request, not a moved fork.
func asForkMoved(err error) error {
	var ae *APIError
	if !errors.As(err, &ae) {
		return err
	}
	stale := ae.Status == http.StatusConflict ||
		(ae.Status == http.StatusBadRequest && strings.Contains(strings.ToLower(ae.Message), staleFileMarker))
	if !stale {
		return err
	}
	return fmt.Errorf("%w: %w", ErrForkMoved, err)
}

// subjectOf returns a commit message's first line, which is how a report
// names a commit without reproducing it.
func subjectOf(message string) string {
	subject, _, _ := strings.Cut(message, "\n")
	return strings.TrimSpace(subject)
}

// commitLabel names one commit of a change set the same way everywhere it
// is mentioned — a refusal, a size rejection, a mid-replay failure — so a
// developer reading two different errors about the same publish sees the
// same commit named the same way. index is 0-based.
func commitLabel(index, total int, subject string) string {
	return fmt.Sprintf("commit %d of %d (%q)", index+1, total, subject)
}
