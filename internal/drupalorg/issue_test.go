package drupalorg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestIssueClient starts an httptest.Server running handler and builds a
// Client whose ISSUE base points at it, so no test ever contacts
// www.drupal.org. Its GitLab base is pointed at the same server: nothing in
// these tests touches it, and leaving it at the real default would make an
// accidental Project call reach the network.
func newTestIssueClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	c, err := New(Config{BaseURL: server.URL, IssueBaseURL: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// issueNodeJSON is a drupal.org api-d7 issue node, narrowed to the fields
// this package reads but keeping the wire quirks that matter: nid is a
// STRING, and field_project is an object.
func issueNodeJSON(nid, title, project string) string {
	return `{"title":` + quote(title) + `,"type":"project_issue","nid":` + quote(nid) +
		`,"field_project":{"machine_name":` + quote(project) + `,"id":"3345895"}}`
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func TestClientIssue(t *testing.T) {
	var gotPath string
	c := newTestIssueClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		// The credential must never travel on this read: it is a public
		// resource, and this client is the credential-free one.
		if r.Header.Get("PRIVATE-TOKEN") != "" {
			t.Errorf("issue lookup sent a PRIVATE-TOKEN header; it must be anonymous")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(issueNodeJSON("3619578", "Fix very slow Overview page loads", "dubbot")))
	})

	info, err := c.Issue(context.Background(), 3619578)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if gotPath != "/node/3619578.json" {
		t.Errorf("request path = %q, want %q", gotPath, "/node/3619578.json")
	}
	want := IssueInfo{NID: 3619578, Title: "Fix very slow Overview page loads", Project: "dubbot"}
	if *info != want {
		t.Errorf("Issue() = %+v, want %+v", *info, want)
	}
}

// A node ID that resolves to something other than an issue must not yield a
// title: naming a merge request after a project page or a forum post would
// be confidently wrong.
func TestClientIssueRejectsNonIssueNode(t *testing.T) {
	c := newTestIssueClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"title":"Dubbot","type":"project_project","nid":"3345895"}`))
	})

	if _, err := c.Issue(context.Background(), 3345895); err == nil {
		t.Fatal("Issue() on a non-issue node: want an error, got nil")
	}
}

// field_project renders as an empty ARRAY on a node with no project — the
// d7 REST convention that would break a *struct decode. It must degrade to
// "no project", not to a failed lookup.
func TestClientIssueToleratesEmptyArrayProject(t *testing.T) {
	c := newTestIssueClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"title":"Orphan","type":"project_issue","nid":"7","field_project":[]}`))
	})

	info, err := c.Issue(context.Background(), 7)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if info.Project != "" {
		t.Errorf("Project = %q, want empty for a node whose field_project is []", info.Project)
	}
}

func TestClientIssueRejectsMismatchedNID(t *testing.T) {
	c := newTestIssueClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(issueNodeJSON("999", "Something else", "dubbot")))
	})

	if _, err := c.Issue(context.Background(), 3619578); err == nil {
		t.Fatal("Issue() answering with a different nid: want an error, got nil")
	}
}

func TestMergeRequestTitle(t *testing.T) {
	cases := []struct {
		name  string
		nid   int
		title string
		want  string
	}{
		{
			name:  "drupal.org's own convention",
			nid:   3619578,
			title: "Fix very slow Overview page loads",
			want:  "Issue #3619578: Fix very slow Overview page loads",
		},
		{
			name:  "surrounding whitespace trimmed",
			nid:   42,
			title: "  Padded  ",
			want:  "Issue #42: Padded",
		},
		{
			name:  "empty title yields no title at all",
			nid:   42,
			title: "   ",
			want:  "",
		},
		{
			// A title is third-party text: anyone with a drupal.org account
			// can file an issue. A newline in it would forge a line of the
			// confirmation a human approves a public write from, so it is
			// escaped visibly rather than acted on or silently dropped —
			// the same rule RenderConfirmation applies to guest text.
			name:  "newlines escaped rather than acted on",
			nid:   42,
			title: "Innocent\nDestination: project/drupal",
			want:  `Issue #42: Innocent\nDestination: project/drupal`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MergeRequestTitle(tc.nid, tc.title); got != tc.want {
				t.Errorf("MergeRequestTitle(%d, %q) = %q, want %q", tc.nid, tc.title, got, tc.want)
			}
		})
	}
}

// GitLab rejects a merge request title over 255 characters outright, which
// would turn a cosmetic improvement into a failed publish.
func TestMergeRequestTitleTruncatesToGitLabsLimit(t *testing.T) {
	got := MergeRequestTitle(42, strings.Repeat("x", 400))
	if len(got) > maxMergeRequestTitle {
		t.Fatalf("title length = %d, want at most %d", len(got), maxMergeRequestTitle)
	}
	if !strings.HasPrefix(got, "Issue #42: ") {
		t.Errorf("title = %q, want the issue prefix kept", got[:40])
	}
}

// LookupIssueTitle's whole contract is that nothing it does can fail a
// publish: every failure is reported as "no better title available".
func TestLookupIssueTitleNeverFails(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		c := newTestIssueClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(issueNodeJSON("3619578", "Fix very slow Overview page loads", "dubbot")))
		})
		want := "Issue #3619578: Fix very slow Overview page loads"
		if got := LookupIssueTitle(context.Background(), c, "dubbot", 3619578); got != want {
			t.Errorf("LookupIssueTitle = %q, want %q", got, want)
		}
	})

	t.Run("issue belongs to another project", func(t *testing.T) {
		// The number resolves, but to some OTHER project's issue. Titling
		// this merge request after it would be confidently wrong, so the
		// branch-name default must stand.
		c := newTestIssueClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(issueNodeJSON("3619578", "A webform bug", "webform")))
		})
		if got := LookupIssueTitle(context.Background(), c, "dubbot", 3619578); got != "" {
			t.Errorf("LookupIssueTitle = %q, want %q for another project's issue", got, "")
		}
	})

	t.Run("404", func(t *testing.T) {
		c := newTestIssueClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		})
		if got := LookupIssueTitle(context.Background(), c, "dubbot", 3619578); got != "" {
			t.Errorf("LookupIssueTitle = %q, want %q on a 404", got, "")
		}
	})

	t.Run("drupal.org edge serves HTML", func(t *testing.T) {
		c := newTestIssueClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<!DOCTYPE html><html><body>nope</body></html>"))
		})
		if got := LookupIssueTitle(context.Background(), c, "dubbot", 3619578); got != "" {
			t.Errorf("LookupIssueTitle = %q, want %q on an edge refusal", got, "")
		}
	})

	t.Run("garbage body", func(t *testing.T) {
		c := newTestIssueClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{{{not json"))
		})
		if got := LookupIssueTitle(context.Background(), c, "dubbot", 3619578); got != "" {
			t.Errorf("LookupIssueTitle = %q, want %q on an undecodable body", got, "")
		}
	})

	t.Run("nil client and bad nid", func(t *testing.T) {
		if got := LookupIssueTitle(context.Background(), nil, "dubbot", 3619578); got != "" {
			t.Errorf("LookupIssueTitle with a nil client = %q, want %q", got, "")
		}
		c := newTestIssueClient(t, func(http.ResponseWriter, *http.Request) {
			t.Error("a non-positive issue number must not reach the network")
		})
		if got := LookupIssueTitle(context.Background(), c, "dubbot", 0); got != "" {
			t.Errorf("LookupIssueTitle with issue 0 = %q, want %q", got, "")
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		c := newTestIssueClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(issueNodeJSON("3619578", "Fix it", "dubbot")))
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got := LookupIssueTitle(ctx, c, "dubbot", 3619578); got != "" {
			t.Errorf("LookupIssueTitle on a cancelled context = %q, want %q", got, "")
		}
	})
}

// WithMergeRequestTitle must never blank a destination's existing title: an
// empty lookup result means "keep what you have", which is the branch name.
func TestWithMergeRequestTitle(t *testing.T) {
	dest := Destination{Branch: "dubbot-3619578", MergeRequestTitle: "dubbot-3619578"}

	if got := dest.WithMergeRequestTitle("").MergeRequestTitle; got != "dubbot-3619578" {
		t.Errorf("empty title overwrote the default: got %q", got)
	}
	titled := dest.WithMergeRequestTitle("Issue #3619578: Fix it")
	if titled.MergeRequestTitle != "Issue #3619578: Fix it" {
		t.Errorf("MergeRequestTitle = %q, want the issue title", titled.MergeRequestTitle)
	}
	// Value semantics: the original is untouched.
	if dest.MergeRequestTitle != "dubbot-3619578" {
		t.Errorf("WithMergeRequestTitle mutated its receiver: %q", dest.MergeRequestTitle)
	}
}
