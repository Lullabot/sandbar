package drupalorg

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestForkPath(t *testing.T) {
	cases := []struct {
		name    string
		module  string
		issue   int
		want    string
		wantErr bool
	}{
		{name: "simple", module: "drupal", issue: 3181657, want: "issue/drupal-3181657"},
		{name: "underscore module", module: "views_bulk_operations", issue: 42, want: "issue/views_bulk_operations-42"},
		{name: "digits-only module ok", module: "commerce2", issue: 1, want: "issue/commerce2-1"},
		{name: "empty module rejected", module: "", issue: 1, wantErr: true},
		{name: "slash in module rejected", module: "drupal/../etc", issue: 1, wantErr: true},
		{name: "uppercase module rejected", module: "Drupal", issue: 1, wantErr: true},
		{name: "shell metacharacters rejected", module: "drupal; rm -rf $(whoami)", issue: 1, wantErr: true},
		{name: "zero issue rejected", module: "drupal", issue: 0, wantErr: true},
		{name: "negative issue rejected", module: "drupal", issue: -5, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ForkPath(tc.module, tc.issue)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ForkPath(%q, %d) = %q, nil; want error", tc.module, tc.issue, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ForkPath(%q, %d) unexpected error: %v", tc.module, tc.issue, err)
			}
			if got != tc.want {
				t.Errorf("ForkPath(%q, %d) = %q, want %q", tc.module, tc.issue, got, tc.want)
			}
		})
	}
}

func TestModuleFromRemoteURL(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{
			name: "https",
			raw:  "https://git.drupalcode.org/project/drupal.git",
			want: "drupal",
		},
		{
			name: "https no dot git suffix",
			raw:  "https://git.drupalcode.org/project/webform",
			want: "webform",
		},
		{
			name: "scp-like ssh",
			raw:  "git@git.drupalcode.org:project/views_bulk_operations.git",
			want: "views_bulk_operations",
		},
		{
			name: "ssh url form",
			raw:  "ssh://git@git.drupalcode.org/project/commerce.git",
			want: "commerce",
		},
		{
			name:    "wrong host rejected",
			raw:     "https://github.com/project/drupal.git",
			wantErr: true,
		},
		{
			name:    "not a project path rejected",
			raw:     "https://git.drupalcode.org/issue/drupal-3181657.git",
			wantErr: true,
		},
		{
			name:    "empty rejected",
			raw:     "",
			wantErr: true,
		},
		{
			name:    "garbage rejected",
			raw:     "not a url at all",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ModuleFromRemoteURL(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ModuleFromRemoteURL(%q) = %q, nil; want error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ModuleFromRemoteURL(%q) unexpected error: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("ModuleFromRemoteURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestClient_Project_ForkedFromProject asserts that Project correctly parses
// a fork's forked_from_project, which is how a caller learns the canonical
// parent to publish/query merge requests against — this field is entirely
// host-derived (read from drupal.org) and never guest-supplied, which is
// what makes it safe to trust as a merge-request target later.
func TestClient_Project_ForkedFromProject(t *testing.T) {
	const body = `{
		"id": 999001,
		"default_branch": "3181657-add-a-widget",
		"forked_from_project": {
			"id": 59858,
			"path_with_namespace": "project/drupal",
			"default_branch": "11.x"
		}
	}`

	var gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	})

	p, err := c.Project(context.Background(), "issue/drupal-3181657")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	if gotPath != "/projects/issue%2Fdrupal-3181657" {
		t.Errorf("request path = %q, want %q", gotPath, "/projects/issue%2Fdrupal-3181657")
	}
	if p.ID != 999001 || p.DefaultBranch != "3181657-add-a-widget" {
		t.Errorf("Project = %+v, unexpected top-level fields", p)
	}
	if p.ForkedFromProject == nil {
		t.Fatal("ForkedFromProject = nil, want non-nil")
	}
	if p.ForkedFromProject.ID != 59858 ||
		p.ForkedFromProject.PathWithNamespace != "project/drupal" ||
		p.ForkedFromProject.DefaultBranch != "11.x" {
		t.Errorf("ForkedFromProject = %+v, unexpected fields", p.ForkedFromProject)
	}
}

// TestClient_BranchExists covers both outcomes of the existence check
// through GitLab's single-branch endpoint, and asserts the branch name is
// escaped into the request the same way a project path is.
func TestClient_BranchExists(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case "/projects/issue%2Fdrupal-3181657/repository/branches/3181657-add-a-widget":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"name":"3181657-add-a-widget"}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"404 Branch Not Found"}`))
		}
	})

	exists, err := c.BranchExists(context.Background(), "issue/drupal-3181657", "3181657-add-a-widget")
	if err != nil {
		t.Fatalf("BranchExists (present): %v", err)
	}
	if !exists {
		t.Error("BranchExists (present) = false, want true")
	}

	exists, err = c.BranchExists(context.Background(), "issue/drupal-3181657", "some-other-branch")
	if err != nil {
		t.Fatalf("BranchExists (absent): %v", err)
	}
	if exists {
		t.Error("BranchExists (absent) = true, want false")
	}
}

// TestClient_OpenMergeRequest_QueriesParentNotFork asserts the load-bearing
// behaviour: the lookup hits the CANONICAL PARENT project's merge_requests
// endpoint (never the fork's), and filters by source_project_id to find the
// one opened from this fork among the parent's merge requests, mirroring the
// plan's verified project/dubbot example (source_project_id 241450 ->
// target_project_id 106348).
func TestClient_OpenMergeRequest_QueriesParentNotFork(t *testing.T) {
	var gotPath, gotQuery string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotQuery = r.URL.RawQuery
		if gotPath == "/projects/issue%2Fdrupal-3181657/merge_requests" {
			t.Errorf("request hit the fork's merge_requests endpoint (%s); must query the parent instead", gotPath)
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[]`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"iid": 1, "web_url": "https://git.drupalcode.org/project/drupal/-/merge_requests/1", "source_project_id": 111, "target_project_id": 106348, "source_branch": "other-branch", "target_branch": "11.x", "state": "opened"},
			{"iid": 2, "web_url": "https://git.drupalcode.org/project/drupal/-/merge_requests/2", "source_project_id": 999001, "target_project_id": 106348, "source_branch": "3181657-add-a-widget", "target_branch": "11.x", "state": "opened"}
		]`))
	})

	mr, err := c.OpenMergeRequest(context.Background(), "project/drupal", 999001, "3181657-add-a-widget")
	if err != nil {
		t.Fatalf("OpenMergeRequest: %v", err)
	}

	const wantPath = "/projects/project%2Fdrupal/merge_requests"
	if gotPath != wantPath {
		t.Errorf("request path = %q, want %q", gotPath, wantPath)
	}
	if !strings.Contains(gotQuery, "state=opened") || !strings.Contains(gotQuery, "source_branch=3181657-add-a-widget") {
		t.Errorf("request query = %q, missing expected filters", gotQuery)
	}

	if mr == nil {
		t.Fatal("OpenMergeRequest = nil, want the matching MR")
	}
	if mr.IID != 2 || mr.SourceProjectID != 999001 || mr.TargetProjectID != 106348 {
		t.Errorf("OpenMergeRequest = %+v, unexpected fields", mr)
	}
}

// TestClient_OpenMergeRequest_None asserts a nil, non-error result when the
// parent has no open MR whose source_project_id matches — including when
// the parent's merge_requests endpoint returns entries from OTHER forks.
func TestClient_OpenMergeRequest_None(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"iid": 5, "source_project_id": 424242, "target_project_id": 106348, "source_branch": "3181657-add-a-widget", "state": "opened"}
		]`))
	})

	mr, err := c.OpenMergeRequest(context.Background(), "project/drupal", 999001, "3181657-add-a-widget")
	if err != nil {
		t.Fatalf("OpenMergeRequest: %v", err)
	}
	if mr != nil {
		t.Errorf("OpenMergeRequest = %+v, want nil (no matching source_project_id)", mr)
	}
}
