package main

import (
	"bufio"
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
	target drupalorg.RemoteTarget
	cs     drupalorg.ChangeSet
	err    error
	calls  int

	// syncStatus/syncErr drive Fetch; adoptErr drives Adopt. The zero
	// SyncStatus is "not diverged", which is what the tests that predate the
	// post-publish sync want: nothing to report, nothing offered.
	syncStatus  drupalorg.SyncStatus
	syncErr     error
	adoptErr    error
	fetchCalls  int
	adoptCalls  int
	adoptBranch string
}

func (f *fakeCollector) Fetch(_ context.Context, _ vm.VM, _, _ string) (drupalorg.SyncStatus, error) {
	f.fetchCalls++
	return f.syncStatus, f.syncErr
}

func (f *fakeCollector) Adopt(_ context.Context, _ vm.VM, _, branch string) error {
	f.adoptCalls++
	f.adoptBranch = branch
	return f.adoptErr
}

// divergedSync is the state a successful replay always leaves behind: two
// histories with the same content and no commit in common, and a clean tree.
func divergedSync() drupalorg.SyncStatus {
	return drupalorg.SyncStatus{
		Dirty: 0, Local: "aaaaaaa", Fork: "bbbbbbb", Ahead: 3, Behind: 3, SameContent: true,
	}
}

func (f *fakeCollector) Collect(context.Context, vm.VM, string) (drupalorg.RemoteTarget, drupalorg.ChangeSet, error) {
	f.calls++
	return f.target, f.cs, f.err
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

	// gotModule/gotIssue record what the last ResolveDestination call was
	// actually asked to resolve — the only way to observe whether doPublish
	// used the ISSUE it was given or the one read off the checkout's remote.
	gotModule string
	gotIssue  int
}

func (f *fakeDestPublisher) ResolveDestination(_ context.Context, module string, issue int, _ bool) (drupalorg.Destination, error) {
	f.resolveCalls++
	f.gotModule, f.gotIssue = module, issue
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
	coll := &fakeCollector{target: drupalorg.RemoteTarget{Module: "foo"}, cs: sampleChangeSet()}
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
	coll := &fakeCollector{target: drupalorg.RemoteTarget{Module: "foo"}, cs: sampleChangeSet()}
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
	coll := &fakeCollector{target: drupalorg.RemoteTarget{Module: "foo"}, cs: sampleChangeSet()}
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
	coll := &fakeCollector{target: drupalorg.RemoteTarget{Module: "foo"}, cs: sampleChangeSet()}
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
	coll := &fakeCollector{target: drupalorg.RemoteTarget{Module: "foo"}, cs: drupalorg.ChangeSet{}}
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
		ok, err := confirmPublish(&out, bufio.NewReader(strings.NewReader("")), false, true)
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
			ok, err := confirmPublish(&out, bufio.NewReader(strings.NewReader(in)), true, false)
			if err != nil || !ok {
				t.Errorf("confirmPublish(%q) = (%v, %v), want (true, nil)", in, ok, err)
			}
		}
	})

	t.Run("tty rejects anything else", func(t *testing.T) {
		ok, err := confirmPublish(&strings.Builder{}, bufio.NewReader(strings.NewReader("no\n")), true, false)
		if err != nil || ok {
			t.Errorf("confirmPublish(no) = (%v, %v), want (false, nil)", ok, err)
		}
	})

	t.Run("non-tty without yes refuses", func(t *testing.T) {
		_, err := confirmPublish(&strings.Builder{}, bufio.NewReader(strings.NewReader("")), false, false)
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
	// Argument validation must be decided before any token, store, or
	// provider is touched; pinning XDG_CONFIG_HOME at an empty dir keeps
	// that true of the test too, rather than letting it read whatever the
	// developer running it happens to have on disk.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "no args", args: []string{}, wantErr: "need a VM NAME"},
		{name: "only a name", args: []string{"vm"}, wantErr: "need a VM NAME"},
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

// Two arguments is now a complete invocation — the issue is read from the
// checkout's own remote — so it must get PAST argument validation rather than
// being rejected for arity. The absent-PAT refusal is simply the next gate it
// reaches in a test environment with no token, and reaching it is the proof.
func TestRunPublishAcceptsOmittedIssue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	err := runPublish([]string{"somevm", "/some/path"})
	if err == nil {
		t.Fatal("runPublish: want an error (no token), not a success")
	}
	if strings.Contains(err.Error(), "need a VM NAME") {
		t.Errorf("runPublish error = %v, want ISSUE to be optional rather than an arity failure", err)
	}
	if !strings.Contains(err.Error(), "publication is unavailable") {
		t.Errorf("runPublish error = %v, want it to have reached the token check", err)
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

// --- doPublish: where the issue number comes from -------------------------

// A checkout cloned from its issue fork names the issue in its own remote, so
// omitting ISSUE must resolve the destination for exactly that issue rather
// than refusing — this is the whole point of making the argument optional.
func TestDoPublishDerivesIssueFromForkRemote(t *testing.T) {
	coll := &fakeCollector{target: drupalorg.RemoteTarget{Module: "dubbot", Issue: 3619578}, cs: sampleChangeSet()}
	dp := &fakeDestPublisher{dest: sampleDestination()}

	var stdout strings.Builder
	err := doPublish(context.Background(), &stdout, strings.NewReader("n\n"), true, coll, dp, vm.VM{Name: "foo"}, "/path", 0, false, false)
	if err != nil {
		t.Fatalf("doPublish: unexpected error: %v", err)
	}
	if dp.gotModule != "dubbot" || dp.gotIssue != 3619578 {
		t.Errorf("ResolveDestination got (%q, %d), want (%q, %d)", dp.gotModule, dp.gotIssue, "dubbot", 3619578)
	}
	// A destination nobody typed must be announced, not assumed silently.
	if !strings.Contains(stdout.String(), "3619578") {
		t.Errorf("doPublish stdout = %q, want the derived issue number reported", stdout.String())
	}
}

// An explicitly given ISSUE is what the operator asked for, so it wins over
// the one the remote happens to name.
func TestDoPublishExplicitIssueOverridesForkRemote(t *testing.T) {
	coll := &fakeCollector{target: drupalorg.RemoteTarget{Module: "dubbot", Issue: 3619578}, cs: sampleChangeSet()}
	dp := &fakeDestPublisher{dest: sampleDestination()}

	var stdout strings.Builder
	err := doPublish(context.Background(), &stdout, strings.NewReader("n\n"), true, coll, dp, vm.VM{Name: "foo"}, "/path", 999, false, false)
	if err != nil {
		t.Fatalf("doPublish: unexpected error: %v", err)
	}
	if dp.gotIssue != 999 {
		t.Errorf("ResolveDestination got issue %d, want the explicit 999", dp.gotIssue)
	}
}

// A canonical "project/<module>" remote names no issue, so omitting ISSUE has
// to fail with something that says so and says what to do — never publish to
// a guessed destination.
func TestDoPublishWithoutIssueOnCanonicalRemoteRefuses(t *testing.T) {
	coll := &fakeCollector{target: drupalorg.RemoteTarget{Module: "foo"}, cs: sampleChangeSet()}
	dp := &fakeDestPublisher{dest: sampleDestination()}

	var stdout strings.Builder
	err := doPublish(context.Background(), &stdout, strings.NewReader("y\n"), true, coll, dp, vm.VM{Name: "foo"}, "/path", 0, false, false)
	if err == nil {
		t.Fatal("doPublish: want an error when neither the argument nor the remote names an issue")
	}
	if !strings.Contains(err.Error(), "ISSUE") {
		t.Errorf("doPublish error = %q, want it to name the ISSUE argument as the fix", err.Error())
	}
	if dp.resolveCalls != 0 || dp.publishCalls != 0 {
		t.Errorf("resolve/publish called (%d, %d), want (0, 0) with no issue to resolve", dp.resolveCalls, dp.publishCalls)
	}
}

// --- doPublish: reconciling the checkout after a replay -------------------

// A replay leaves the checkout holding commits that are now public under
// different SHAs. doPublish must fetch so that divergence is visible, and —
// the tree being clean and the content identical — offer to adopt what
// landed.
func TestDoPublishOffersToAdoptPublishedCommits(t *testing.T) {
	coll := &fakeCollector{target: drupalorg.RemoteTarget{Module: "foo"}, cs: sampleChangeSet(), syncStatus: divergedSync()}
	dp := &fakeDestPublisher{dest: sampleDestination(), result: drupalorg.Result{}}

	var stdout strings.Builder
	// "y" confirms the publish; the second "y" accepts the adopt offer.
	err := doPublish(context.Background(), &stdout, strings.NewReader("y\ny\n"), true, coll, dp, vm.VM{Name: "foo"}, "/path", 12345, false, false)
	if err != nil {
		t.Fatalf("doPublish: unexpected error: %v", err)
	}
	if coll.fetchCalls != 1 {
		t.Errorf("Fetch calls = %d, want 1", coll.fetchCalls)
	}
	if coll.adoptCalls != 1 {
		t.Fatalf("Adopt calls = %d, want 1", coll.adoptCalls)
	}
	if coll.adoptBranch != sampleDestination().Branch {
		t.Errorf("adopted branch = %q, want %q", coll.adoptBranch, sampleDestination().Branch)
	}
	out := stdout.String()
	if !strings.Contains(out, "different commits") {
		t.Errorf("stdout = %q, want the divergence explained", out)
	}
}

// Declining the offer must leave the checkout exactly as it was. The publish
// is done either way; the reset is a separate act with its own answer.
func TestDoPublishDeclinedAdoptLeavesCheckoutAlone(t *testing.T) {
	coll := &fakeCollector{target: drupalorg.RemoteTarget{Module: "foo"}, cs: sampleChangeSet(), syncStatus: divergedSync()}
	dp := &fakeDestPublisher{dest: sampleDestination()}

	var stdout strings.Builder
	if err := doPublish(context.Background(), &stdout, strings.NewReader("y\nn\n"), true, coll, dp, vm.VM{Name: "foo"}, "/path", 12345, false, false); err != nil {
		t.Fatalf("doPublish: unexpected error: %v", err)
	}
	if coll.adoptCalls != 0 {
		t.Errorf("Adopt calls = %d, want 0 after declining", coll.adoptCalls)
	}
	if !strings.Contains(stdout.String(), "left as is") {
		t.Errorf("stdout = %q, want the decline acknowledged", stdout.String())
	}
}

// A dirty tree means publication left uncommitted work behind, and a reset
// would destroy it. The offer must not be made at all — and the reason must
// be said, since "no prompt appeared" is not actionable on its own.
func TestDoPublishDoesNotOfferAdoptOnADirtyTree(t *testing.T) {
	status := divergedSync()
	status.Dirty = 2
	coll := &fakeCollector{target: drupalorg.RemoteTarget{Module: "foo"}, cs: sampleChangeSet(), syncStatus: status}
	dp := &fakeDestPublisher{dest: sampleDestination()}

	var stdout strings.Builder
	if err := doPublish(context.Background(), &stdout, strings.NewReader("y\ny\n"), true, coll, dp, vm.VM{Name: "foo"}, "/path", 12345, false, false); err != nil {
		t.Fatalf("doPublish: unexpected error: %v", err)
	}
	if coll.adoptCalls != 0 {
		t.Fatalf("Adopt calls = %d, want 0 with uncommitted work in the tree", coll.adoptCalls)
	}
	if !strings.Contains(stdout.String(), "uncommitted") {
		t.Errorf("stdout = %q, want the uncommitted work named as the reason", stdout.String())
	}
}

// Content that genuinely differs is not a SHA-divergence artifact, and a
// reset would discard real local work. No offer.
func TestDoPublishDoesNotOfferAdoptWhenContentDiffers(t *testing.T) {
	status := divergedSync()
	status.SameContent = false
	coll := &fakeCollector{target: drupalorg.RemoteTarget{Module: "foo"}, cs: sampleChangeSet(), syncStatus: status}
	dp := &fakeDestPublisher{dest: sampleDestination()}

	var stdout strings.Builder
	if err := doPublish(context.Background(), &stdout, strings.NewReader("y\ny\n"), true, coll, dp, vm.VM{Name: "foo"}, "/path", 12345, false, false); err != nil {
		t.Fatalf("doPublish: unexpected error: %v", err)
	}
	if coll.adoptCalls != 0 {
		t.Errorf("Adopt calls = %d, want 0 when the content differs", coll.adoptCalls)
	}
}

// --yes confirms the PUBLISH. A hard reset of a working tree is a different
// act, and a pipe is not a human who can answer for it: without a terminal
// the divergence is reported and nothing is touched.
func TestDoPublishNonTTYReportsButNeverAdopts(t *testing.T) {
	coll := &fakeCollector{target: drupalorg.RemoteTarget{Module: "foo"}, cs: sampleChangeSet(), syncStatus: divergedSync()}
	dp := &fakeDestPublisher{dest: sampleDestination()}

	var stdout strings.Builder
	if err := doPublish(context.Background(), &stdout, strings.NewReader(""), false, coll, dp, vm.VM{Name: "foo"}, "/path", 12345, true, false); err != nil {
		t.Fatalf("doPublish: unexpected error: %v", err)
	}
	if coll.adoptCalls != 0 {
		t.Fatalf("Adopt calls = %d, want 0 without a terminal", coll.adoptCalls)
	}
	if !strings.Contains(stdout.String(), "git reset --hard") {
		t.Errorf("stdout = %q, want the command to run reported instead", stdout.String())
	}
}

// The publish has already succeeded and cannot be rolled back by the time
// the sync runs, so a fetch failure is a warning — never a non-zero exit
// that would report a successful publish as a failed command.
func TestDoPublishFetchFailureIsOnlyAWarning(t *testing.T) {
	coll := &fakeCollector{
		target:  drupalorg.RemoteTarget{Module: "foo"},
		cs:      sampleChangeSet(),
		syncErr: errors.New("network unreachable"),
	}
	dp := &fakeDestPublisher{dest: sampleDestination()}

	var stdout strings.Builder
	if err := doPublish(context.Background(), &stdout, strings.NewReader("y\n"), true, coll, dp, vm.VM{Name: "foo"}, "/path", 12345, false, false); err != nil {
		t.Fatalf("doPublish: a fetch failure must not fail the command, got %v", err)
	}
	if !strings.Contains(stdout.String(), "warning") {
		t.Errorf("stdout = %q, want the fetch failure reported as a warning", stdout.String())
	}
}

// Nothing is offered, and no reset is attempted, when the publish never got
// as far as writing anything.
func TestDoPublishDeclineSkipsTheSyncEntirely(t *testing.T) {
	coll := &fakeCollector{target: drupalorg.RemoteTarget{Module: "foo"}, cs: sampleChangeSet(), syncStatus: divergedSync()}
	dp := &fakeDestPublisher{dest: sampleDestination()}

	var stdout strings.Builder
	if err := doPublish(context.Background(), &stdout, strings.NewReader("n\n"), true, coll, dp, vm.VM{Name: "foo"}, "/path", 12345, false, false); err != nil {
		t.Fatalf("doPublish: unexpected error: %v", err)
	}
	if coll.fetchCalls != 0 || coll.adoptCalls != 0 {
		t.Errorf("fetch/adopt calls = (%d, %d), want (0, 0) when the publish was declined", coll.fetchCalls, coll.adoptCalls)
	}
}
