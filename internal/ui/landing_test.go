package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/checkouts"
	"github.com/lullabot/sandbar/internal/landgh"
	"github.com/lullabot/sandbar/internal/vm"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// --- fakeGhActions: the ghActions seam's test double ------------------------

// fakeGhActions records every call it receives and returns canned results, so
// a test can exercise the Landing pane's dispatch logic without spawning a
// real gh binary or opening a real browser — mirroring landgh's own
// fakeRunner/fakeOpener test doubles (internal/landgh/fakes_test.go).
type fakeGhActions struct {
	availability landgh.Availability

	prStateCalls []struct{ orgRepo, branch string }
	prState      *landgh.PR
	prStateErr   error

	createCalls []struct{ orgRepo, branch string }
	createPR    *landgh.PR
	createErr   error

	opened  []string
	openErr error
}

func (f *fakeGhActions) Availability(context.Context) landgh.Availability {
	return f.availability
}

// ghUp is the "host gh is installed and authenticated" fixture, named because
// most tests only care that the one-key path is open. Its zero-value opposite
// (a bare landgh.Availability{}) is gh missing from PATH entirely.
func ghUp() landgh.Availability {
	return landgh.Availability{Installed: true, Authenticated: true}
}

func (f *fakeGhActions) PRState(_ context.Context, orgRepo, branch string) (*landgh.PR, error) {
	f.prStateCalls = append(f.prStateCalls, struct{ orgRepo, branch string }{orgRepo, branch})
	return f.prState, f.prStateErr
}

func (f *fakeGhActions) CreateDraftPR(_ context.Context, orgRepo, branch string) (*landgh.PR, error) {
	f.createCalls = append(f.createCalls, struct{ orgRepo, branch string }{orgRepo, branch})
	return f.createPR, f.createErr
}

func (f *fakeGhActions) OpenInBrowser(_ context.Context, target string) error {
	f.opened = append(f.opened, target)
	return f.openErr
}

// --- classifyLandRow: the pure row-state -> action mapping, every table case

func TestClassifyLandRowPushedNoPRNotYetResolved(t *testing.T) {
	c := checkouts.Checkout{Path: "/home/user/repo", PushState: checkouts.PushStatePushed, OrgRepo: "acme/repo", Forge: "github.com", Branch: "feature"}
	row := classifyLandRow(c, nil, false)
	if row.Kind != landRowPushedNoPR {
		t.Fatalf("Kind = %v, want landRowPushedNoPR", row.Kind)
	}
	if row.Action != landActionOpenDraftPR {
		t.Fatalf("Action = %v, want landActionOpenDraftPR", row.Action)
	}
	if !strings.Contains(row.Label, "checking") {
		t.Fatalf("Label = %q, want it to say the PR check is still provisional", row.Label)
	}
}

func TestClassifyLandRowPushedNoPRResolved(t *testing.T) {
	c := checkouts.Checkout{Path: "/home/user/repo", PushState: checkouts.PushStatePushed, OrgRepo: "acme/repo", Forge: "github.com", Branch: "feature"}
	row := classifyLandRow(c, nil, true)
	if row.Kind != landRowPushedNoPR {
		t.Fatalf("Kind = %v, want landRowPushedNoPR", row.Kind)
	}
	if row.Action != landActionOpenDraftPR {
		t.Fatalf("Action = %v, want landActionOpenDraftPR", row.Action)
	}
	if strings.Contains(row.Label, "checking") {
		t.Fatalf("Label = %q, an AUTHORITATIVELY resolved no-PR row must not say it is still checking", row.Label)
	}
}

func TestClassifyLandRowPushedHasPR(t *testing.T) {
	c := checkouts.Checkout{Path: "/home/user/repo", PushState: checkouts.PushStatePushed, OrgRepo: "acme/repo", Forge: "github.com", Branch: "feature"}
	pr := &landgh.PR{Number: 42, URL: "https://github.com/acme/repo/pull/42", State: "OPEN", Draft: true}
	row := classifyLandRow(c, pr, true)
	if row.Kind != landRowPushedHasPR {
		t.Fatalf("Kind = %v, want landRowPushedHasPR", row.Kind)
	}
	if row.Action != landActionOpenInBrowser {
		t.Fatalf("Action = %v, want landActionOpenInBrowser", row.Action)
	}
	if !strings.Contains(row.Label, "#42") || !strings.Contains(row.Label, "draft") {
		t.Fatalf("Label = %q, want it to name the PR number and its draft state", row.Label)
	}
}

func TestClassifyLandRowUnpushed(t *testing.T) {
	c := checkouts.Checkout{Path: "/home/user/repo", PushState: checkouts.PushStateUnpushed, Ahead: 3, OrgRepo: "acme/repo", Forge: "github.com"}
	row := classifyLandRow(c, nil, false)
	if row.Kind != landRowAtRisk {
		t.Fatalf("Kind = %v, want landRowAtRisk", row.Kind)
	}
	if row.Action != landActionNone {
		t.Fatalf("Action = %v, want landActionNone (push in the shell first)", row.Action)
	}
	if !strings.Contains(row.Label, "3") {
		t.Fatalf("Label = %q, want the ahead count", row.Label)
	}
}

func TestClassifyLandRowDirtyOverridesAnAlreadyPushedPR(t *testing.T) {
	// Pushed AND already has an open PR, but there are uncommitted changes: the
	// safe row is still at-risk/no-action — never a one-key action that could
	// paper over local state the branch on GitHub does not yet reflect.
	c := checkouts.Checkout{Path: "/home/user/repo", PushState: checkouts.PushStatePushed, Dirty: 2, OrgRepo: "acme/repo", Forge: "github.com"}
	pr := &landgh.PR{Number: 1, URL: "https://github.com/acme/repo/pull/1"}
	row := classifyLandRow(c, pr, true)
	if row.Kind != landRowAtRisk {
		t.Fatalf("Kind = %v, want landRowAtRisk (dirty overrides an existing PR)", row.Kind)
	}
	if row.Action != landActionNone {
		t.Fatalf("Action = %v, want landActionNone", row.Action)
	}
}

func TestClassifyLandRowUnpushedAndDirtyCombinedLabel(t *testing.T) {
	c := checkouts.Checkout{Path: "/home/user/repo", PushState: checkouts.PushStateUnpushed, Ahead: 2, Dirty: 1}
	row := classifyLandRow(c, nil, false)
	if row.Kind != landRowAtRisk {
		t.Fatalf("Kind = %v, want landRowAtRisk", row.Kind)
	}
	if !strings.Contains(row.Label, "2") || !strings.Contains(row.Label, "1") {
		t.Fatalf("Label = %q, want both the ahead count and the dirty count", row.Label)
	}
}

func TestClassifyLandRowNeverPushed(t *testing.T) {
	c := checkouts.Checkout{Path: "/home/user/repo", PushState: checkouts.PushStateNever, OrgRepo: "acme/repo", Forge: "github.com"}
	row := classifyLandRow(c, nil, false)
	if row.Kind != landRowLocalOnly {
		t.Fatalf("Kind = %v, want landRowLocalOnly", row.Kind)
	}
	if row.Action != landActionNone {
		t.Fatalf("Action = %v, want landActionNone", row.Action)
	}
	if row.Label != "local only" {
		t.Fatalf("Label = %q, want %q", row.Label, "local only")
	}
}

func TestClassifyLandRowNoRemoteAtAll(t *testing.T) {
	c := checkouts.Checkout{Path: "/home/user/repo", PushState: checkouts.PushStatePushed, OrgRepo: ""}
	row := classifyLandRow(c, nil, false)
	if row.Kind != landRowLocalOnly {
		t.Fatalf("Kind = %v, want landRowLocalOnly (no remote configured at all)", row.Kind)
	}
	if row.Action != landActionNone {
		t.Fatalf("Action = %v, want landActionNone", row.Action)
	}
}

func TestClassifyLandRowGitLabForgeNoOneKeyAction(t *testing.T) {
	c := checkouts.Checkout{Path: "/home/user/repo", PushState: checkouts.PushStatePushed, OrgRepo: "group/repo", Forge: "gitlab.com", Branch: "feature"}
	// Even if somehow "resolved" with a PR-shaped result (never actually
	// produced for a non-GitHub forge — handleLandingAvailable only fires
	// PRState for github.com checkouts), gh scope is GitHub-only: no one-key
	// action for GitLab/drupal.org.
	row := classifyLandRow(c, &landgh.PR{Number: 1}, true)
	if row.Kind != landRowOtherForge {
		t.Fatalf("Kind = %v, want landRowOtherForge", row.Kind)
	}
	if row.Action != landActionNone {
		t.Fatalf("Action = %v, want landActionNone — no one-key MR action for GitLab (deferred)", row.Action)
	}
	if !strings.Contains(row.Label, "gitlab.com") {
		t.Fatalf("Label = %q, want it to name the forge", row.Label)
	}
}

func TestClassifyLandRowDrupalOrgForgeNoOneKeyAction(t *testing.T) {
	c := checkouts.Checkout{Path: "/home/user/repo", PushState: checkouts.PushStatePushed, OrgRepo: "project/module", Forge: "git.drupalcode.org", Branch: "1.0.x"}
	row := classifyLandRow(c, nil, false)
	if row.Kind != landRowOtherForge {
		t.Fatalf("Kind = %v, want landRowOtherForge", row.Kind)
	}
	if row.Action != landActionNone {
		t.Fatalf("Action = %v, want landActionNone", row.Action)
	}
}

// --- groupCheckouts: worktrees nested under their parent repo ---------------

func TestGroupCheckoutsWorktreeUnderParent(t *testing.T) {
	cs := []checkouts.Checkout{
		{Path: "/home/user/repo", Kind: checkouts.KindRepo},
		{Path: "/home/user/repo-wt", Kind: checkouts.KindWorktree, Parent: "/home/user/repo"},
	}
	groups := groupCheckouts(cs)
	if len(groups) != 1 {
		t.Fatalf("len(groups) = %d, want 1 (the worktree must nest, not stand alone)", len(groups))
	}
	if !groups[0].HasRepo || groups[0].Repo.Path != "/home/user/repo" {
		t.Fatalf("groups[0].Repo = %+v, want the repo row", groups[0].Repo)
	}
	if len(groups[0].Worktrees) != 1 || groups[0].Worktrees[0].Path != "/home/user/repo-wt" {
		t.Fatalf("groups[0].Worktrees = %+v, want the one worktree", groups[0].Worktrees)
	}
}

func TestGroupCheckoutsMultipleReposPreserveOrder(t *testing.T) {
	cs := []checkouts.Checkout{
		{Path: "/home/user/a", Kind: checkouts.KindRepo},
		{Path: "/home/user/b", Kind: checkouts.KindRepo},
		{Path: "/home/user/a-wt", Kind: checkouts.KindWorktree, Parent: "/home/user/a"},
		{Path: "/home/user/b-wt", Kind: checkouts.KindWorktree, Parent: "/home/user/b"},
	}
	groups := groupCheckouts(cs)
	if len(groups) != 2 {
		t.Fatalf("len(groups) = %d, want 2", len(groups))
	}
	if groups[0].Repo.Path != "/home/user/a" || groups[1].Repo.Path != "/home/user/b" {
		t.Fatalf("groups out of order: %+v", groups)
	}
	if len(groups[0].Worktrees) != 1 || groups[0].Worktrees[0].Path != "/home/user/a-wt" {
		t.Fatalf("groups[0] worktrees = %+v, want a's own worktree only", groups[0].Worktrees)
	}
	if len(groups[1].Worktrees) != 1 || groups[1].Worktrees[0].Path != "/home/user/b-wt" {
		t.Fatalf("groups[1] worktrees = %+v, want b's own worktree only", groups[1].Worktrees)
	}
}

func TestGroupCheckoutsOrphanWorktreeStillRendered(t *testing.T) {
	// The worktree's parent was cut by the sweep's own cap (or otherwise never
	// recorded) — it must still show up, standing alone, rather than vanish.
	cs := []checkouts.Checkout{
		{Path: "/home/user/repo", Kind: checkouts.KindRepo},
		{Path: "/home/user/orphan-wt", Kind: checkouts.KindWorktree, Parent: "/home/user/never-swept"},
	}
	groups := groupCheckouts(cs)
	if len(groups) != 2 {
		t.Fatalf("len(groups) = %d, want 2 (the repo, plus the orphan standing alone)", len(groups))
	}
	orphan := groups[1]
	if orphan.HasRepo {
		t.Fatalf("orphan group HasRepo = true, want false")
	}
	if len(orphan.Worktrees) != 1 || orphan.Worktrees[0].Path != "/home/user/orphan-wt" {
		t.Fatalf("orphan group worktrees = %+v, want the orphan", orphan.Worktrees)
	}
}

// --- buildLandRows: grouping + resolution folded into the flat row list ----

func TestBuildLandRowsIndentsWorktreesUnderParent(t *testing.T) {
	groups := groupCheckouts([]checkouts.Checkout{
		{Path: "/home/user/repo", Kind: checkouts.KindRepo, PushState: checkouts.PushStateNever},
		{Path: "/home/user/repo-wt", Kind: checkouts.KindWorktree, Parent: "/home/user/repo", PushState: checkouts.PushStateNever},
	})
	rows := buildLandRows(groups, map[string]resolvedPR{})
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if rows[0].Indent {
		t.Fatalf("rows[0] (the repo) is indented, want top-level")
	}
	if !rows[1].Indent {
		t.Fatalf("rows[1] (the worktree) is not indented, want it nested under its parent")
	}
}

func TestBuildLandRowsUsesResolvedPRPerPath(t *testing.T) {
	c := checkouts.Checkout{Path: "/home/user/repo", Kind: checkouts.KindRepo, PushState: checkouts.PushStatePushed, OrgRepo: "acme/repo", Forge: "github.com", Branch: "feature"}
	groups := groupCheckouts([]checkouts.Checkout{c})
	pr := &landgh.PR{Number: 9, URL: "https://github.com/acme/repo/pull/9"}
	rows := buildLandRows(groups, map[string]resolvedPR{"/home/user/repo": {pr: pr, ok: true}})
	if rows[0].Kind != landRowPushedHasPR {
		t.Fatalf("rows[0].Kind = %v, want landRowPushedHasPR", rows[0].Kind)
	}
	if rows[0].PR != pr {
		t.Fatalf("rows[0].PR = %+v, want the resolved PR", rows[0].PR)
	}
}

// --- landDraftPRRun / landOpenBrowserRun: the dispatched action bodies -----

func TestLandDraftPRRunCreatesWhenGhAvailable(t *testing.T) {
	fake := &fakeGhActions{createPR: &landgh.PR{Number: 7, URL: "https://github.com/acme/repo/pull/7"}}
	run := landDraftPRRun(fake, true, "acme/repo", "feature")
	var out strings.Builder
	if err := run(context.Background(), &out); err != nil {
		t.Fatalf("run() = %v, want nil", err)
	}
	if len(fake.createCalls) != 1 || fake.createCalls[0].orgRepo != "acme/repo" || fake.createCalls[0].branch != "feature" {
		t.Fatalf("CreateDraftPR calls = %+v, want exactly one call for acme/repo#feature", fake.createCalls)
	}
	if len(fake.opened) != 0 {
		t.Fatalf("OpenInBrowser calls = %v, want none — gh was available, so no browser fallback", fake.opened)
	}
	if !strings.Contains(out.String(), "#7") {
		t.Fatalf("output = %q, want it to name the created PR", out.String())
	}
}

// TestLandDraftPRRunOpensCompareURLWhenGhUnavailable is the acceptance
// criterion's graceful-degradation case: with host gh absent/unauthed
// (available == false), "Open draft PR" must NEVER call CreateDraftPR — it
// falls back to opening the gh-free compare URL in the browser instead, so
// the action is never simply dead without gh.
func TestLandDraftPRRunOpensCompareURLWhenGhUnavailable(t *testing.T) {
	fake := &fakeGhActions{availability: landgh.Availability{}}
	run := landDraftPRRun(fake, false, "acme/repo", "feature")
	var out strings.Builder
	if err := run(context.Background(), &out); err != nil {
		t.Fatalf("run() = %v, want nil", err)
	}
	if len(fake.createCalls) != 0 {
		t.Fatalf("CreateDraftPR calls = %+v, want none — gh is unavailable", fake.createCalls)
	}
	wantURL := "https://github.com/acme/repo/pull/new/feature"
	if len(fake.opened) != 1 || fake.opened[0] != wantURL {
		t.Fatalf("OpenInBrowser calls = %v, want exactly one call opening %q", fake.opened, wantURL)
	}
	if !strings.Contains(out.String(), wantURL) {
		t.Fatalf("output = %q, want it to name the compare URL it opened", out.String())
	}
}

func TestLandOpenBrowserRunOpensThePRURL(t *testing.T) {
	fake := &fakeGhActions{}
	pr := &landgh.PR{Number: 5, URL: "https://github.com/acme/repo/pull/5"}
	run := landOpenBrowserRun(fake, "acme/repo", pr)
	var out strings.Builder
	if err := run(context.Background(), &out); err != nil {
		t.Fatalf("run() = %v, want nil", err)
	}
	if len(fake.opened) != 1 || fake.opened[0] != pr.URL {
		t.Fatalf("OpenInBrowser calls = %v, want exactly one call opening %q", fake.opened, pr.URL)
	}
}

func TestLandOpenBrowserRunFallsBackToConstructedURL(t *testing.T) {
	// gh's response happened not to carry a URL: PRURL must fill the gap.
	fake := &fakeGhActions{}
	pr := &landgh.PR{Number: 5}
	run := landOpenBrowserRun(fake, "acme/repo", pr)
	var out strings.Builder
	if err := run(context.Background(), &out); err != nil {
		t.Fatalf("run() = %v, want nil", err)
	}
	want := "https://github.com/acme/repo/pull/5"
	if len(fake.opened) != 1 || fake.opened[0] != want {
		t.Fatalf("OpenInBrowser calls = %v, want exactly one call opening %q", fake.opened, want)
	}
}

// --- The pane wired into the model: open, navigate, act --------------------

func landingTestVM(t *testing.T, name string) (model, boardVM) {
	t.Helper()
	m := newTestModel(t)
	m = resized(m, 100, 40)
	m = putOnBoard(t, m, vm.VM{Name: name, Status: limaRunning, CPUs: 2})
	v, ok := m.focusedVM()
	if !ok {
		t.Fatalf("putOnBoard did not focus %s", name)
	}
	return m, v
}

func TestOpenLandingPaneGroupsAndSwitchesView(t *testing.T) {
	m, v := landingTestVM(t, "web")
	if err := m.checkouts.Set(v.scope, v.Name, checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{
			{Path: "/home/user/repo", Kind: checkouts.KindRepo, Branch: "main", PushState: checkouts.PushStateNever},
			{Path: "/home/user/repo-wt", Kind: checkouts.KindWorktree, Parent: "/home/user/repo", Branch: "feature", PushState: checkouts.PushStateNever},
		},
	}); err != nil {
		t.Fatalf("seed checkouts: %v", err)
	}
	m.ghActions = &fakeGhActions{availability: landgh.Availability{}}

	cmd := m.openLandingPane(v)
	if m.view != viewLanding {
		t.Fatalf("view = %v, want viewLanding", m.view)
	}
	if len(m.landing.rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2 (grouped repo + worktree)", len(m.landing.rows))
	}
	if m.landing.rows[1].Indent != true {
		t.Fatalf("rows[1].Indent = false, want the worktree indented under its parent")
	}
	if cmd == nil {
		t.Fatal("openLandingPane returned a nil cmd — the availability check must always fire")
	}
}

// TestLandingAvailableFiresPRStateOnlyForPushedGitHubRows drives the full
// open -> availability-check -> per-row PRState sequence through the real
// teaLoop harness (jobs_test.go), asserting that ONLY the pushed GitHub
// checkout gets an authoritative lookup: the GitLab row (gh scope is
// GitHub-only) and the never-pushed row (nothing to check) must not.
func TestLandingAvailableFiresPRStateOnlyForPushedGitHubRows(t *testing.T) {
	m, v := landingTestVM(t, "web")
	if err := m.checkouts.Set(v.scope, v.Name, checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{
			{Path: "/home/user/gh-repo", Kind: checkouts.KindRepo, Branch: "feature", OrgRepo: "acme/repo", Forge: "github.com", PushState: checkouts.PushStatePushed},
			{Path: "/home/user/gitlab-repo", Kind: checkouts.KindRepo, Branch: "feature", OrgRepo: "group/repo", Forge: "gitlab.com", PushState: checkouts.PushStatePushed},
			{Path: "/home/user/local-repo", Kind: checkouts.KindRepo, Branch: "wip", PushState: checkouts.PushStateNever},
		},
	}); err != nil {
		t.Fatalf("seed checkouts: %v", err)
	}
	fake := &fakeGhActions{availability: ghUp(), prState: &landgh.PR{Number: 11, URL: "https://github.com/acme/repo/pull/11"}}
	m.ghActions = fake
	cmd := m.openLandingPane(v)

	l := newTeaLoop(t, m)
	l.exec(cmd)
	l.pump("PR state resolved for the github.com row", func(m model) bool {
		row := m.landing.rows[0]
		return row.PRResolved && row.Kind == landRowPushedHasPR
	})

	if len(fake.prStateCalls) != 1 {
		t.Fatalf("PRState calls = %+v, want exactly one (the github.com row only)", fake.prStateCalls)
	}
	if fake.prStateCalls[0].orgRepo != "acme/repo" || fake.prStateCalls[0].branch != "feature" {
		t.Fatalf("PRState call = %+v, want acme/repo#feature", fake.prStateCalls[0])
	}
	// The GitLab and never-pushed rows stay exactly as classifyLandRow put
	// them without any resolution ever landing for them.
	if l.m.landing.rows[1].PRResolved {
		t.Fatal("the GitLab row must never receive an authoritative PRState resolution (gh scope is GitHub-only)")
	}
	if l.m.landing.rows[2].PRResolved {
		t.Fatal("the never-pushed row must never receive a PRState resolution")
	}
}

// TestHandleLandingAvailableDropsStaleResult proves a result for a VM the
// pane has since moved on from (closed and reopened on a different VM) is
// recognized as stale and ignored, rather than corrupting the CURRENT pane's
// state.
func TestHandleLandingAvailableDropsStaleResult(t *testing.T) {
	m, v := landingTestVM(t, "web")
	m.ghActions = &fakeGhActions{availability: ghUp()}
	m.openLandingPane(v)

	// A result for a DIFFERENT vm name — as if the pane had moved on.
	cmd := m.handleLandingAvailable(landingAvailableMsg{scope: v.scope, vm: "some-other-vm", availability: ghUp()})
	if cmd != nil {
		t.Fatal("a stale availability result must not fire any further commands")
	}
	if m.landing.ghChecked {
		t.Fatal("a stale availability result must not be folded into the pane's current state")
	}
}

// TestHandleLandingPRStateDropsStaleResult mirrors the above for a stale
// PRState result.
func TestHandleLandingPRStateDropsStaleResult(t *testing.T) {
	m, v := landingTestVM(t, "web")
	if err := m.checkouts.Set(v.scope, v.Name, checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{
			{Path: "/home/user/repo", Kind: checkouts.KindRepo, Branch: "feature", OrgRepo: "acme/repo", Forge: "github.com", PushState: checkouts.PushStatePushed},
		},
	}); err != nil {
		t.Fatalf("seed checkouts: %v", err)
	}
	m.ghActions = &fakeGhActions{}
	m.openLandingPane(v)

	pr := &landgh.PR{Number: 3}
	m.handleLandingPRState(landingPRStateMsg{scope: v.scope, vm: "some-other-vm", path: "/home/user/repo", pr: pr})
	if m.landing.rows[0].PRResolved {
		t.Fatal("a stale PRState result must not be folded into the pane's current rows")
	}
}

// TestHandleLandingPRStateIgnoresErrors: a failed authoritative check must
// leave the row on its provisional hint rather than being folded in as a
// (bogus) resolution.
func TestHandleLandingPRStateIgnoresErrors(t *testing.T) {
	m, v := landingTestVM(t, "web")
	if err := m.checkouts.Set(v.scope, v.Name, checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{
			{Path: "/home/user/repo", Kind: checkouts.KindRepo, Branch: "feature", OrgRepo: "acme/repo", Forge: "github.com", PushState: checkouts.PushStatePushed},
		},
	}); err != nil {
		t.Fatalf("seed checkouts: %v", err)
	}
	m.ghActions = &fakeGhActions{}
	m.openLandingPane(v)

	m.handleLandingPRState(landingPRStateMsg{scope: v.scope, vm: v.Name, path: "/home/user/repo", err: errors.New("boom")})
	if m.landing.rows[0].PRResolved {
		t.Fatal("an error result must not mark the row resolved")
	}
}

// --- Navigation and dispatch, driven through real key presses --------------

func TestUpdateLandingCursorMovesWithinBounds(t *testing.T) {
	m, v := landingTestVM(t, "web")
	if err := m.checkouts.Set(v.scope, v.Name, checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{
			{Path: "/home/user/a", Kind: checkouts.KindRepo, PushState: checkouts.PushStateNever},
			{Path: "/home/user/b", Kind: checkouts.KindRepo, PushState: checkouts.PushStateNever},
		},
	}); err != nil {
		t.Fatalf("seed checkouts: %v", err)
	}
	m.ghActions = &fakeGhActions{}
	m.openLandingPane(v)

	if m.landing.cursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", m.landing.cursor)
	}
	next, _ := m.updateLanding(tea.KeyPressMsg{Code: tea.KeyUp})
	m = next.(model)
	if m.landing.cursor != 0 {
		t.Fatalf("cursor after Up at the top = %d, want 0 (clamped)", m.landing.cursor)
	}
	next, _ = m.updateLanding(tea.KeyPressMsg{Code: tea.KeyDown})
	m = next.(model)
	if m.landing.cursor != 1 {
		t.Fatalf("cursor after Down = %d, want 1", m.landing.cursor)
	}
	next, _ = m.updateLanding(tea.KeyPressMsg{Code: tea.KeyDown})
	m = next.(model)
	if m.landing.cursor != 1 {
		t.Fatalf("cursor after Down past the end = %d, want 1 (clamped)", m.landing.cursor)
	}
}

func TestUpdateLandingBackReturnsToBoard(t *testing.T) {
	m, v := landingTestVM(t, "web")
	m.ghActions = &fakeGhActions{}
	m.openLandingPane(v)

	next, _ := m.updateLanding(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = next.(model)
	if m.view != viewBoard {
		t.Fatalf("view after Back = %v, want viewBoard", m.view)
	}
}

func TestUpdateLandingActKeyRunsTheRowsActionAsAJob(t *testing.T) {
	m, v := landingTestVM(t, "web")
	if err := m.checkouts.Set(v.scope, v.Name, checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{
			{Path: "/home/user/repo", Kind: checkouts.KindRepo, Branch: "feature", OrgRepo: "acme/repo", Forge: "github.com", PushState: checkouts.PushStatePushed},
		},
	}); err != nil {
		t.Fatalf("seed checkouts: %v", err)
	}
	fake := &fakeGhActions{availability: ghUp(), createPR: &landgh.PR{Number: 21, URL: "https://github.com/acme/repo/pull/21"}}
	m.ghActions = fake
	m.openLandingPane(v)
	// simulate the availability check having already resolved
	m.landing.ghAvailability = ghUp()
	m.landing.ghChecked = true

	next, cmd := m.updateLanding(tea.KeyPressMsg{Code: 'o', Text: "o"})
	m = next.(model)
	if cmd == nil {
		t.Fatal("the act key produced no command — the row's action must dispatch as a job")
	}
	if m.view != viewProgress {
		t.Fatalf("view after dispatching the action = %v, want viewProgress (the job/log plumbing)", m.view)
	}
	jk := landKey(v.scope, v.Name)
	snap, ok := m.jobs.snapshot(jk)
	if !ok {
		t.Fatal("no job was registered under the land key — the action must reuse jobs.go's registry")
	}
	if !strings.Contains(snap.Title, "Open draft PR") {
		t.Fatalf("job title = %q, want it to name the draft-PR action", snap.Title)
	}

	l := newTeaLoop(t, m)
	l.exec(cmd)
	l.pump("the land job to finish", func(m model) bool {
		s, ok := m.jobs.snapshot(jk)
		return ok && !s.Running()
	})
	final, _ := l.m.jobs.snapshot(jk)
	if final.Failed() {
		t.Fatalf("job failed: %v", final.Err)
	}
	if !strings.Contains(final.Output, "#21") {
		t.Fatalf("job output = %q, want it to name the created PR", final.Output)
	}
	if len(fake.createCalls) != 1 {
		t.Fatalf("CreateDraftPR calls = %+v, want exactly one", fake.createCalls)
	}
}

// --- Rendering: grouping is visible in the pane's own view ------------------

func TestLandingViewShowsWorktreeIndentedAfterItsRepo(t *testing.T) {
	m, v := landingTestVM(t, "web")
	if err := m.checkouts.Set(v.scope, v.Name, checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{
			{Path: "/home/user/repo", Kind: checkouts.KindRepo, Branch: "main", PushState: checkouts.PushStateNever},
			{Path: "/home/user/repo-wt", Kind: checkouts.KindWorktree, Parent: "/home/user/repo", Branch: "feature", PushState: checkouts.PushStateNever},
		},
	}); err != nil {
		t.Fatalf("seed checkouts: %v", err)
	}
	m.ghActions = &fakeGhActions{availability: landgh.Availability{}}
	m.openLandingPane(v)
	m.landing.ghChecked = true

	rendered := ansi.Strip(m.landingView())
	repoIdx := strings.Index(rendered, "/home/user/repo ")
	wtIdx := strings.Index(rendered, "/home/user/repo-wt")
	if repoIdx < 0 || wtIdx < 0 {
		t.Fatalf("rendered view is missing a row:\n%s", rendered)
	}
	if wtIdx < repoIdx {
		t.Fatalf("the worktree row must render AFTER its parent repo row:\n%s", rendered)
	}
	if !strings.Contains(rendered, "└─") {
		t.Fatalf("rendered view has no indentation marker for the nested worktree:\n%s", rendered)
	}
	if !strings.Contains(rendered, "gh: not installed") {
		t.Fatalf("rendered view = %q, want it to surface the gh-not-installed mode", rendered)
	}
}

func TestLandingViewNoCheckoutsYet(t *testing.T) {
	m, v := landingTestVM(t, "web")
	m.ghActions = &fakeGhActions{availability: ghUp()}
	m.openLandingPane(v)
	m.landing.ghChecked = true
	m.landing.ghAvailability = ghUp()

	rendered := ansi.Strip(m.landingView())
	if !strings.Contains(rendered, "No git checkouts discovered yet") {
		t.Fatalf("rendered view = %q, want the empty-registry hint", rendered)
	}
	if !strings.Contains(rendered, "gh: available") {
		t.Fatalf("rendered view = %q, want it to surface the gh-available mode", rendered)
	}
}

func TestLandingViewDetachedHeadLabel(t *testing.T) {
	m, v := landingTestVM(t, "web")
	if err := m.checkouts.Set(v.scope, v.Name, checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{
			{Path: "/home/user/repo", Kind: checkouts.KindRepo, Branch: "", PushState: checkouts.PushStateNever},
		},
	}); err != nil {
		t.Fatalf("seed checkouts: %v", err)
	}
	m.ghActions = &fakeGhActions{}
	m.openLandingPane(v)

	rendered := ansi.Strip(m.landingView())
	if !strings.Contains(rendered, "(detached)") {
		t.Fatalf("rendered view = %q, want a detached-HEAD label for an empty branch", rendered)
	}
}

func TestGhModeLabelAllModes(t *testing.T) {
	checking := landingPane{}
	if got := checking.ghModeLabel(); !strings.Contains(got, "checking") {
		t.Fatalf("ghModeLabel(unchecked) = %q, want it to say it is still checking", got)
	}
	available := landingPane{ghChecked: true, ghAvailability: ghUp()}
	if got := available.ghModeLabel(); !strings.Contains(got, "available") {
		t.Fatalf("ghModeLabel(available) = %q, want it to say gh is available", got)
	}
	// The two degraded modes are named separately and must NOT be
	// interchangeable: telling someone with gh installed that it is "not
	// installed" sends them to fix the wrong thing. The unauthenticated case is
	// the one a shell-alias credential injector (e.g. the 1Password gh plugin)
	// produces, since gh is exec'd argv-only and never through a shell.
	notInstalled := landingPane{ghChecked: true, ghAvailability: landgh.Availability{}}
	if got := notInstalled.ghModeLabel(); !strings.Contains(got, "not installed") {
		t.Fatalf("ghModeLabel(not installed) = %q, want it to say gh is not installed", got)
	}
	notAuthed := landingPane{ghChecked: true, ghAvailability: landgh.Availability{Installed: true}}
	got := notAuthed.ghModeLabel()
	if !strings.Contains(got, "not authenticated") {
		t.Fatalf("ghModeLabel(not authenticated) = %q, want it to say gh is not authenticated", got)
	}
	if strings.Contains(got, "not installed") {
		t.Fatalf("ghModeLabel(not authenticated) = %q, must not claim gh is missing when it is present", got)
	}
	if !strings.Contains(got, "gh auth login") {
		t.Fatalf("ghModeLabel(not authenticated) = %q, want it to name the fix", got)
	}
	// Every degraded mode must still say what happens instead.
	for name, p := range map[string]landingPane{"not installed": notInstalled, "not authenticated": notAuthed} {
		if !strings.Contains(p.ghModeLabel(), "compare URL") {
			t.Fatalf("ghModeLabel(%s) = %q, want it to name the browser fallback", name, p.ghModeLabel())
		}
	}
}

func TestStyleForLandRowEveryKind(t *testing.T) {
	kinds := []landRowKind{landRowLocalOnly, landRowAtRisk, landRowPushedNoPR, landRowPushedHasPR, landRowOtherForge, landRowNothingToLand}
	for _, k := range kinds {
		if got := styleForLandRow(k).Render("x"); ansi.Strip(got) != "x" {
			t.Fatalf("styleForLandRow(%v).Render(\"x\") stripped = %q, want \"x\"", k, ansi.Strip(got))
		}
	}
}

// TestClassifyLandRowNothingToLand pins the pane's half of the pristine-clone
// fix: a checkout parked on its repo's default branch offers NO action. The
// previous behaviour offered "Open draft PR", which would have asked GitHub to
// open a main -> main PR — a request it rejects outright.
func TestClassifyLandRowNothingToLand(t *testing.T) {
	c := checkouts.Checkout{
		Path: "/home/u/repo", Branch: "main", DefaultBranch: "main",
		PushState: checkouts.PushStatePushed, OrgRepo: "acme/repo", Forge: "github.com",
	}
	row := classifyLandRow(c, nil, false)
	if row.Kind != landRowNothingToLand {
		t.Fatalf("Kind = %v, want landRowNothingToLand", row.Kind)
	}
	if row.Action != landActionNone {
		t.Fatalf("Action = %v, want landActionNone", row.Action)
	}
	// Exact equality, which also pins that the row never picks up the
	// "(checking…)" suffix: it needs no gh lookup, so it must not claim to be
	// waiting on one.
	if row.Label != "nothing to land" {
		t.Fatalf("Label = %q, want %q", row.Label, "nothing to land")
	}

	// A feature branch in the same repo is unaffected.
	c.Branch = "feature"
	if got := classifyLandRow(c, nil, true); got.Kind != landRowPushedNoPR || got.Action != landActionOpenDraftPR {
		t.Fatalf("feature branch classified as %v/%v, want landRowPushedNoPR/landActionOpenDraftPR", got.Kind, got.Action)
	}
}

// TestNothingToLandRowsSkipTheGhLookup pins that a default-branch checkout
// costs no gh round trip: the pane already knows there is no branch-vs-trunk
// PR to find, so querying for one would spend a network call per clone.
func TestNothingToLandRowsSkipTheGhLookup(t *testing.T) {
	m, v := landingTestVM(t, "web")
	if err := m.checkouts.Set(v.scope, v.Name, checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{
			{Path: "/home/u/clone", Branch: "main", DefaultBranch: "main", PushState: checkouts.PushStatePushed, OrgRepo: "acme/repo", Forge: "github.com"},
			{Path: "/home/u/work", Branch: "feature", DefaultBranch: "main", PushState: checkouts.PushStatePushed, OrgRepo: "acme/repo", Forge: "github.com"},
		},
	}); err != nil {
		t.Fatalf("seed checkouts: %v", err)
	}
	fake := &fakeGhActions{availability: ghUp()}
	m.ghActions = fake
	m.openLandingPane(v)

	cmd := m.handleLandingAvailable(landingAvailableMsg{
		scope: v.scope, vm: v.Name,
		availability: ghUp(),
	})
	if cmd != nil {
		cmd() // drain the batch so the fake records its calls
	}
	for _, call := range fake.prStateCalls {
		if call.branch == "main" {
			t.Fatalf("a default-branch checkout triggered a gh PR lookup: %+v", fake.prStateCalls)
		}
	}
}

// TestGhModeLabelOnePasswordPath pins that a 1Password-plugin user gets advice
// that fits their setup: their gh works and their token is in the vault, so
// "run gh auth login" would send them to fix the wrong thing.
func TestGhModeLabelOnePasswordPath(t *testing.T) {
	p := landingPane{ghChecked: true, ghAvailability: landgh.Availability{Installed: true, ViaOnePassword: true}}
	got := p.ghModeLabel()
	if !strings.Contains(got, "1Password") {
		t.Fatalf("ghModeLabel = %q, want it to name 1Password", got)
	}
	if strings.Contains(got, "gh auth login") {
		t.Fatalf("ghModeLabel = %q, must not tell a 1Password user to re-auth gh itself", got)
	}
	if !strings.Contains(got, "compare URL") {
		t.Fatalf("ghModeLabel = %q, want it to name the browser fallback", got)
	}
}
