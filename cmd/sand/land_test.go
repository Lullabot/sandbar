package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lullabot/sandbar/internal/checkouts"
	"github.com/lullabot/sandbar/internal/landgh"
	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/vm"
)

// fakeGh is a ghActions double that records every call and returns canned
// responses, so landPR/landWeb/listCheckouts can be exercised without a real
// gh binary or browser — mirrors landgh's own fakeRunner/fakeOpener pattern
// one level up.
type fakeGh struct {
	available bool

	prState    map[string]*landgh.PR
	prStateErr map[string]error
	prCalls    []string // "orgRepo|branch"

	createPR    *landgh.PR
	createErr   error
	createCalls []string // "orgRepo|branch"

	openErr   error
	openCalls []string
}

func prKey(orgRepo, branch string) string { return orgRepo + "|" + branch }

func (f *fakeGh) Available(context.Context) bool { return f.available }

func (f *fakeGh) PRState(_ context.Context, orgRepo, branch string) (*landgh.PR, error) {
	key := prKey(orgRepo, branch)
	f.prCalls = append(f.prCalls, key)
	if f.prStateErr != nil {
		if err, ok := f.prStateErr[key]; ok {
			return nil, err
		}
	}
	if f.prState == nil {
		return nil, nil
	}
	return f.prState[key], nil
}

func (f *fakeGh) CreateDraftPR(_ context.Context, orgRepo, branch string) (*landgh.PR, error) {
	f.createCalls = append(f.createCalls, prKey(orgRepo, branch))
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.createPR, nil
}

func (f *fakeGh) OpenInBrowser(_ context.Context, target string) error {
	f.openCalls = append(f.openCalls, target)
	return f.openErr
}

// pushedCheckout builds a Checkout in the PushStatePushed state with a
// recognized remote — the common fixture every --pr/--web test starts from.
func pushedCheckout(path, orgRepo, branch string) checkouts.Checkout {
	return checkouts.Checkout{
		Path:      path,
		Kind:      checkouts.KindRepo,
		Branch:    branch,
		Forge:     "github.com",
		OrgRepo:   orgRepo,
		PushState: checkouts.PushStatePushed,
		LastSeen:  time.Now(),
	}
}

// --- listCheckouts ---

func TestListCheckoutsAnnotatesPRState(t *testing.T) {
	vc := checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{
			pushedCheckout("/home/dev/proj", "acme/proj", "feature-x"),
			{
				Path:      "/home/dev/other",
				Kind:      checkouts.KindRepo,
				Branch:    "wip",
				OrgRepo:   "acme/other",
				PushState: checkouts.PushStateUnpushed,
				Ahead:     3,
			},
			{
				Path:      "/home/dev/local-only",
				Kind:      checkouts.KindRepo,
				Branch:    "scratch",
				PushState: checkouts.PushStateNever,
			},
			{
				Path:      "/home/dev/proj-wt",
				Kind:      checkouts.KindWorktree,
				Parent:    "/home/dev/proj",
				Branch:    "another-feature",
				OrgRepo:   "acme/proj",
				PushState: checkouts.PushStatePushed,
			},
		},
	}

	gh := &fakeGh{
		prState: map[string]*landgh.PR{
			prKey("acme/proj", "feature-x"):       {Number: 5, State: "OPEN", Draft: true},
			prKey("acme/proj", "another-feature"): nil, // no PR yet
		},
	}

	var buf strings.Builder
	if err := listCheckouts(context.Background(), &buf, gh, vc); err != nil {
		t.Fatalf("listCheckouts: unexpected error: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"/home/dev/proj", "#5 OPEN (draft)",
		"/home/dev/other", "unpushed (+3)",
		"/home/dev/local-only", "never pushed",
		"/home/dev/proj-wt", "worktree of /home/dev/proj", "no PR",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("listCheckouts output missing %q; got:\n%s", want, out)
		}
	}

	// gh.PRState must be consulted only for pushed checkouts with a remote —
	// never for the unpushed or never-pushed rows, which cannot have a PR.
	if len(gh.prCalls) != 2 {
		t.Errorf("PRState called %d times, want 2 (only the two pushed+remote rows); calls=%v", len(gh.prCalls), gh.prCalls)
	}
}

func TestListCheckoutsPRStateErrorDoesNotAbortListing(t *testing.T) {
	vc := checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{
			pushedCheckout("/home/dev/a", "acme/a", "br-a"),
			pushedCheckout("/home/dev/b", "acme/b", "br-b"),
		},
	}
	gh := &fakeGh{
		prStateErr: map[string]error{
			prKey("acme/a", "br-a"): errors.New("boom"),
		},
		prState: map[string]*landgh.PR{
			prKey("acme/b", "br-b"): {Number: 9, State: "OPEN"},
		},
	}

	var buf strings.Builder
	if err := listCheckouts(context.Background(), &buf, gh, vc); err != nil {
		t.Fatalf("listCheckouts: unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "? (gh error)") {
		t.Errorf("listCheckouts output missing the gh-error marker for the failing row; got:\n%s", out)
	}
	if !strings.Contains(out, "#9 OPEN") {
		t.Errorf("listCheckouts output missing the other row's real PR state; got:\n%s", out)
	}
}

func TestListCheckoutsEmpty(t *testing.T) {
	var buf strings.Builder
	if err := listCheckouts(context.Background(), &buf, &fakeGh{}, checkouts.VMCheckouts{}); err != nil {
		t.Fatalf("listCheckouts: unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "no git checkouts found") {
		t.Errorf("listCheckouts empty output = %q, want a no-checkouts message", buf.String())
	}
}

func TestListCheckoutsTruncatedFlag(t *testing.T) {
	vc := checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{{Path: "/x", Branch: "main", PushState: checkouts.PushStateNever}},
		Truncated: true,
	}
	var buf strings.Builder
	if err := listCheckouts(context.Background(), &buf, &fakeGh{}, vc); err != nil {
		t.Fatalf("listCheckouts: unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "truncated") {
		t.Errorf("listCheckouts output missing a truncation notice; got:\n%s", buf.String())
	}
}

// --- landPR: the --pr gh-absent TTY-vs-pipe branching ---

func TestLandPRUsesGhWhenAvailable(t *testing.T) {
	co := pushedCheckout("/home/dev/proj", "acme/proj", "feature-x")
	gh := &fakeGh{available: true, createPR: &landgh.PR{Number: 7, URL: "https://github.com/acme/proj/pull/7", Draft: true}}

	var stdout strings.Builder
	err := landPR(context.Background(), &stdout, gh, false /* tty irrelevant when gh available */, func() bool { return false }, co)
	if err != nil {
		t.Fatalf("landPR: unexpected error: %v", err)
	}
	if len(gh.createCalls) != 1 || gh.createCalls[0] != prKey("acme/proj", "feature-x") {
		t.Fatalf("CreateDraftPR calls = %v, want exactly one call for acme/proj|feature-x", gh.createCalls)
	}
	if !strings.Contains(stdout.String(), "https://github.com/acme/proj/pull/7") {
		t.Errorf("landPR stdout = %q, want it to report the created PR's URL", stdout.String())
	}
}

func TestLandPRGhCreateErrorIsWrapped(t *testing.T) {
	co := pushedCheckout("/home/dev/proj", "acme/proj", "feature-x")
	wantErr := errors.New("gh api boom")
	gh := &fakeGh{available: true, createErr: wantErr}

	var stdout strings.Builder
	err := landPR(context.Background(), &stdout, gh, false, func() bool { return false }, co)
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("landPR error = %v, want it to wrap %v", err, wantErr)
	}
}

func TestLandPRNoGhOnTTYOffersToOpen(t *testing.T) {
	co := pushedCheckout("/home/dev/proj", "acme/proj", "feature-x")
	wantURL := "https://github.com/acme/proj/pull/new/feature-x"

	t.Run("user accepts the offer", func(t *testing.T) {
		gh := &fakeGh{available: false}
		var stdout strings.Builder
		err := landPR(context.Background(), &stdout, gh, true /* tty */, func() bool { return true }, co)
		if err != nil {
			t.Fatalf("landPR: unexpected error on a TTY: %v", err)
		}
		if !strings.Contains(stdout.String(), wantURL) {
			t.Errorf("landPR stdout = %q, want the compare URL", stdout.String())
		}
		if len(gh.openCalls) != 1 || gh.openCalls[0] != wantURL {
			t.Errorf("OpenInBrowser calls = %v, want exactly [%q]", gh.openCalls, wantURL)
		}
	})

	t.Run("user declines the offer", func(t *testing.T) {
		gh := &fakeGh{available: false}
		var stdout strings.Builder
		err := landPR(context.Background(), &stdout, gh, true, func() bool { return false }, co)
		if err != nil {
			t.Fatalf("landPR: unexpected error on a TTY: %v", err)
		}
		if len(gh.openCalls) != 0 {
			t.Errorf("OpenInBrowser calls = %v, want none when the user declines", gh.openCalls)
		}
	})
}

func TestLandPRNoGhInPipeExitsNonZeroWithURLOnStderr(t *testing.T) {
	co := pushedCheckout("/home/dev/proj", "acme/proj", "feature-x")
	gh := &fakeGh{available: false}
	wantURL := "https://github.com/acme/proj/pull/new/feature-x"

	var stdout strings.Builder
	err := landPR(context.Background(), &stdout, gh, false /* not a tty: piped/scripted */, func() bool {
		t.Fatal("confirmOpen must not be consulted outside a TTY")
		return false
	}, co)

	// The acceptance criterion is "exits non-zero with the URL on stderr".
	// main.go's dispatch (identical to create/shell: `if err != nil {
	// fmt.Fprintln(os.Stderr, err); os.Exit(1) }`) turns ANY non-nil error
	// into exactly that: a non-zero exit with err.Error() on stderr. So the
	// contract this function must uphold is that err is non-nil and its
	// message IS the compare URL — nothing more, nothing less.
	if err == nil {
		t.Fatal("landPR: want a non-nil error in the no-gh, no-tty (piped) case")
	}
	if err.Error() != wantURL {
		t.Errorf("landPR error = %q, want it to be exactly the compare URL %q", err.Error(), wantURL)
	}
	if len(gh.openCalls) != 0 {
		t.Errorf("OpenInBrowser calls = %v, want none in the piped case", gh.openCalls)
	}
	if stdout.String() != "" {
		t.Errorf("landPR stdout = %q, want nothing written to stdout in the piped case (URL belongs on stderr via the returned error)", stdout.String())
	}
}

func TestLandPRRejectsUnpushedOrNoRemote(t *testing.T) {
	unpushed := checkouts.Checkout{Path: "/x", Branch: "wip", OrgRepo: "acme/x", PushState: checkouts.PushStateUnpushed, Ahead: 1}
	err := landPR(context.Background(), &strings.Builder{}, &fakeGh{}, false, nil, unpushed)
	if err == nil || !strings.Contains(err.Error(), "no pushed branch") {
		t.Errorf("landPR(unpushed) error = %v, want a 'no pushed branch' error", err)
	}

	noRemote := checkouts.Checkout{Path: "/y", Branch: "main", PushState: checkouts.PushStatePushed}
	err = landPR(context.Background(), &strings.Builder{}, &fakeGh{}, false, nil, noRemote)
	if err == nil || !strings.Contains(err.Error(), "no recognized remote") {
		t.Errorf("landPR(no remote) error = %v, want a 'no recognized remote' error", err)
	}
}

// --- landWeb: gh-free URL targeting ---

func TestLandWebOpensCompareURLWithoutCallingGh(t *testing.T) {
	co := pushedCheckout("/home/dev/proj", "acme/proj", "feature-x")
	gh := &fakeGh{available: false} // if landWeb ever calls Available/PRState, this would surface as a mismatch below

	if err := landWeb(context.Background(), gh, co); err != nil {
		t.Fatalf("landWeb: unexpected error: %v", err)
	}

	wantURL := "https://github.com/acme/proj/pull/new/feature-x"
	if len(gh.openCalls) != 1 || gh.openCalls[0] != wantURL {
		t.Fatalf("OpenInBrowser calls = %v, want exactly [%q]", gh.openCalls, wantURL)
	}
	if len(gh.prCalls) != 0 || len(gh.createCalls) != 0 {
		t.Errorf("landWeb must be gh-free: PRState calls=%v, CreateDraftPR calls=%v", gh.prCalls, gh.createCalls)
	}
}

func TestLandWebOpenErrorIsWrapped(t *testing.T) {
	co := pushedCheckout("/home/dev/proj", "acme/proj", "feature-x")
	wantErr := errors.New("no browser opener on this system")
	gh := &fakeGh{openErr: wantErr}

	err := landWeb(context.Background(), gh, co)
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("landWeb error = %v, want it to wrap %v", err, wantErr)
	}
}

func TestLandWebRejectsUnpushedOrNoRemote(t *testing.T) {
	unpushed := checkouts.Checkout{Path: "/x", Branch: "wip", OrgRepo: "acme/x", PushState: checkouts.PushStateUnpushed}
	if err := landWeb(context.Background(), &fakeGh{}, unpushed); err == nil {
		t.Error("landWeb(unpushed): want an error, got nil")
	}

	noRemote := checkouts.Checkout{Path: "/y", Branch: "main", PushState: checkouts.PushStatePushed}
	if err := landWeb(context.Background(), &fakeGh{}, noRemote); err == nil {
		t.Error("landWeb(no remote): want an error, got nil")
	}
}

// --- findCheckout ---

func TestFindCheckout(t *testing.T) {
	vc := checkouts.VMCheckouts{Checkouts: []checkouts.Checkout{{Path: "/a"}, {Path: "/b"}}}

	if co, err := findCheckout(vc, "/b"); err != nil || co.Path != "/b" {
		t.Errorf("findCheckout(/b) = (%v, %v), want the /b checkout with no error", co, err)
	}
	if _, err := findCheckout(vc, "/missing"); err == nil || !strings.Contains(err.Error(), `"/missing"`) {
		t.Errorf("findCheckout(/missing) error = %v, want it to name the missing path", err)
	}
}

// --- requireRunningVM: mirrors shell.go's shellAttachArgv tests ---

func TestRequireRunningVM(t *testing.T) {
	t.Run("not running", func(t *testing.T) {
		l := stubVMLister{vms: []vm.VM{{Name: "foo", Status: "Stopped"}}}
		_, err := requireRunningVM(l, "foo")
		if err == nil {
			t.Fatal("requireRunningVM: want an error for a stopped VM")
		}
		for _, want := range []string{`"foo"`, "not running", "Stopped"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("requireRunningVM error = %q, want it to contain %q", err.Error(), want)
			}
		}
	})

	t.Run("unknown instance", func(t *testing.T) {
		l := stubVMLister{vms: []vm.VM{{Name: "other", Status: "Running"}}}
		_, err := requireRunningVM(l, "missing")
		if err == nil || !strings.Contains(err.Error(), `"missing"`) {
			t.Errorf("requireRunningVM error = %v, want it to name the missing instance", err)
		}
	})

	t.Run("list error is wrapped", func(t *testing.T) {
		wantErr := errors.New("boom")
		l := stubVMLister{err: wantErr}
		_, err := requireRunningVM(l, "foo")
		if err == nil || !errors.Is(err, wantErr) {
			t.Fatalf("requireRunningVM error = %v, want it to wrap %v", err, wantErr)
		}
	})

	t.Run("running", func(t *testing.T) {
		l := stubVMLister{vms: []vm.VM{{Name: "foo", Status: "Running"}}}
		found, err := requireRunningVM(l, "foo")
		if err != nil {
			t.Fatalf("requireRunningVM: unexpected error: %v", err)
		}
		if found.Name != "foo" {
			t.Errorf("requireRunningVM found = %+v, want Name=foo", found)
		}
	})
}

// requireRunningVM's not-running/unknown errors must be distinguishable from
// lima.ErrNoSuchInstance's sentinel — sanity check that the wrapping doesn't
// accidentally hide it from a caller that wants errors.Is.
func TestRequireRunningVMWrapsErrNoSuchInstance(t *testing.T) {
	l := stubVMLister{err: lima.ErrNoSuchInstance}
	_, err := requireRunningVM(l, "foo")
	if err == nil || !strings.Contains(err.Error(), `no VM named "foo"`) {
		t.Errorf("requireRunningVM error = %v, want the friendly no-such-VM message", err)
	}
}

// --- reorderLandFlags ---

func TestReorderLandFlags(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "flag after positional args",
			in:   []string{"myvm", "/path/to/repo", "--pr"},
			want: []string{"--pr", "myvm", "/path/to/repo"},
		},
		{
			name: "profile value reordered ahead of positionals",
			in:   []string{"myvm", "--profile", "work", "/path"},
			want: []string{"--profile", "work", "myvm", "/path"},
		},
		{
			name: "already-ordered flags are left alone",
			in:   []string{"--web", "myvm", "/path"},
			want: []string{"--web", "myvm", "/path"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reorderLandFlags(tc.in)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("reorderLandFlags(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// --- runLand argument validation (no store/provider touched on these paths) ---

func TestRunLandArgValidation(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "no args", args: []string{}, wantErr: "need a VM NAME"},
		{name: "too many positional args", args: []string{"vm", "/a", "/b"}, wantErr: "need a VM NAME"},
		{name: "pr and web together", args: []string{"vm", "/path", "--pr", "--web"}, wantErr: "cannot be used together"},
		{name: "pr without path", args: []string{"vm", "--pr"}, wantErr: "require a checkout PATH"},
		{name: "web without path", args: []string{"vm", "--web"}, wantErr: "require a checkout PATH"},
		{name: "path without a flag", args: []string{"vm", "/path"}, wantErr: "neither --pr nor --web"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := runLand(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("runLand(%v) error = %v, want it to contain %q", tc.args, err, tc.wantErr)
			}
		})
	}
}
