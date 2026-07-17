package landgh

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestDefaultBranchArgs(t *testing.T) {
	got := defaultBranchArgs("acme/widgets")
	want := []string{"api", "repos/acme/widgets", "--jq", ".default_branch"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("defaultBranchArgs = %v, want %v", got, want)
	}
}

func TestHeadCommitMessageArgs(t *testing.T) {
	got := headCommitMessageArgs("acme/widgets", "feature-x")
	want := []string{"api", "repos/acme/widgets/commits/feature-x", "--jq", ".commit.message"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("headCommitMessageArgs = %v, want %v", got, want)
	}
}

func TestCreateDraftPRArgs(t *testing.T) {
	got := createDraftPRArgs("acme/widgets", "feature-x", "main", "My title", "My body")
	want := []string{
		"api", "--method", "POST", "repos/acme/widgets/pulls",
		"-f", "head=feature-x",
		"-f", "base=main",
		"-f", "title=My title",
		"-f", "body=My body",
		"-F", "draft=true",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("createDraftPRArgs = %v, want %v", got, want)
	}
}

// TestCreateDraftPRArgsInjectionSafety proves a branch name with shell
// metacharacters survives as one argv element in every step of the
// CreateDraftPR argv chain — the same graded criterion as PRState's.
func TestCreateDraftPRArgsInjectionSafety(t *testing.T) {
	evil := "feature; rm -rf / #`id`$(whoami)"

	hc := headCommitMessageArgs("acme/widgets", evil)
	if hc[1] != "repos/acme/widgets/commits/"+evil {
		t.Fatalf("headCommitMessageArgs did not carry the branch as inert text: %v", hc)
	}

	cp := createDraftPRArgs("acme/widgets", evil, "main", "t", "b")
	wantHeadArg := "head=" + evil
	found := 0
	for _, a := range cp {
		if a == wantHeadArg {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("createDraftPRArgs(%q) did not carry the evil branch as exactly one -f head=... element: %v", evil, cp)
	}
}

func TestSplitCommitMessage(t *testing.T) {
	cases := []struct {
		name      string
		msg       string
		wantTitle string
		wantBody  string
	}{
		{"title only", "Add feature X", "Add feature X", ""},
		{"title and body", "Add feature X\n\nLonger description here.\n", "Add feature X", "Longer description here."},
		{"trailing newline only", "Add feature X\n", "Add feature X", ""},
		{"empty", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, body := splitCommitMessage(tc.msg)
			if title != tc.wantTitle || body != tc.wantBody {
				t.Fatalf("splitCommitMessage(%q) = (%q, %q), want (%q, %q)", tc.msg, title, body, tc.wantTitle, tc.wantBody)
			}
		})
	}
}

func TestCreateDraftPR(t *testing.T) {
	ctx := context.Background()

	t.Run("happy path: resolves base, derives title/body, creates draft", func(t *testing.T) {
		run := &sequentialRunner{
			outputs: [][]byte{
				[]byte("main\n"), // default branch lookup
				[]byte("Add feature X\n\nLonger body.\n"),                                                              // head commit message lookup
				[]byte(`{"number":7,"html_url":"https://github.com/acme/widgets/pull/7","state":"open","draft":true}`), // create POST
			},
		}
		c := &Client{run: run}
		pr, err := c.CreateDraftPR(ctx, "acme/widgets", "feature-x")
		if err != nil {
			t.Fatalf("CreateDraftPR() error = %v", err)
		}
		want := &PR{Number: 7, URL: "https://github.com/acme/widgets/pull/7", State: "open", Draft: true}
		if *pr != *want {
			t.Fatalf("CreateDraftPR() = %+v, want %+v", pr, want)
		}

		if len(run.calls) != 3 {
			t.Fatalf("expected 3 gh calls, got %d: %v", len(run.calls), run.calls)
		}
		if got, want := run.calls[0], defaultBranchArgs("acme/widgets"); !reflect.DeepEqual(got, want) {
			t.Fatalf("call[0] = %v, want %v", got, want)
		}
		if got, want := run.calls[1], headCommitMessageArgs("acme/widgets", "feature-x"); !reflect.DeepEqual(got, want) {
			t.Fatalf("call[1] = %v, want %v", got, want)
		}
		wantCreate := createDraftPRArgs("acme/widgets", "feature-x", "main", "Add feature X", "Longer body.")
		if got := run.calls[2]; !reflect.DeepEqual(got, wantCreate) {
			t.Fatalf("call[2] = %v, want %v", got, wantCreate)
		}
	})

	t.Run("default-branch lookup failure stops before any create call", func(t *testing.T) {
		run := &sequentialRunner{errs: []error{errors.New("api error: 404")}}
		c := &Client{run: run}
		if _, err := c.CreateDraftPR(ctx, "acme/widgets", "feature-x"); err == nil {
			t.Fatal("CreateDraftPR() error = nil, want error when default-branch lookup fails")
		}
		if len(run.calls) != 1 {
			t.Fatalf("expected exactly 1 call (no further steps after failure), got %d: %v", len(run.calls), run.calls)
		}
	})

	t.Run("empty default_branch from gh is treated as a resolution failure", func(t *testing.T) {
		run := &sequentialRunner{outputs: [][]byte{[]byte("\n")}}
		c := &Client{run: run}
		if _, err := c.CreateDraftPR(ctx, "acme/widgets", "feature-x"); err == nil {
			t.Fatal("CreateDraftPR() error = nil, want error when gh returns an empty default_branch")
		}
		if len(run.calls) != 1 {
			t.Fatalf("expected exactly 1 call, got %d: %v", len(run.calls), run.calls)
		}
	})

	t.Run("commit-message lookup failure stops before the create call", func(t *testing.T) {
		run := &sequentialRunner{
			outputs: [][]byte{[]byte("main\n"), nil},
			errs:    []error{nil, errors.New("api error: 404 no such commit")},
		}
		c := &Client{run: run}
		if _, err := c.CreateDraftPR(ctx, "acme/widgets", "feature-x"); err == nil {
			t.Fatal("CreateDraftPR() error = nil, want error when commit-message lookup fails")
		}
		if len(run.calls) != 2 {
			t.Fatalf("expected exactly 2 calls (no create step after failure), got %d: %v", len(run.calls), run.calls)
		}
	})

	t.Run("create POST failure propagates", func(t *testing.T) {
		run := &sequentialRunner{
			outputs: [][]byte{[]byte("main\n"), []byte("Add feature X\n"), nil},
			errs:    []error{nil, nil, errors.New("api error: 422 already exists")},
		}
		c := &Client{run: run}
		if _, err := c.CreateDraftPR(ctx, "acme/widgets", "feature-x"); err == nil {
			t.Fatal("CreateDraftPR() error = nil, want propagated create error")
		}
	})

	t.Run("malformed create response returns an error, not a panic", func(t *testing.T) {
		run := &sequentialRunner{
			outputs: [][]byte{[]byte("main\n"), []byte("Add feature X\n"), []byte("not json")},
		}
		c := &Client{run: run}
		if _, err := c.CreateDraftPR(ctx, "acme/widgets", "feature-x"); err == nil {
			t.Fatal("CreateDraftPR() error = nil, want decode error for malformed create response")
		}
	})

	t.Run("falls back to the branch name as title when the commit message is empty", func(t *testing.T) {
		run := &sequentialRunner{
			outputs: [][]byte{
				[]byte("main\n"),
				[]byte(""),
				[]byte(`{"number":9,"html_url":"https://github.com/acme/widgets/pull/9","state":"open","draft":true}`),
			},
		}
		c := &Client{run: run}
		if _, err := c.CreateDraftPR(ctx, "acme/widgets", "feature-x"); err != nil {
			t.Fatalf("CreateDraftPR() error = %v", err)
		}
		wantCreate := createDraftPRArgs("acme/widgets", "feature-x", "main", "feature-x", "")
		if got := run.calls[2]; !reflect.DeepEqual(got, wantCreate) {
			t.Fatalf("call[2] = %v, want %v (branch-name title fallback)", got, wantCreate)
		}
	})
}
