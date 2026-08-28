package drupalorg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// ---------------------------------------------------------------------------
// Fixture
//
// One httptest handler models enough of a GitLab issue fork — branch state, a
// commit history, a file tree, and the merge requests its parent holds — to
// serve the anonymous reads, accept authenticated commits, and be made to
// fail a chosen call. The plan's implementation notes call for exactly this:
// one fixture covering the replay, the failure, and the resume rather than
// three separate scaffolds.
//
// The fixture deliberately enforces the GitLab rules this task must respect,
// so a wrong request FAILS here rather than merely tripping an assertion:
// start_branch against an existing branch is refused, a create over an
// existing file is refused, and an update whose last_commit_id is not the
// file's actual last commit is refused with GitLab's own wording. That makes
// the tests prove the requests are right, not just that they were sent.
// ---------------------------------------------------------------------------

// fixtureSHA renders n as a 40-character commit id, so fixture ids have the
// shape of the SHAs a real API returns while staying nameable by ordinal.
func fixtureSHA(n int) string { return fmt.Sprintf("%040d", n) }

type fixtureCommit struct {
	id          string
	message     string
	authorName  string
	authorEmail string
	paths       []string
}

type fixtureRequest struct {
	method string
	path   string
	query  url.Values
	token  string
}

// fixtureCommitRequest is the POST /repository/commits body, typed.
type fixtureCommitRequest struct {
	Branch        string `json:"branch"`
	StartBranch   string `json:"start_branch"`
	CommitMessage string `json:"commit_message"`
	AuthorName    string `json:"author_name"`
	AuthorEmail   string `json:"author_email"`
	Actions       []struct {
		Action       string `json:"action"`
		FilePath     string `json:"file_path"`
		PreviousPath string `json:"previous_path"`
		Content      string `json:"content"`
		Encoding     string `json:"encoding"`
		LastCommitID string `json:"last_commit_id"`
	} `json:"actions"`
}

type forkFixture struct {
	mu sync.Mutex

	forkPath     string
	forkID       int
	forkDefault  string
	parentPath   string
	parentID     int
	parentBranch string

	branch       string
	branchExists bool
	history      []fixtureCommit // oldest first
	files        map[string]bool
	openMRs      []MergeRequest

	// failCommitPost, when non-nil, is consulted with the 1-based ordinal of
	// every POST to the commits endpoint before it is applied. Writing a
	// response and returning true makes that call fail instead of creating a
	// commit — which is how the interior-failure test breaks call 2 of 3.
	failCommitPost func(n int, w http.ResponseWriter) bool

	commitPosts    int
	requests       []fixtureRequest
	commitBodies   []map[string]any
	commitRequests []fixtureCommitRequest
	mrBodies       []map[string]any
}

func newForkFixture() *forkFixture {
	return &forkFixture{
		forkPath:     "issue/drupal-3181657",
		forkID:       241450,
		forkDefault:  "11.x",
		parentPath:   "project/drupal",
		parentID:     106348,
		parentBranch: "11.x",
		branch:       "drupal-3181657",
		files:        map[string]bool{},
	}
}

// destination builds the Destination under test through the real
// NewDestination, so the fork path, branch, and parent identity a test
// publishes to are the ones task 4 would actually produce.
func (f *forkFixture) destination(t *testing.T) Destination {
	t.Helper()
	dest, err := NewDestination("drupal", 3181657, f.forkPath, &ProjectInfo{
		ID:            f.forkID,
		DefaultBranch: f.forkDefault,
		ForkedFromProject: &ForkedFromProject{
			ID:                f.parentID,
			PathWithNamespace: f.parentPath,
			DefaultBranch:     f.parentBranch,
		},
	}, false)
	if err != nil {
		t.Fatalf("NewDestination: %v", err)
	}
	if dest.Branch != f.branch {
		t.Fatalf("fixture branch %q does not match NewDestination's %q", f.branch, dest.Branch)
	}
	return dest
}

// client starts the fixture's server and returns a Client pointed at it,
// through the package's existing newTestClient helper. No test in this file
// ever contacts git.drupalcode.org.
func (f *forkFixture) client(t *testing.T) *Client {
	t.Helper()
	return newTestClient(t, f.ServeHTTP)
}

// publisher starts the fixture's server and returns a Publisher pointed at it.
func (f *forkFixture) publisher(t *testing.T) *Publisher {
	t.Helper()
	return NewPublisher(f.client(t))
}

func (f *forkFixture) snapshotRequests() []fixtureRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func (f *forkFixture) snapshotCommitRequests() ([]fixtureCommitRequest, []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.commitRequests), slices.Clone(f.commitBodies)
}

func (f *forkFixture) snapshotMRBodies() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.mrBodies)
}

// setFailCommitPost installs (or clears) the commit-POST failure hook under
// the fixture's own lock, so a test can flip it between publish runs without
// racing the handler goroutine.
func (f *forkFixture) setFailCommitPost(fn func(n int, w http.ResponseWriter) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failCommitPost = fn
}

// historySubjects is the branch's commit history, oldest first, by subject —
// the shape an assertion about duplicates reads most clearly.
func (f *forkFixture) historySubjects() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.history))
	for _, c := range f.history {
		out = append(out, strings.SplitN(c.message, "\n", 2)[0])
	}
	return out
}

func (f *forkFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusInternalServerError)
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.requests = append(f.requests, fixtureRequest{
		method: r.Method,
		path:   r.URL.EscapedPath(),
		query:  r.URL.Query(),
		token:  r.Header.Get("PRIVATE-TOKEN"),
	})

	segs := strings.Split(strings.Trim(r.URL.EscapedPath(), "/"), "/")
	if len(segs) < 2 || segs[0] != "projects" {
		f.fail(w, http.StatusNotFound, "404 Not Found")
		return
	}
	project, err := url.PathUnescape(segs[1])
	if err != nil {
		f.fail(w, http.StatusBadRequest, "bad project path")
		return
	}
	rest := segs[2:]

	isRepo := func(tail ...string) bool {
		return len(rest) >= 1 && rest[0] == "repository" && slices.Equal(rest[1:], tail)
	}

	switch {
	case r.Method == http.MethodGet && len(rest) == 0:
		f.serveProject(w, project)
	case r.Method == http.MethodGet && len(rest) == 3 && rest[0] == "repository" && rest[1] == "branches":
		name, _ := url.PathUnescape(rest[2])
		f.serveBranch(w, project, name)
	case r.Method == http.MethodGet && isRepo("commits"):
		f.serveCommits(w, project, r.URL.Query())
	case r.Method == http.MethodPost && isRepo("commits"):
		f.createCommit(w, r, project, body)
	case r.Method == http.MethodGet && len(rest) == 1 && rest[0] == "merge_requests":
		f.serveMergeRequests(w, project, r.URL.Query())
	case r.Method == http.MethodPost && len(rest) == 1 && rest[0] == "merge_requests":
		f.createMergeRequest(w, r, project, body)
	default:
		f.fail(w, http.StatusNotFound, "404 Not Found")
	}
}

func (f *forkFixture) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *forkFixture) fail(w http.ResponseWriter, status int, message string) {
	f.writeJSON(w, status, map[string]string{"message": message})
}

func (f *forkFixture) serveProject(w http.ResponseWriter, project string) {
	switch project {
	case f.forkPath:
		f.writeJSON(w, http.StatusOK, map[string]any{
			"id":             f.forkID,
			"default_branch": f.forkDefault,
			"forked_from_project": map[string]any{
				"id":                  f.parentID,
				"path_with_namespace": f.parentPath,
				"default_branch":      f.parentBranch,
			},
		})
	case f.parentPath:
		f.writeJSON(w, http.StatusOK, map[string]any{"id": f.parentID, "default_branch": f.parentBranch})
	default:
		f.fail(w, http.StatusNotFound, "404 Project Not Found")
	}
}

func (f *forkFixture) serveBranch(w http.ResponseWriter, project, name string) {
	if project != f.forkPath || name != f.branch || !f.branchExists {
		f.fail(w, http.StatusNotFound, "404 Branch Not Found")
		return
	}
	f.writeJSON(w, http.StatusOK, map[string]any{"name": name})
}

// serveCommits answers GET /repository/commits, newest first, honouring
// ref_name, the optional path filter, per_page and page — the shape the
// resumption read and the last_commit_id seed lookups both depend on.
func (f *forkFixture) serveCommits(w http.ResponseWriter, project string, q url.Values) {
	if project != f.forkPath {
		f.fail(w, http.StatusNotFound, "404 Project Not Found")
		return
	}
	if !f.branchExists || q.Get("ref_name") != f.branch {
		f.fail(w, http.StatusNotFound, "404 Reference Not Found")
		return
	}
	path := q.Get("path")
	out := []map[string]any{}
	for i := len(f.history) - 1; i >= 0; i-- {
		c := f.history[i]
		if path != "" && !slices.Contains(c.paths, path) {
			continue
		}
		out = append(out, map[string]any{
			"id":       c.id,
			"short_id": c.id[:8],
			"title":    strings.SplitN(c.message, "\n", 2)[0],
			// A real forge stores a commit message with a trailing newline
			// even when the caller sent none, so the fixture returns one:
			// resumption identity must not be defeated by that.
			"message":      c.message + "\n",
			"author_name":  c.authorName,
			"author_email": c.authorEmail,
		})
	}

	perPage := atoiOr(q.Get("per_page"), 20)
	page := atoiOr(q.Get("page"), 1)
	start := min((page-1)*perPage, len(out))
	end := min(start+perPage, len(out))
	f.writeJSON(w, http.StatusOK, out[start:end])
}

func atoiOr(s string, fallback int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// lastCommitTouching is the fixture's answer to "what is this file's last
// known commit id" — the value GitLab compares an update action's
// last_commit_id against.
func (f *forkFixture) lastCommitTouching(path string) string {
	for i := len(f.history) - 1; i >= 0; i-- {
		if slices.Contains(f.history[i].paths, path) {
			return f.history[i].id
		}
	}
	return ""
}

func (f *forkFixture) createCommit(w http.ResponseWriter, r *http.Request, project string, body []byte) {
	if project != f.forkPath {
		f.fail(w, http.StatusNotFound, "404 Project Not Found")
		return
	}
	if r.Header.Get("PRIVATE-TOKEN") == "" {
		f.fail(w, http.StatusUnauthorized, "401 Unauthorized")
		return
	}

	var raw map[string]any
	var req fixtureCommitRequest
	if json.Unmarshal(body, &raw) != nil || json.Unmarshal(body, &req) != nil {
		f.fail(w, http.StatusBadRequest, "malformed json body")
		return
	}
	f.commitBodies = append(f.commitBodies, raw)
	f.commitRequests = append(f.commitRequests, req)

	f.commitPosts++
	if f.failCommitPost != nil && f.failCommitPost(f.commitPosts, w) {
		return
	}

	if req.Branch != f.branch {
		f.fail(w, http.StatusBadRequest, "404 Branch Not Found")
		return
	}
	switch {
	case req.StartBranch != "" && f.branchExists:
		f.fail(w, http.StatusBadRequest, fmt.Sprintf("A branch called '%s' already exists.", f.branch))
		return
	case req.StartBranch == "" && !f.branchExists:
		f.fail(w, http.StatusBadRequest, "You can only create or edit files when you are on a branch")
		return
	case req.StartBranch != "" && req.StartBranch != f.forkDefault:
		f.fail(w, http.StatusBadRequest, fmt.Sprintf("Invalid reference name: %s", req.StartBranch))
		return
	}
	if strings.TrimSpace(req.CommitMessage) == "" {
		f.fail(w, http.StatusBadRequest, "commit_message is missing")
		return
	}
	if len(req.Actions) == 0 {
		f.fail(w, http.StatusBadRequest, "Actions are missing")
		return
	}

	// Applied to a clone so a rejected action leaves the tree untouched.
	files := maps.Clone(f.files)
	var touched []string
	for _, a := range req.Actions {
		switch a.Action {
		case "create":
			if files[a.FilePath] {
				f.fail(w, http.StatusBadRequest, fmt.Sprintf("A file with this name already exists: %s", a.FilePath))
				return
			}
			files[a.FilePath] = true
			touched = append(touched, a.FilePath)
		case "update":
			if !files[a.FilePath] {
				f.fail(w, http.StatusBadRequest, fmt.Sprintf("A file with this name doesn't exist: %s", a.FilePath))
				return
			}
			if want := f.lastCommitTouching(a.FilePath); a.LastCommitID != want {
				f.fail(w, http.StatusBadRequest, fmt.Sprintf("You are attempting to update a file %s that has changed since you started editing it.", a.FilePath))
				return
			}
			touched = append(touched, a.FilePath)
		case "delete":
			if !files[a.FilePath] {
				f.fail(w, http.StatusBadRequest, fmt.Sprintf("A file with this name doesn't exist: %s", a.FilePath))
				return
			}
			delete(files, a.FilePath)
			touched = append(touched, a.FilePath)
		case "move":
			if !files[a.PreviousPath] {
				f.fail(w, http.StatusBadRequest, fmt.Sprintf("A file with this name doesn't exist: %s", a.PreviousPath))
				return
			}
			if files[a.FilePath] {
				f.fail(w, http.StatusBadRequest, fmt.Sprintf("A file with this name already exists: %s", a.FilePath))
				return
			}
			delete(files, a.PreviousPath)
			files[a.FilePath] = true
			touched = append(touched, a.PreviousPath, a.FilePath)
		default:
			f.fail(w, http.StatusBadRequest, fmt.Sprintf("Unknown action %q", a.Action))
			return
		}
	}

	f.files = files
	id := fixtureSHA(len(f.history) + 1)
	f.history = append(f.history, fixtureCommit{
		id:          id,
		message:     req.CommitMessage,
		authorName:  req.AuthorName,
		authorEmail: req.AuthorEmail,
		paths:       touched,
	})
	f.branchExists = true
	f.writeJSON(w, http.StatusCreated, map[string]any{
		"id":       id,
		"short_id": id[:8],
		"title":    strings.SplitN(req.CommitMessage, "\n", 2)[0],
	})
}

// serveMergeRequests models drupal.org's cross-project reality: an issue
// fork holds no merge requests of its own, so only the parent's endpoint
// ever answers with anything.
func (f *forkFixture) serveMergeRequests(w http.ResponseWriter, project string, q url.Values) {
	out := []MergeRequest{}
	if project == f.parentPath {
		for _, mr := range f.openMRs {
			if state := q.Get("state"); state != "" && state != mr.State {
				continue
			}
			if src := q.Get("source_branch"); src != "" && mr.SourceBranch != src {
				continue
			}
			out = append(out, mr)
		}
	}
	f.writeJSON(w, http.StatusOK, out)
}

func (f *forkFixture) createMergeRequest(w http.ResponseWriter, r *http.Request, project string, body []byte) {
	if project != f.forkPath {
		f.fail(w, http.StatusNotFound, "404 Project Not Found")
		return
	}
	if r.Header.Get("PRIVATE-TOKEN") == "" {
		f.fail(w, http.StatusUnauthorized, "401 Unauthorized")
		return
	}
	var raw map[string]any
	var req struct {
		SourceBranch    string `json:"source_branch"`
		TargetBranch    string `json:"target_branch"`
		TargetProjectID int    `json:"target_project_id"`
		Title           string `json:"title"`
	}
	if json.Unmarshal(body, &raw) != nil || json.Unmarshal(body, &req) != nil {
		f.fail(w, http.StatusBadRequest, "malformed json body")
		return
	}
	f.mrBodies = append(f.mrBodies, raw)

	if req.Title == "" {
		f.fail(w, http.StatusBadRequest, "title is missing")
		return
	}
	if req.SourceBranch == "" || req.TargetBranch == "" || req.TargetProjectID == 0 {
		f.fail(w, http.StatusBadRequest, "source_branch, target_branch and target_project_id are required")
		return
	}
	mr := MergeRequest{
		IID:             len(f.openMRs) + 1,
		WebURL:          fmt.Sprintf("https://git.drupalcode.org/%s/-/merge_requests/%d", f.parentPath, len(f.openMRs)+1),
		SourceProjectID: f.forkID,
		TargetProjectID: req.TargetProjectID,
		SourceBranch:    req.SourceBranch,
		TargetBranch:    req.TargetBranch,
		State:           "opened",
	}
	f.openMRs = append(f.openMRs, mr)
	f.writeJSON(w, http.StatusCreated, mr)
}

// ---------------------------------------------------------------------------
// Assertion helpers
// ---------------------------------------------------------------------------

// assertNoTokenLeak asserts the token never reached a request line — not in
// the path, not in the query — and never appears in err's text. The token is
// carried in a PRIVATE-TOKEN header and nowhere else.
func assertNoTokenLeak(t *testing.T, fx *forkFixture, token string, err error) {
	t.Helper()
	for _, req := range fx.snapshotRequests() {
		if strings.Contains(req.path, token) || strings.Contains(req.query.Encode(), token) {
			t.Errorf("token leaked into request line: %s %s?%s", req.method, req.path, req.query.Encode())
		}
	}
	if err != nil && strings.Contains(err.Error(), token) {
		t.Errorf("token leaked into error text: %v", err)
	}
}

func countPosts(reqs []fixtureRequest, suffix string) int {
	n := 0
	for _, r := range reqs {
		if r.method == http.MethodPost && strings.HasSuffix(r.path, suffix) {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

const testToken = "glpat-fixture-token"

// threeCommitChangeSet is the change set most tests replay: it exercises all
// four action kinds, both encodings, and a commit whose update targets a
// file the change set did not itself create.
func threeCommitChangeSet() ChangeSet {
	binaryEnc, binaryContent := EncodeContent([]byte{0x00, 0xff, 0xfe})
	return ChangeSet{Commits: []Commit{
		{
			Message:     "Add Foo",
			AuthorName:  "Alice Contributor",
			AuthorEmail: "alice@example.com",
			Actions: []FileAction{
				{Kind: ActionCreate, Path: "src/Foo.php", Content: "<?php\n", Encoding: EncodingText},
				{Kind: ActionCreate, Path: "assets/logo.bin", Content: binaryContent, Encoding: binaryEnc},
			},
		},
		{
			Message:     "Update README\n\nWith an explanatory body.",
			AuthorName:  "Bob Reviewer",
			AuthorEmail: "bob@example.com",
			Actions: []FileAction{
				{Kind: ActionUpdate, Path: "README.md", Content: "hello\n", Encoding: EncodingText},
				{Kind: ActionMove, Path: "src/New.php", PreviousPath: "src/Old.php"},
			},
		},
		{
			Message:     "Drop Foo",
			AuthorName:  "Alice Contributor",
			AuthorEmail: "alice@example.com",
			Actions: []FileAction{
				{Kind: ActionDelete, Path: "src/Foo.php"},
			},
		},
	}}
}

// seededFixture is a fork whose issue branch already exists (the common
// case: drupal.org creates it alongside the fork) carrying one upstream
// commit and the files the change set expects to find.
func seededFixture() *forkFixture {
	fx := newForkFixture()
	fx.branchExists = true
	fx.files = map[string]bool{"README.md": true, "src/Old.php": true}
	fx.history = []fixtureCommit{{
		id:          fixtureSHA(1),
		message:     "Initial issue branch commit",
		authorName:  "Upstream",
		authorEmail: "upstream@example.com",
		paths:       []string{"README.md", "src/Old.php"},
	}}
	return fx
}

// TestPublish_ExistingBranchReplaysEveryCommitThenOpensMergeRequest is the
// critical path: an issue branch that already exists, three commits replayed
// one call each, then a cross-project merge request. It asserts the whole
// request contract at once — one call per commit, start_branch absent,
// author and message carried per commit, actions mapped by kind, the update
// action's last_commit_id, the credential on writes and only on writes, and
// the merge request's target being the canonical parent from the
// Destination.
func TestPublish_ExistingBranchReplaysEveryCommitThenOpensMergeRequest(t *testing.T) {
	writeTokenFile(t, testToken+"\n", 0o600)

	fx := seededFixture()
	dest := fx.destination(t)
	p := fx.publisher(t)

	res, err := p.Publish(context.Background(), dest, threeCommitChangeSet())
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	reqs := fx.snapshotRequests()
	if got := countPosts(reqs, "/repository/commits"); got != 3 {
		t.Fatalf("commit POSTs = %d, want 3 (one call per commit)", got)
	}
	if got := countPosts(reqs, "/merge_requests"); got != 1 {
		t.Errorf("merge request POSTs = %d, want 1", got)
	}

	typed, raw := fx.snapshotCommitRequests()
	wantSubjects := []string{"Add Foo", "Update README", "Drop Foo"}
	for i, req := range typed {
		if _, present := raw[i]["start_branch"]; present {
			t.Errorf("commit POST %d carries start_branch %v; the branch already exists so it must be absent", i+1, raw[i]["start_branch"])
		}
		if req.Branch != dest.Branch {
			t.Errorf("commit POST %d branch = %q, want %q", i+1, req.Branch, dest.Branch)
		}
		if subject := strings.SplitN(req.CommitMessage, "\n", 2)[0]; subject != wantSubjects[i] {
			t.Errorf("commit POST %d subject = %q, want %q", i+1, subject, wantSubjects[i])
		}
	}
	if typed[1].CommitMessage != "Update README\n\nWith an explanatory body." {
		t.Errorf("commit POST 2 message = %q, want the full multi-line message", typed[1].CommitMessage)
	}
	if typed[1].AuthorName != "Bob Reviewer" || typed[1].AuthorEmail != "bob@example.com" {
		t.Errorf("commit POST 2 author = %q <%q>, want the payload's own author", typed[1].AuthorName, typed[1].AuthorEmail)
	}

	// Commit 1's two creates, with the right encodings.
	if len(typed[0].Actions) != 2 {
		t.Fatalf("commit POST 1 actions = %d, want 2", len(typed[0].Actions))
	}
	if a := typed[0].Actions[0]; a.Action != "create" || a.FilePath != "src/Foo.php" || a.Content != "<?php\n" || a.Encoding != "text" {
		t.Errorf("commit POST 1 action 0 = %+v, want a text create of src/Foo.php", a)
	}
	if a := typed[0].Actions[1]; a.Action != "create" || a.Encoding != "base64" {
		t.Errorf("commit POST 1 action 1 = %+v, want a base64 create", a)
	}

	// Commit 2's update must carry the file's real last commit — the seeded
	// upstream commit, not the branch tip guessed at.
	if a := typed[1].Actions[0]; a.Action != "update" || a.LastCommitID != fixtureSHA(1) {
		t.Errorf("commit POST 2 action 0 = %+v, want an update carrying last_commit_id %s", a, fixtureSHA(1))
	}
	if a := typed[1].Actions[1]; a.Action != "move" || a.PreviousPath != "src/Old.php" || a.FilePath != "src/New.php" {
		t.Errorf("commit POST 2 action 1 = %+v, want a move src/Old.php -> src/New.php", a)
	}
	if a := typed[2].Actions[0]; a.Action != "delete" || a.FilePath != "src/Foo.php" || a.Content != "" {
		t.Errorf("commit POST 3 action 0 = %+v, want a contentless delete", a)
	}

	// The credential goes on writes, and only on writes.
	for _, req := range reqs {
		switch req.method {
		case http.MethodGet:
			if req.token != "" {
				t.Errorf("anonymous read %s %s carried a PRIVATE-TOKEN header", req.method, req.path)
			}
		case http.MethodPost:
			if req.token != testToken {
				t.Errorf("write %s %s carried PRIVATE-TOKEN %q, want the loaded token", req.method, req.path, req.token)
			}
		}
	}
	assertNoTokenLeak(t, fx, testToken, nil)

	// The merge request is cross-project, and both of its targets come from
	// the Destination rather than the payload.
	mrBodies := fx.snapshotMRBodies()
	if len(mrBodies) != 1 {
		t.Fatalf("merge request bodies = %d, want 1", len(mrBodies))
	}
	if got, want := mrBodies[0]["target_project_id"], float64(dest.ParentID); got != want {
		t.Errorf("merge request target_project_id = %v, want %v", got, want)
	}
	if got, want := mrBodies[0]["target_branch"], dest.ParentBranch; got != want {
		t.Errorf("merge request target_branch = %v, want %v", got, want)
	}
	if got, want := mrBodies[0]["source_branch"], dest.Branch; got != want {
		t.Errorf("merge request source_branch = %v, want %v", got, want)
	}

	// The Result names every commit, in order, with the SHA the API returned.
	if len(res.Commits) != 3 {
		t.Fatalf("Result.Commits = %d, want 3", len(res.Commits))
	}
	for i, cr := range res.Commits {
		if cr.Status != CommitLanded {
			t.Errorf("Result.Commits[%d].Status = %q, want %q", i, cr.Status, CommitLanded)
		}
		if want := fixtureSHA(i + 2); cr.SHA != want {
			t.Errorf("Result.Commits[%d].SHA = %q, want %q", i, cr.SHA, want)
		}
	}
	if res.FirstFailure() != nil {
		t.Errorf("Result.FirstFailure() = %+v, want nil", res.FirstFailure())
	}
	if !res.MergeRequestOpened || res.MergeRequest == nil {
		t.Fatalf("Result merge request = %+v, opened = %v; want a newly opened merge request", res.MergeRequest, res.MergeRequestOpened)
	}
	if res.MergeRequest.TargetProjectID != dest.ParentID {
		t.Errorf("Result.MergeRequest.TargetProjectID = %d, want %d", res.MergeRequest.TargetProjectID, dest.ParentID)
	}
}

// TestPublish_AbsentBranchIsCreatedFromTheForkDefaultOnFirstCallOnly covers
// the uncommon arm: an issue branch that genuinely does not exist yet.
// start_branch appears on the first call, naming the fork's own
// default_branch, and must not appear on the second — by then the first call
// has created the branch, and start_branch against an existing branch is an
// error. It also asserts the ordering the plan requires: branch existence is
// resolved anonymously BEFORE any authenticated call.
func TestPublish_AbsentBranchIsCreatedFromTheForkDefaultOnFirstCallOnly(t *testing.T) {
	writeTokenFile(t, testToken, 0o600)

	fx := newForkFixture()
	dest := fx.destination(t)
	p := fx.publisher(t)

	cs := ChangeSet{Commits: []Commit{
		{Message: "Add one", AuthorName: "Alice", AuthorEmail: "alice@example.com",
			Actions: []FileAction{{Kind: ActionCreate, Path: "one.txt", Content: "1\n", Encoding: EncodingText}}},
		{Message: "Add two", AuthorName: "Alice", AuthorEmail: "alice@example.com",
			Actions: []FileAction{{Kind: ActionCreate, Path: "two.txt", Content: "2\n", Encoding: EncodingText}}},
	}}

	if _, err := p.Publish(context.Background(), dest, cs); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	typed, raw := fx.snapshotCommitRequests()
	if len(typed) != 2 {
		t.Fatalf("commit POSTs = %d, want 2", len(typed))
	}
	if typed[0].StartBranch != fx.forkDefault {
		t.Errorf("commit POST 1 start_branch = %q, want the fork's own default branch %q", typed[0].StartBranch, fx.forkDefault)
	}
	if v, present := raw[1]["start_branch"]; present {
		t.Errorf("commit POST 2 carries start_branch %v; the first call created the branch so the second must omit it", v)
	}

	reqs := fx.snapshotRequests()
	branchProbe, firstWrite := -1, -1
	for i, r := range reqs {
		if branchProbe < 0 && r.method == http.MethodGet && strings.Contains(r.path, "/repository/branches/") {
			branchProbe = i
		}
		if firstWrite < 0 && r.method == http.MethodPost {
			firstWrite = i
		}
	}
	if branchProbe < 0 || firstWrite < 0 || branchProbe > firstWrite {
		t.Errorf("anonymous branch probe at request %d, first authenticated write at %d; existence must be resolved before any authenticated call", branchProbe, firstWrite)
	}
}

// TestPublish_InteriorFailureIsReportedThenResumed is the scenario the plan
// names as the reason partial failure is a first-class outcome: call 2 of 3
// fails with call 1 already public and unrevocable. It asserts the report,
// then re-runs the same publish against the fixture state that failure left
// behind and asserts exactly the remaining commits are sent — no duplicate
// of the commit that already landed. A third run, with the token file
// removed, asserts a fully-resumed publish is a no-op that needs no
// credential at all: the token is read at the moment of the first
// authenticated call, and there is none.
func TestPublish_InteriorFailureIsReportedThenResumed(t *testing.T) {
	tokenPath := writeTokenFile(t, testToken, 0o600)

	fx := seededFixture()
	dest := fx.destination(t)
	p := fx.publisher(t)
	cs := threeCommitChangeSet()

	// --- Run 1: the interior call fails. ---
	fx.setFailCommitPost(func(n int, w http.ResponseWriter) bool {
		if n != 2 {
			return false
		}
		fx.fail(w, http.StatusInternalServerError, "500 Internal Server Error")
		return true
	})

	res, err := p.Publish(context.Background(), dest, cs)
	if err == nil {
		t.Fatal("Publish: got nil error, want the interior failure reported")
	}
	assertNoTokenLeak(t, fx, testToken, err)

	if len(res.Commits) != 3 {
		t.Fatalf("Result.Commits = %+v, want one entry per change-set commit", res.Commits)
	}
	wantStatus := []CommitStatus{CommitLanded, CommitFailed, CommitNotAttempted}
	for i, cr := range res.Commits {
		if cr.Status != wantStatus[i] {
			t.Errorf("Result.Commits[%d] (%q) status = %q, want %q", i, cr.Subject, cr.Status, wantStatus[i])
		}
	}
	if res.Commits[0].SHA != fixtureSHA(2) {
		t.Errorf("landed commit SHA = %q, want the id the API returned (%s)", res.Commits[0].SHA, fixtureSHA(2))
	}
	failure := res.FirstFailure()
	if failure == nil || failure.Index != 1 {
		t.Fatalf("Result.FirstFailure() = %+v, want the second commit", failure)
	}
	var ae *APIError
	if !errors.As(failure.Err, &ae) || ae.Status != http.StatusInternalServerError {
		t.Errorf("first failure cause = %v, want the API's 500", failure.Err)
	}
	// The error must not read as a clean failure: it names the call, and it
	// says a commit is already public with no rollback.
	for _, want := range []string{"commit 2 of 3", "no rollback"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if res.MergeRequest != nil || res.MergeRequestOpened {
		t.Errorf("a merge request was touched after a failed replay: %+v", res.MergeRequest)
	}
	if got, want := fx.historySubjects(), []string{"Initial issue branch commit", "Add Foo"}; !slices.Equal(got, want) {
		t.Fatalf("branch history after the failed run = %v, want %v", got, want)
	}

	// --- Run 2: the same publish, resumed. ---
	fx.setFailCommitPost(nil)
	sentBefore, _ := fx.snapshotCommitRequests()

	res2, err := p.Publish(context.Background(), dest, cs)
	if err != nil {
		t.Fatalf("resumed Publish: %v", err)
	}

	typed, _ := fx.snapshotCommitRequests()
	resent := typed[len(sentBefore):]
	if len(resent) != 2 {
		t.Fatalf("resumed run sent %d commits, want exactly the 2 that had not landed", len(resent))
	}
	for i, want := range []string{"Update README", "Drop Foo"} {
		if got := strings.SplitN(resent[i].CommitMessage, "\n", 2)[0]; got != want {
			t.Errorf("resumed commit %d subject = %q, want %q", i+1, got, want)
		}
	}
	if res2.Commits[0].Status != CommitAlreadyPresent {
		t.Errorf("resumed Result.Commits[0].Status = %q, want %q", res2.Commits[0].Status, CommitAlreadyPresent)
	}
	if res2.Commits[0].SHA != fixtureSHA(2) {
		t.Errorf("already-present commit SHA = %q, want the id it landed under (%s)", res2.Commits[0].SHA, fixtureSHA(2))
	}
	for i, cr := range res2.Commits[1:] {
		if cr.Status != CommitLanded {
			t.Errorf("resumed Result.Commits[%d].Status = %q, want %q", i+1, cr.Status, CommitLanded)
		}
	}
	if !res2.MergeRequestOpened {
		t.Errorf("resumed run did not open the merge request the failed run never reached")
	}
	want := []string{"Initial issue branch commit", "Add Foo", "Update README", "Drop Foo"}
	if got := fx.historySubjects(); !slices.Equal(got, want) {
		t.Fatalf("branch history after resuming = %v, want %v (no duplicates)", got, want)
	}

	// --- Run 3: nothing left to do, and no credential on disk. ---
	if err := os.Remove(tokenPath); err != nil {
		t.Fatalf("remove token file: %v", err)
	}
	reqsBefore := len(fx.snapshotRequests())

	res3, err := p.Publish(context.Background(), dest, cs)
	if err != nil {
		t.Fatalf("fully-resumed Publish: %v", err)
	}
	for _, r := range fx.snapshotRequests()[reqsBefore:] {
		if r.method != http.MethodGet {
			t.Errorf("fully-resumed publish made a %s request to %s; it had nothing to write", r.method, r.path)
		}
	}
	for i, cr := range res3.Commits {
		if cr.Status != CommitAlreadyPresent {
			t.Errorf("fully-resumed Result.Commits[%d].Status = %q, want %q", i, cr.Status, CommitAlreadyPresent)
		}
	}
	if res3.MergeRequestOpened {
		t.Error("fully-resumed publish opened a second merge request")
	}
	if res3.MergeRequest == nil || res3.MergeRequest.IID != 1 {
		t.Errorf("fully-resumed Result.MergeRequest = %+v, want the merge request already open", res3.MergeRequest)
	}
}

// TestPublish_StaleFileIsReportedAsForkMoved covers the concurrency guard:
// last_commit_id is the only thing that detects the fork having moved
// underneath, and the refusal it produces is a "re-derive and retry" outcome
// rather than a generic failure. The request side of the guard — that the
// right last_commit_id is sent at all — is enforced by the fixture in the
// replay test above; this covers how the refusal is classified.
func TestPublish_StaleFileIsReportedAsForkMoved(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		message string
	}{
		{
			name:    "gitlab's wording on a 400",
			status:  http.StatusBadRequest,
			message: "You are attempting to update a file README.md that has changed since you started editing it.",
		},
		{
			name:    "a bare conflict",
			status:  http.StatusConflict,
			message: "409 Conflict",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeTokenFile(t, testToken, 0o600)

			fx := seededFixture()
			dest := fx.destination(t)
			p := fx.publisher(t)
			fx.setFailCommitPost(func(n int, w http.ResponseWriter) bool {
				fx.fail(w, tc.status, tc.message)
				return true
			})

			res, err := p.Publish(context.Background(), dest, threeCommitChangeSet())
			if err == nil {
				t.Fatal("Publish: got nil error, want ErrForkMoved")
			}
			if !errors.Is(err, ErrForkMoved) {
				t.Errorf("Publish error = %v, want errors.Is(err, ErrForkMoved)", err)
			}
			var ae *APIError
			if !errors.As(err, &ae) || ae.Status != tc.status {
				t.Errorf("Publish error = %v, want the underlying *APIError to stay visible", err)
			}
			failure := res.FirstFailure()
			if failure == nil || failure.Index != 0 {
				t.Fatalf("Result.FirstFailure() = %+v, want the first commit", failure)
			}
			if !errors.Is(failure.Err, ErrForkMoved) {
				t.Errorf("Result's recorded cause = %v, want it to carry ErrForkMoved too", failure.Err)
			}
			if got := fx.historySubjects(); len(got) != 1 {
				t.Errorf("branch history = %v, want the refused commit not to have landed", got)
			}
		})
	}
}

// TestPublish_CommitSizeIsCheckedBeforeSending covers both thresholds. The
// rejection arm asserts the strong property: an oversized commit means NO
// request is made at all, not a request that fails.
func TestPublish_CommitSizeIsCheckedBeforeSending(t *testing.T) {
	// Lowered rather than met: proving a 300 MB refusal by allocating 300 MB
	// would cost more than the bug it guards against.
	origReject, origLimit := commitRejectBytes, commitRateLimitBytes
	t.Cleanup(func() { commitRejectBytes, commitRateLimitBytes = origReject, origLimit })
	commitRejectBytes, commitRateLimitBytes = 64, 16

	changeSetOf := func(size int) ChangeSet {
		return ChangeSet{Commits: []Commit{{
			Message: "Add a big file", AuthorName: "Alice", AuthorEmail: "alice@example.com",
			Actions: []FileAction{{Kind: ActionCreate, Path: "big.bin", Content: strings.Repeat("x", size), Encoding: EncodingText}},
		}}}
	}

	t.Run("over the rejection threshold, nothing is sent", func(t *testing.T) {
		writeTokenFile(t, testToken, 0o600)
		fx := seededFixture()
		dest := fx.destination(t)
		p := fx.publisher(t)

		res, err := p.Publish(context.Background(), dest, changeSetOf(128))
		if err == nil {
			t.Fatal("Publish: got nil error, want the oversized commit refused")
		}
		if n := len(fx.snapshotRequests()); n != 0 {
			t.Errorf("%d requests were made; an oversized commit must be refused before anything is sent", n)
		}
		if len(res.Commits) != 0 {
			t.Errorf("Result.Commits = %+v, want nothing reported as attempted", res.Commits)
		}
		// The message must say the check is per call, since that is what
		// makes a large change set publishable as a replay.
		if !strings.Contains(err.Error(), "each commit is one request") {
			t.Errorf("error %q does not explain that the threshold is per call", err)
		}
	})

	t.Run("over the rate-limit threshold, it is reported and sent", func(t *testing.T) {
		writeTokenFile(t, testToken, 0o600)
		fx := seededFixture()
		dest := fx.destination(t)
		p := fx.publisher(t)

		res, err := p.Publish(context.Background(), dest, changeSetOf(32))
		if err != nil {
			t.Fatalf("Publish: %v", err)
		}
		if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "rate-limit") {
			t.Errorf("Result.Warnings = %v, want one rate-limit warning", res.Warnings)
		}
		if n := countPosts(fx.snapshotRequests(), "/repository/commits"); n != 1 {
			t.Errorf("commit POSTs = %d, want the commit to have been sent anyway", n)
		}
	})
}

// TestPublish_OpenMergeRequestIsNotDuplicated covers the common case on an
// issue that has been worked before: publishing more commits to a branch
// whose merge request is already open must not attempt to open a second one.
func TestPublish_OpenMergeRequestIsNotDuplicated(t *testing.T) {
	writeTokenFile(t, testToken, 0o600)

	fx := seededFixture()
	dest := fx.destination(t)
	fx.openMRs = []MergeRequest{{
		IID:             7,
		WebURL:          "https://git.drupalcode.org/project/drupal/-/merge_requests/7",
		SourceProjectID: fx.forkID,
		TargetProjectID: fx.parentID,
		SourceBranch:    dest.Branch,
		TargetBranch:    fx.parentBranch,
		State:           "opened",
	}}
	p := fx.publisher(t)

	res, err := p.Publish(context.Background(), dest, threeCommitChangeSet())
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if n := countPosts(fx.snapshotRequests(), "/merge_requests"); n != 0 {
		t.Errorf("merge request POSTs = %d, want 0 when one is already open", n)
	}
	if res.MergeRequestOpened {
		t.Error("Result.MergeRequestOpened = true, want false when the merge request already existed")
	}
	if res.MergeRequest == nil || res.MergeRequest.IID != 7 {
		t.Errorf("Result.MergeRequest = %+v, want the merge request already open", res.MergeRequest)
	}
	if n := countPosts(fx.snapshotRequests(), "/repository/commits"); n != 3 {
		t.Errorf("commit POSTs = %d, want the commits to have been replayed regardless", n)
	}
}

// TestPublish_EdgeRefusalDuringPublication asserts that drupal.org's
// allowlist blocking a write — an HTML page where GitLab JSON was expected —
// is reported as a refusal rather than as a generic API error or a
// misleading 404.
func TestPublish_EdgeRefusalDuringPublication(t *testing.T) {
	writeTokenFile(t, testToken, 0o600)

	fx := seededFixture()
	dest := fx.destination(t)
	p := fx.publisher(t)
	fx.setFailCommitPost(func(n int, w http.ResponseWriter) bool {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><title>Page not found</title></head><body>Not Found</body></html>`))
		return true
	})

	res, err := p.Publish(context.Background(), dest, threeCommitChangeSet())
	if err == nil {
		t.Fatal("Publish: got nil error, want ErrDrupalOrgRefused")
	}
	if !errors.Is(err, ErrDrupalOrgRefused) {
		t.Errorf("Publish error = %v, want errors.Is(err, ErrDrupalOrgRefused)", err)
	}
	var ae *APIError
	if errors.As(err, &ae) {
		t.Errorf("Publish error = %v, want NOT an *APIError: a blocked endpoint is not a GitLab 404", err)
	}
	if res.FirstFailure() == nil {
		t.Error("Result.FirstFailure() = nil, want the refused commit named")
	}
}

// TestPublish_TokenNeverEscapesItsHeader covers the two ways an account
// credential could escape: an error that quotes it, and a redirect that
// forwards it. Go strips only its own list of sensitive headers when
// following a cross-host redirect, and PRIVATE-TOKEN is not on that list, so
// a followed redirect would hand the token to whatever host it named.
func TestPublish_TokenNeverEscapesItsHeader(t *testing.T) {
	t.Run("an API refusal never quotes it", func(t *testing.T) {
		writeTokenFile(t, testToken, 0o600)

		fx := seededFixture()
		dest := fx.destination(t)
		p := fx.publisher(t)
		fx.setFailCommitPost(func(n int, w http.ResponseWriter) bool {
			fx.fail(w, http.StatusUnauthorized, "401 Unauthorized")
			return true
		})

		res, err := p.Publish(context.Background(), dest, threeCommitChangeSet())
		if err == nil {
			t.Fatal("Publish: got nil error, want the 401 reported")
		}
		assertNoTokenLeak(t, fx, testToken, err)
		if failure := res.FirstFailure(); failure != nil && strings.Contains(failure.Err.Error(), testToken) {
			t.Errorf("token leaked into the Result's recorded cause: %v", failure.Err)
		}
	})

	t.Run("a redirect is refused rather than followed", func(t *testing.T) {
		writeTokenFile(t, testToken, 0o600)

		var sinkHits atomic.Int64
		sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sinkHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"0000000000000000000000000000000000000099"}`))
		}))
		t.Cleanup(sink.Close)

		fx := seededFixture()
		dest := fx.destination(t)
		p := fx.publisher(t)
		fx.setFailCommitPost(func(n int, w http.ResponseWriter) bool {
			w.Header().Set("Location", sink.URL+"/projects/elsewhere/repository/commits")
			w.WriteHeader(http.StatusFound)
			return true
		})

		_, err := p.Publish(context.Background(), dest, threeCommitChangeSet())
		if err == nil {
			t.Fatal("Publish: got nil error, want the redirect reported rather than followed")
		}
		if n := sinkHits.Load(); n != 0 {
			t.Fatalf("the redirect target received %d authenticated request(s); it must receive none", n)
		}
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != http.StatusFound {
			t.Errorf("Publish error = %v, want the 302 surfaced as an API error", err)
		}
	})
}

// TestPublish_RefusesABadChangeSetBeforeWritingAnything covers the host-side
// checks that must all run before the first call: a publish that is going to
// be refused has to be refused while nothing is public.
func TestPublish_RefusesABadChangeSetBeforeWritingAnything(t *testing.T) {
	cases := []struct {
		name string
		cs   ChangeSet
	}{
		{name: "no commits", cs: ChangeSet{}},
		{name: "empty commit message", cs: ChangeSet{Commits: []Commit{{
			Message: "  ", AuthorName: "A", AuthorEmail: "a@example.com",
			Actions: []FileAction{{Kind: ActionCreate, Path: "a.txt", Content: "a", Encoding: EncodingText}},
		}}}},
		{name: "commit with no file actions", cs: ChangeSet{Commits: []Commit{{
			Message: "Empty", AuthorName: "A", AuthorEmail: "a@example.com",
		}}}},
		{name: "a later commit carries a traversal path", cs: ChangeSet{Commits: []Commit{
			{Message: "Fine", AuthorName: "A", AuthorEmail: "a@example.com",
				Actions: []FileAction{{Kind: ActionCreate, Path: "a.txt", Content: "a", Encoding: EncodingText}}},
			{Message: "Hostile", AuthorName: "A", AuthorEmail: "a@example.com",
				Actions: []FileAction{{Kind: ActionCreate, Path: "../../etc/passwd", Content: "x", Encoding: EncodingText}}},
		}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeTokenFile(t, testToken, 0o600)
			fx := seededFixture()
			dest := fx.destination(t)
			p := fx.publisher(t)

			if _, err := p.Publish(context.Background(), dest, tc.cs); err == nil {
				t.Fatal("Publish: got nil error, want the change set refused")
			}
			if n := len(fx.snapshotRequests()); n != 0 {
				t.Errorf("%d requests were made; the refusal must happen before anything is sent", n)
			}
		})
	}
}

// TestBranchCommits_PagesWithoutRereading covers the resumption read's
// pagination, which no publish test above reaches: every change set in them
// is far shorter than one page.
//
// GitLab paginates this endpoint by OFFSET — page N starts at
// (N-1)*per_page — so a per_page that shrinks between pages walks the offset
// backwards and returns commits already collected. A history with duplicates
// in it cannot be matched by alreadyLandedCount, and the resume it defeats
// would replay already-public commits a second time, which is precisely what
// this package has no rollback for.
func TestBranchCommits_PagesWithoutRereading(t *testing.T) {
	const historyLen = 250

	fx := newForkFixture()
	fx.branchExists = true
	for i := 1; i <= historyLen; i++ {
		fx.history = append(fx.history, fixtureCommit{
			id:          fixtureSHA(i),
			message:     fmt.Sprintf("commit %d", i),
			authorName:  "Alice Contributor",
			authorEmail: "alice@example.com",
		})
	}
	c := fx.client(t)

	t.Run("a limit spanning several pages", func(t *testing.T) {
		got, err := c.branchCommits(context.Background(), fx.forkPath, fx.branch, 150)
		if err != nil {
			t.Fatalf("branchCommits: %v", err)
		}
		if len(got) != 150 {
			t.Fatalf("branchCommits returned %d commits, want exactly the 150 asked for", len(got))
		}
		seen := map[string]bool{}
		for i, rc := range got {
			if seen[rc.ID] {
				t.Fatalf("commit %s appears twice: the pages overlap", rc.ID)
			}
			seen[rc.ID] = true
			// Newest first, contiguous, starting at the branch tip.
			if want := fixtureSHA(historyLen - i); rc.ID != want {
				t.Fatalf("commit %d = %s, want %s (newest first, no gaps)", i, rc.ID, want)
			}
		}
	})

	t.Run("a limit longer than the history", func(t *testing.T) {
		got, err := c.branchCommits(context.Background(), fx.forkPath, fx.branch, historyLen+50)
		if err != nil {
			t.Fatalf("branchCommits: %v", err)
		}
		if len(got) != historyLen {
			t.Errorf("branchCommits returned %d commits, want the whole %d-commit history", len(got), historyLen)
		}
	})
}

// TestAlreadyLandedCount exercises the resumption-identity rule directly:
// which leading commits of a change set count as already on the branch.
// Every case here is one a wrong rule gets wrong by either duplicating a
// public commit or silently skipping work.
func TestAlreadyLandedCount(t *testing.T) {
	commit := func(message, name string) Commit {
		return Commit{Message: message, AuthorName: name, AuthorEmail: strings.ToLower(name) + "@example.com"}
	}
	remote := func(id, message, name string) remoteCommit {
		// Newline-terminated, as a forge stores and returns it.
		return remoteCommit{ID: id, Message: message + "\n", AuthorName: name, AuthorEmail: strings.ToLower(name) + "@example.com"}
	}

	changeSet := []Commit{commit("one", "alice"), commit("two", "bob"), commit("three", "alice")}

	cases := []struct {
		name    string
		history []remoteCommit // newest first
		commits []Commit
		want    int
	}{
		{name: "empty branch", commits: changeSet, want: 0},
		{
			name:    "nothing of ours has landed",
			history: []remoteCommit{remote("z", "upstream", "carol")},
			commits: changeSet,
			want:    0,
		},
		{
			name:    "an interrupted run landed the first commit",
			history: []remoteCommit{remote("b", "one", "alice"), remote("a", "upstream", "carol")},
			commits: changeSet,
			want:    1,
		},
		{
			name:    "everything landed, so a re-run is a no-op",
			history: []remoteCommit{remote("d", "three", "alice"), remote("c", "two", "bob"), remote("b", "one", "alice"), remote("a", "upstream", "carol")},
			commits: changeSet,
			want:    3,
		},
		{
			name:    "a repeated message resumes one at a time",
			history: []remoteCommit{remote("b", "fix typo", "alice"), remote("a", "upstream", "carol")},
			commits: []Commit{commit("fix typo", "alice"), commit("fix typo", "alice"), commit("done", "alice")},
			want:    1,
		},
		{
			name:    "both copies of a repeated message are recognised",
			history: []remoteCommit{remote("c", "fix typo", "alice"), remote("b", "fix typo", "alice"), remote("a", "upstream", "carol")},
			commits: []Commit{commit("fix typo", "alice"), commit("fix typo", "alice"), commit("done", "alice")},
			want:    2,
		},
		{
			name:    "the same message by a different author is a different commit",
			history: []remoteCommit{remote("b", "one", "mallory"), remote("a", "upstream", "carol")},
			commits: changeSet,
			want:    0,
		},
		{
			name:    "someone else pushed on top, so the run is not anchored",
			history: []remoteCommit{remote("c", "unrelated", "carol"), remote("b", "one", "alice")},
			commits: changeSet,
			want:    0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := alreadyLandedCount(tc.history, tc.commits); got != tc.want {
				t.Errorf("alreadyLandedCount = %d, want %d", got, tc.want)
			}
		})
	}
}
