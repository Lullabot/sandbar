package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/drupalorg"
	"github.com/lullabot/sandbar/internal/vm"
)

// fakeCollector is a collector double that returns canned values, so
// doPublish's decision logic can be exercised without a real VM or guest —
// mirrors land_test.go's fakeGh pattern.
type fakeCollector struct {
	module string
	cs     drupalorg.ChangeSet
	err    error
	calls  int
}

func (f *fakeCollector) Collect(context.Context, vm.VM, string) (string, drupalorg.ChangeSet, error) {
	f.calls++
	return f.module, f.cs, f.err
}

// fakeDestPublisher is a destPublisher double that records every call and
// returns canned values, so doPublish can be exercised without a real
// git.drupalcode.org call or a real PAT.
type fakeDestPublisher struct {
	dest       drupalorg.Destination
	resolveErr error

	result     drupalorg.Result
	publishErr error

	resolveCalls int
	publishCalls int
}

func (f *fakeDestPublisher) ResolveDestination(context.Context, string, int, bool) (drupalorg.Destination, error) {
	f.resolveCalls++
	return f.dest, f.resolveErr
}

func (f *fakeDestPublisher) Publish(context.Context, drupalorg.Destination, drupalorg.ChangeSet) (drupalorg.Result, error) {
	f.publishCalls++
	return f.result, f.publishErr
}

// sampleChangeSet is the common one-commit fixture every doPublish test
// starts from.
func sampleChangeSet() drupalorg.ChangeSet {
	return drupalorg.ChangeSet{
		Commits: []drupalorg.Commit{
			{
				Message:     "Fix the thing",
				AuthorName:  "Dev Eloper",
				AuthorEmail: "dev@example.com",
				Actions: []drupalorg.FileAction{
					{Kind: drupalorg.ActionUpdate, Path: "foo.php", Content: "<?php\n", Encoding: drupalorg.EncodingText},
				},
			},
		},
	}
}

func sampleDestination() drupalorg.Destination {
	return drupalorg.Destination{
		ForkPath:     "issue/foo-12345",
		Branch:       "foo-12345",
		ParentID:     42,
		ParentPath:   "project/foo",
		ParentBranch: "1.0.x",
	}
}

// --- doPublish: decline, non-TTY refusal, --yes, and the partial-failure report ---

func TestDoPublishDeclinePathPublishesNothing(t *testing.T) {
	coll := &fakeCollector{module: "foo", cs: sampleChangeSet()}
	dp := &fakeDestPublisher{dest: sampleDestination()}

	var stdout strings.Builder
	err := doPublish(context.Background(), &stdout, strings.NewReader("n\n"), true, coll, dp, vm.VM{Name: "foo"}, "/path", 12345, false, false)
	if err != nil {
		t.Fatalf("doPublish: unexpected error on decline: %v", err)
	}
	if dp.publishCalls != 0 {
		t.Errorf("Publish called %d times, want 0 on decline", dp.publishCalls)
	}
	out := stdout.String()
	if !strings.Contains(out, "declined") {
		t.Errorf("doPublish stdout = %q, want a decline notice", out)
	}
	// The confirmation must still have been printed before the prompt, so
	// the human had something to read before answering "no".
	if !strings.Contains(out, "issue/foo-12345") {
		t.Errorf("doPublish stdout = %q, want the confirmation to have been printed before the decline", out)
	}
}

func TestDoPublishNonTTYRefusesWithoutYes(t *testing.T) {
	coll := &fakeCollector{module: "foo", cs: sampleChangeSet()}
	dp := &fakeDestPublisher{dest: sampleDestination()}

	var stdout strings.Builder
	err := doPublish(context.Background(), &stdout, strings.NewReader(""), false, coll, dp, vm.VM{Name: "foo"}, "/path", 12345, false, false)
	if err == nil {
		t.Fatal("doPublish: want an error refusing to publish on a non-TTY without --yes")
	}
	for _, want := range []string{"not a terminal", "--yes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("doPublish error = %q, want it to mention %q", err.Error(), want)
		}
	}
	if dp.publishCalls != 0 {
		t.Errorf("Publish called %d times, want 0 when refused", dp.publishCalls)
	}
}

func TestDoPublishYesFlagSkipsPromptAndPublishes(t *testing.T) {
	coll := &fakeCollector{module: "foo", cs: sampleChangeSet()}
	dp := &fakeDestPublisher{
		dest: sampleDestination(),
		result: drupalorg.Result{
			Commits: []drupalorg.CommitResult{
				{Index: 0, Subject: "Fix the thing", Status: drupalorg.CommitLanded, SHA: "abc123"},
			},
		},
	}

	var stdout strings.Builder
	// tty=false here on purpose: --yes must be sufficient on its own, with
	// no terminal at all, since it IS the non-interactive confirmation.
	err := doPublish(context.Background(), &stdout, strings.NewReader(""), false, coll, dp, vm.VM{Name: "foo"}, "/path", 12345, true, false)
	if err != nil {
		t.Fatalf("doPublish: unexpected error with --yes: %v", err)
	}
	if dp.publishCalls != 1 {
		t.Errorf("Publish called %d times, want exactly 1", dp.publishCalls)
	}
	if !strings.Contains(stdout.String(), "abc123") {
		t.Errorf("doPublish stdout = %q, want the landed commit's SHA", stdout.String())
	}
}

func TestDoPublishReportsPartialFailureFromResult(t *testing.T) {
	failErr := errors.New("422: validation failed")
	res := drupalorg.Result{
		Commits: []drupalorg.CommitResult{
			{Index: 0, Subject: "first commit", Status: drupalorg.CommitLanded, SHA: "aaa111"},
			{Index: 1, Subject: "second commit", Status: drupalorg.CommitFailed, Err: failErr},
			{Index: 2, Subject: "third commit", Status: drupalorg.CommitNotAttempted},
		},
	}
	coll := &fakeCollector{module: "foo", cs: sampleChangeSet()}
	dp := &fakeDestPublisher{
		dest:       sampleDestination(),
		result:     res,
		publishErr: fmt.Errorf("sand publish: commit 2/3 failed: %w", failErr),
	}

	var stdout strings.Builder
	err := doPublish(context.Background(), &stdout, strings.NewReader(""), false, coll, dp, vm.VM{Name: "foo"}, "/path", 12345, true, false)
	if err == nil || !errors.Is(err, failErr) {
		t.Fatalf("doPublish error = %v, want it to wrap %v", err, failErr)
	}

	out := stdout.String()
	for _, want := range []string{
		"aaa111", "landed", "first commit",
		"second commit", "failed",
		"third commit", "not-attempted",
		"first failure",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doPublish stdout missing %q; got:\n%s", want, out)
		}
	}
}

func TestDoPublishNothingToPublishSkipsConfirmationAndDestination(t *testing.T) {
	coll := &fakeCollector{module: "foo", cs: drupalorg.ChangeSet{}}
	dp := &fakeDestPublisher{dest: sampleDestination()}

	var stdout strings.Builder
	err := doPublish(context.Background(), &stdout, strings.NewReader(""), true, coll, dp, vm.VM{Name: "foo"}, "/path", 12345, false, false)
	if err != nil {
		t.Fatalf("doPublish: unexpected error with nothing to publish: %v", err)
	}
	if dp.resolveCalls != 0 || dp.publishCalls != 0 {
		t.Errorf("destPublisher called (resolve=%d, publish=%d), want neither called when there is nothing to publish", dp.resolveCalls, dp.publishCalls)
	}
	if !strings.Contains(stdout.String(), "nothing to publish") {
		t.Errorf("doPublish stdout = %q, want a nothing-to-publish notice", stdout.String())
	}
}

// --- confirmPublish ---

func TestConfirmPublish(t *testing.T) {
	t.Run("yes flag bypasses the prompt entirely", func(t *testing.T) {
		var out strings.Builder
		ok, err := confirmPublish(&out, strings.NewReader(""), false, true)
		if err != nil || !ok {
			t.Fatalf("confirmPublish(yes=true) = (%v, %v), want (true, nil)", ok, err)
		}
		if out.String() != "" {
			t.Errorf("confirmPublish(yes=true) printed a prompt: %q", out.String())
		}
	})

	t.Run("tty accepts y/yes case-insensitively", func(t *testing.T) {
		for _, in := range []string{"y\n", "Y\n", "yes\n", "YES\n"} {
			var out strings.Builder
			ok, err := confirmPublish(&out, strings.NewReader(in), true, false)
			if err != nil || !ok {
				t.Errorf("confirmPublish(%q) = (%v, %v), want (true, nil)", in, ok, err)
			}
		}
	})

	t.Run("tty rejects anything else", func(t *testing.T) {
		ok, err := confirmPublish(&strings.Builder{}, strings.NewReader("no\n"), true, false)
		if err != nil || ok {
			t.Errorf("confirmPublish(no) = (%v, %v), want (false, nil)", ok, err)
		}
	})

	t.Run("non-tty without yes refuses", func(t *testing.T) {
		_, err := confirmPublish(&strings.Builder{}, strings.NewReader(""), false, false)
		if err == nil {
			t.Error("confirmPublish: want an error on a non-tty without yes")
		}
	})
}

// --- reorderPublishFlags ---

func TestReorderPublishFlags(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "yes after positional args",
			in:   []string{"myvm", "/path", "123", "--yes"},
			want: []string{"--yes", "myvm", "/path", "123"},
		},
		{
			name: "profile value reordered ahead of positionals",
			in:   []string{"myvm", "--profile", "work", "/path", "123"},
			want: []string{"--profile", "work", "myvm", "/path", "123"},
		},
		{
			name: "already-ordered flags are left alone",
			in:   []string{"--allow-outside-issue-namespace", "myvm", "/path", "123"},
			want: []string{"--allow-outside-issue-namespace", "myvm", "/path", "123"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reorderPublishFlags(tc.in)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("reorderPublishFlags(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// --- runPublish: argument validation and the absent-PAT message (neither touches a VM or the network) ---

func TestRunPublishArgValidation(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "no args", args: []string{}, wantErr: "need a VM NAME"},
		{name: "missing issue", args: []string{"vm", "/path"}, wantErr: "need a VM NAME"},
		{name: "too many args", args: []string{"vm", "/path", "123", "extra"}, wantErr: "need a VM NAME"},
		{name: "non-numeric issue", args: []string{"vm", "/path", "abc"}, wantErr: "invalid ISSUE"},
		{name: "zero issue", args: []string{"vm", "/path", "0"}, wantErr: "invalid ISSUE"},
		{name: "negative issue", args: []string{"vm", "/path", "-5"}, wantErr: "invalid ISSUE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runPublish(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("runPublish(%v) error = %v, want it to contain %q", tc.args, err, tc.wantErr)
			}
		})
	}
}

func TestRunPublishAbsentPATMessage(t *testing.T) {
	// No drupalorg.token is written under this XDG_CONFIG_HOME, so LoadToken
	// returns ErrNoToken — this must be reported before runPublish ever
	// touches a store, a registry, or a provider (all of which would need a
	// real environment this test does not set up).
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	err := runPublish([]string{"somevm", "/some/path", "12345"})
	if err == nil {
		t.Fatal("runPublish: want an error when no drupal.org token exists")
	}
	if !strings.Contains(err.Error(), "publication is unavailable") {
		t.Errorf("runPublish error = %v, want it to say publication is unavailable", err)
	}
	if !errors.Is(err, drupalorg.ErrNoToken) {
		t.Errorf("runPublish error = %v, want it to wrap drupalorg.ErrNoToken", err)
	}
}
