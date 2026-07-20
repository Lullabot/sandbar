package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/lullabot/sandbar/internal/checkouts"
	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"
)

// --- deleteGuardExtra: the pure copy-composition function ------------------

func TestDeleteGuardExtraNothingWhenNotFound(t *testing.T) {
	if got := deleteGuardExtra(checkouts.VMCheckouts{}, false); got != "" {
		t.Fatalf("deleteGuardExtra(not found) = %q, want \"\" (today's copy unchanged)", got)
	}
}

func TestDeleteGuardExtraNothingWhenEmpty(t *testing.T) {
	vc := checkouts.VMCheckouts{Checkouts: nil}
	if got := deleteGuardExtra(vc, true); got != "" {
		t.Fatalf("deleteGuardExtra(no checkouts) = %q, want \"\"", got)
	}
}

func TestDeleteGuardExtraUnpushedOnly(t *testing.T) {
	vc := checkouts.VMCheckouts{Checkouts: []checkouts.Checkout{
		{Path: "/home/user/repo", PushState: checkouts.PushStateUnpushed, Ahead: 3},
	}}
	got := deleteGuardExtra(vc, true)
	want := "3 unpushed commits (only in this VM — lost on delete)."
	if got != want {
		t.Fatalf("deleteGuardExtra(unpushed-only) = %q, want %q", got, want)
	}
}

func TestDeleteGuardExtraSingleUnpushedCommitIsSingular(t *testing.T) {
	vc := checkouts.VMCheckouts{Checkouts: []checkouts.Checkout{
		{Path: "/home/user/repo", PushState: checkouts.PushStateUnpushed, Ahead: 1},
	}}
	got := deleteGuardExtra(vc, true)
	want := "1 unpushed commit (only in this VM — lost on delete)."
	if got != want {
		t.Fatalf("deleteGuardExtra(1 unpushed commit) = %q, want %q", got, want)
	}
}

func TestDeleteGuardExtraDirtyOnly(t *testing.T) {
	// A checkout whose commits are all safely on the forge but whose working
	// tree is dirty: the ONLY thing at risk is the uncommitted work, so that
	// is the only thing in the "lost" clause — with the pushed branch reported
	// separately as safe.
	//
	// This case used to be written with PushStateNever, and asserted that a
	// never-pushed branch contributed NOTHING to the lost clause. That was
	// reasoning from the implementation rather than from what is true: such a
	// branch's commits exist nowhere but the VM. See
	// TestDeleteGuardExtraNeverPushedCleanIsWarned.
	vc := checkouts.VMCheckouts{Checkouts: []checkouts.Checkout{
		{Path: "/home/user/repo", Branch: "feature", PushState: checkouts.PushStatePushed, Dirty: 2},
	}}
	got := deleteGuardExtra(vc, true)
	want := "uncommitted changes (only in this VM — lost on delete); 1 branch pushed without a PR (safe on GitHub)."
	if got != want {
		t.Fatalf("deleteGuardExtra(dirty-only) = %q, want %q", got, want)
	}
}

// TestDeleteGuardExtraNeverPushedAndDirtyNamesBoth is the case the old
// dirty-only test actually held: a never-pushed branch WITH uncommitted
// changes has two distinct things at risk, and must name both.
func TestDeleteGuardExtraNeverPushedAndDirtyNamesBoth(t *testing.T) {
	vc := checkouts.VMCheckouts{Checkouts: []checkouts.Checkout{
		{Path: "/home/user/repo", Branch: "feature", PushState: checkouts.PushStateNever, Dirty: 2},
	}}
	got := deleteGuardExtra(vc, true)
	want := "1 never-pushed branch + uncommitted changes (only in this VM — lost on delete)."
	if got != want {
		t.Fatalf("deleteGuardExtra() = %q, want %q", got, want)
	}
}

func TestDeleteGuardExtraBoth(t *testing.T) {
	// Aggregated across two checkouts: one with unpushed commits, one that is
	// never-pushed AND dirty. All three quantities fold into the single "lost
	// on delete" clause.
	//
	// This expectation used to omit the never-pushed branch entirely — it read
	// "3 unpushed commits + uncommitted changes" — which is exactly the bug
	// TestDeleteGuardExtraNeverPushedCleanIsWarned covers: repo-b's branch was
	// invisible to the guard and only its dirtiness showed. The fixture pairs
	// the two states precisely because that pairing is what hid the miscount.
	vc := checkouts.VMCheckouts{Checkouts: []checkouts.Checkout{
		{Path: "/home/user/repo-a", PushState: checkouts.PushStateUnpushed, Ahead: 3},
		{Path: "/home/user/repo-b", PushState: checkouts.PushStateNever, Dirty: 1},
	}}
	got := deleteGuardExtra(vc, true)
	want := "3 unpushed commits + 1 never-pushed branch + uncommitted changes (only in this VM — lost on delete)."
	if got != want {
		t.Fatalf("deleteGuardExtra(both) = %q, want %q", got, want)
	}
}

func TestDeleteGuardExtraPushedNoPRIsSafe(t *testing.T) {
	vc := checkouts.VMCheckouts{Checkouts: []checkouts.Checkout{
		{Path: "/home/user/repo", PushState: checkouts.PushStatePushed},
	}}
	got := deleteGuardExtra(vc, true)
	want := "1 branch pushed without a PR (safe on GitHub)."
	if got != want {
		t.Fatalf("deleteGuardExtra(pushed-no-PR) = %q, want %q", got, want)
	}
}

func TestDeleteGuardExtraMultiplePushedIsPlural(t *testing.T) {
	vc := checkouts.VMCheckouts{Checkouts: []checkouts.Checkout{
		{Path: "/home/user/repo-a", PushState: checkouts.PushStatePushed},
		{Path: "/home/user/repo-b", PushState: checkouts.PushStatePushed},
	}}
	got := deleteGuardExtra(vc, true)
	want := "2 branches pushed without a PR (safe on GitHub)."
	if got != want {
		t.Fatalf("deleteGuardExtra(2 pushed) = %q, want %q", got, want)
	}
}

func TestDeleteGuardExtraLostAndSafeTogether(t *testing.T) {
	vc := checkouts.VMCheckouts{Checkouts: []checkouts.Checkout{
		{Path: "/home/user/repo-a", PushState: checkouts.PushStateUnpushed, Ahead: 3, Dirty: 1},
		{Path: "/home/user/repo-b", PushState: checkouts.PushStatePushed},
	}}
	got := deleteGuardExtra(vc, true)
	want := "3 unpushed commits + uncommitted changes (only in this VM — lost on delete); 1 branch pushed without a PR (safe on GitHub)."
	if got != want {
		t.Fatalf("deleteGuardExtra(lost+safe) = %q, want %q", got, want)
	}
}

// --- deleteGuardPrompt: base prompt + extra + stopped-VM as-of label -------

func TestDeleteGuardPromptUnchangedWhenNothingNotable(t *testing.T) {
	got := deleteGuardPrompt("claude", checkouts.VMCheckouts{}, false, true, time.Now())
	want := `Delete "claude"?`
	if got != want {
		t.Fatalf("deleteGuardPrompt(nothing) = %q, want %q (today's copy unchanged)", got, want)
	}
}

func TestDeleteGuardPromptRunningVMHasNoAsOfLabel(t *testing.T) {
	vc := checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{{PushState: checkouts.PushStateUnpushed, Ahead: 1}},
		SweptAt:   time.Now().Add(-2 * time.Hour),
	}
	got := deleteGuardPrompt("claude", vc, true, true /* running */, time.Now())
	if strings.Contains(got, "as of") {
		t.Fatalf("deleteGuardPrompt(running VM) = %q, must not carry an \"as of\" label", got)
	}
}

func TestDeleteGuardPromptStoppedVMLabelsAsOfLastSeen(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	vc := checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{{PushState: checkouts.PushStateUnpushed, Ahead: 1}},
		SweptAt:   now.Add(-2 * time.Hour),
	}
	got := deleteGuardPrompt("claude", vc, true, false /* stopped */, now)
	want := `Delete "claude"? 1 unpushed commit (only in this VM — lost on delete). (as of 2h ago)`
	if got != want {
		t.Fatalf("deleteGuardPrompt(stopped VM) = %q, want %q", got, want)
	}
}

// --- The security invariant: zero guest contact on the delete-confirm path -

// fatalOnGuestContact is a lima.Runner whose every method fails the test the
// instant it is called. Wiring it under the model proves — dynamically, not
// just by code inspection — that raising the delete confirmation (pressing
// 'd') never executes anything inside the guest: if it did, one of these
// methods would fire and the test would fail loudly rather than silently
// pass. This is the primary acceptance signal for the delete guard's hard
// boundary (deleteguard.go) and for the "delete guard drifting into guest
// contact" security risk it exists to close.
type fatalOnGuestContact struct{ t *testing.T }

func (r fatalOnGuestContact) Output(context.Context, ...string) ([]byte, error) {
	r.t.Helper()
	r.t.Fatal("delete-confirmation path executed a guest command via Output — zero guest contact was violated")
	return nil, nil
}

func (r fatalOnGuestContact) Stream(context.Context, io.Reader, io.Writer, ...string) error {
	r.t.Helper()
	r.t.Fatal("delete-confirmation path executed a guest command via Stream — zero guest contact was violated")
	return nil
}

func (r fatalOnGuestContact) StreamOut(context.Context, io.Reader, io.Writer, ...string) error {
	r.t.Helper()
	r.t.Fatal("delete-confirmation path executed a guest command via StreamOut — zero guest contact was violated")
	return nil
}

// TestDeleteGuardRefreshesARunningVM drives the REAL 'd' verb (commandreg.go)
// over a RUNNING VM whose cached checkout registry has plenty to warn about,
// and pins the freshness behaviour: the overlay is raised immediately from
// cache (so the UI never stalls), a re-read is dispatched, and Confirm is HELD
// until that re-read lands.
//
// The re-read exists because a running VM's cached entry can be a whole
// sweepInterval stale — precisely the window in which someone edits a file and
// then reaches for delete. It runs the same read-only pass that VM's own sweep
// loop is already running, and only ever against a VM that is already up; see
// TestDeleteGuardNeverSweepsAStoppedVM for the half of the invariant that did
// NOT change.
func TestDeleteGuardRefreshesARunningVM(t *testing.T) {
	m := newTestModel(t)
	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: "Running", CPUs: 2})

	if err := m.checkouts.Set(registry.LocalScope, "claude", checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{
			{Path: "/home/user/repo", PushState: checkouts.PushStateUnpushed, Ahead: 3, Dirty: 1},
		},
	}); err != nil {
		t.Fatalf("seed checkout registry: %v", err)
	}

	m, cmd := pressDispatch(t, m, runeKey('d'))
	if m.confirm == nil {
		t.Fatal("pressing 'd' should raise the confirm overlay")
	}
	if cmd == nil {
		t.Fatal("a running VM's delete confirm should dispatch the freshness re-read")
	}
	if !m.confirm.checking {
		t.Fatal("the confirm should be marked as still checking while the re-read is in flight")
	}
	// The overlay is populated from cache straight away rather than waiting.
	want := `Delete "claude"? 3 unpushed commits + uncommitted changes (only in this VM — lost on delete).`
	if m.confirm.prompt != want {
		t.Fatalf("confirm prompt = %q, want %q", m.confirm.prompt, want)
	}
	// ...and it says so, instead of looking like a dead 'y' key.
	wide := resized(m, 160, 40)
	if !strings.Contains(ansi.Strip(wide.confirmView()), "checking for recent changes") {
		t.Fatalf("confirm overlay should say it is still checking, got %q", ansi.Strip(wide.confirmView()))
	}

	// Confirm is REFUSED while the check is in flight: answering now would be
	// answering a question that is about to change.
	after, cmd2 := m.Update(runeKey('y'))
	m2 := after.(model)
	if m2.confirm == nil {
		t.Fatal("'y' during the freshness check must not dismiss the overlay")
	}
	if cmd2 != nil {
		t.Fatal("'y' during the freshness check must not dispatch the delete")
	}
	// Cancel still works throughout — the hold must never trap the user.
	after, _ = m2.Update(runeKey('n'))
	if after.(model).confirm != nil {
		t.Fatal("'n' must cancel even while the freshness check is in flight")
	}
}

// TestDeleteGuardRefreshFolds pins what the landed re-read does: it rewrites
// the prompt from the FRESH data and releases Confirm. The seeded cache says
// there is nothing at risk; the refresh finds uncommitted work — exactly the
// stale-cache case the re-read exists for.
func TestDeleteGuardRefreshFolds(t *testing.T) {
	m := newTestModel(t)
	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: "Running", CPUs: 2})
	if err := m.checkouts.Set(registry.LocalScope, "claude", checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{{Path: "/home/user/repo", PushState: checkouts.PushStatePushed}},
	}); err != nil {
		t.Fatalf("seed checkout registry: %v", err)
	}
	m, _ = pressDispatch(t, m, runeKey('d'))

	m.handleDeleteGuardRefresh(deleteGuardRefreshMsg{
		scope: registry.LocalScope, vm: "claude",
		vc: checkouts.VMCheckouts{Checkouts: []checkouts.Checkout{
			{Path: "/home/user/repo", PushState: checkouts.PushStateUnpushed, Ahead: 2, Dirty: 5},
		}},
	})
	if m.confirm.checking {
		t.Fatal("the landed re-read must release the Confirm hold")
	}
	if !strings.Contains(m.confirm.prompt, "2 unpushed") {
		t.Fatalf("prompt = %q, want it rewritten from the FRESH data", m.confirm.prompt)
	}
	// The refresh is a real sweep result, so it lands in the shared registry
	// rather than being a private read only the prompt saw.
	vc, _ := m.checkouts.Get(registry.LocalScope, "claude")
	if len(vc.Checkouts) != 1 || vc.Checkouts[0].Dirty != 5 {
		t.Fatalf("registry not updated from the refresh: %+v", vc)
	}
}

// TestDeleteGuardRefreshFailureKeepsCacheAndSaysSo pins the degradation path:
// a guest that will not answer a read-only sweep is exactly the one whose
// cached picture deserves suspicion, so the guard keeps what it had but must
// not present it as a live answer.
func TestDeleteGuardRefreshFailureKeepsCacheAndSaysSo(t *testing.T) {
	m := newTestModel(t)
	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: "Running", CPUs: 2})
	m, _ = pressDispatch(t, m, runeKey('d'))
	before := m.confirm.prompt

	m.handleDeleteGuardRefresh(deleteGuardRefreshMsg{
		scope: registry.LocalScope, vm: "claude", err: errors.New("guest went away"),
	})
	if m.confirm.checking {
		t.Fatal("a failed re-read must still release the Confirm hold, or the overlay traps the user")
	}
	if !strings.Contains(m.confirm.prompt, "could not re-check") {
		t.Fatalf("prompt = %q, want it to admit the re-check did not land", m.confirm.prompt)
	}
	if !strings.HasPrefix(m.confirm.prompt, before) {
		t.Fatalf("prompt = %q, want the cached answer retained ahead of the caveat", m.confirm.prompt)
	}
}

// TestDeleteGuardRefreshIgnoresStaleResults pins the (scope, vm) guard: a
// re-read landing after the user moved on must not rewrite whatever
// confirmation is on screen now.
func TestDeleteGuardRefreshIgnoresStaleResults(t *testing.T) {
	m := newTestModel(t)
	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: "Running", CPUs: 2})
	m, _ = pressDispatch(t, m, runeKey('d'))
	before := m.confirm.prompt

	m.handleDeleteGuardRefresh(deleteGuardRefreshMsg{
		scope: registry.LocalScope, vm: "some-other-vm",
		vc: checkouts.VMCheckouts{Checkouts: []checkouts.Checkout{
			{Path: "/x", PushState: checkouts.PushStateUnpushed, Ahead: 99},
		}},
	})
	if m.confirm.prompt != before {
		t.Fatalf("a result for another VM rewrote this prompt: %q", m.confirm.prompt)
	}
	if !m.confirm.checking {
		t.Fatal("a stale result must not release this confirmation's hold")
	}
}

// TestDeleteGuardNoGuestContactStoppedVM repeats the same proof for a
// stopped VM (the "as of" last-seen path), since that is a second, distinct
// branch through deleteGuardPrompt that must equally never touch the guest.
func TestDeleteGuardNoGuestContactStoppedVM(t *testing.T) {
	cli := lima.New(fatalOnGuestContact{t: t})
	m := newTestModelWithCli(t, cli)

	m = putOnBoard(t, m, vm.VM{Name: "claude", Status: "Stopped", CPUs: 2})

	sweptAt := time.Now().Add(-3 * time.Hour)
	if err := m.checkouts.Set(registry.LocalScope, "claude", checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{
			{Path: "/home/user/repo", PushState: checkouts.PushStatePushed},
		},
		SweptAt: sweptAt,
	}); err != nil {
		t.Fatalf("seed checkout registry: %v", err)
	}

	m, _ = pressDispatch(t, m, runeKey('d'))
	if m.confirm == nil {
		t.Fatal("pressing 'd' should raise the confirm overlay even for a stopped VM")
	}
	if !strings.Contains(m.confirm.prompt, "safe on GitHub") {
		t.Fatalf("confirm prompt = %q, want it to mention the pushed branch", m.confirm.prompt)
	}
	if !strings.Contains(m.confirm.prompt, "as of") {
		t.Fatalf("confirm prompt = %q, want a stopped-VM \"as of\" label", m.confirm.prompt)
	}
}

// TestDeleteGuardCountsDefaultBranchCheckouts pins the deliberate asymmetry
// between this guard and the tile badge / Landing pane. Those two suppress a
// checkout sitting on its repo's default branch (checkouts.NothingToLand) —
// there is nothing to turn into a PR. The guard must NOT: it answers "what is
// about to be destroyed", and a pristine clone with uncommitted work in it is
// exactly the case a user would not expect to lose silently.
func TestDeleteGuardCountsDefaultBranchCheckouts(t *testing.T) {
	vc := checkouts.VMCheckouts{
		Checkouts: []checkouts.Checkout{
			{Path: "/home/u/clone", Branch: "main", DefaultBranch: "main", PushState: checkouts.PushStatePushed},
		},
	}
	if got := deleteGuardExtra(vc, true); got == "" {
		t.Fatal("a default-branch checkout must still be named by the delete guard")
	}

	// And uncommitted work on that same default branch must still be called
	// out as lost — NothingToLand answers only the actionable question.
	vc.Checkouts[0].Dirty = 4
	got := deleteGuardExtra(vc, true)
	if !strings.Contains(got, "uncommitted") {
		t.Fatalf("guard = %q, want it to name the uncommitted changes on the default branch", got)
	}
}

// settleDeleteGuard completes a running VM's in-flight delete-guard re-read
// using whatever the registry already holds, for tests whose subject is the
// settled overlay rather than the check itself.
func settleDeleteGuard(m model) model {
	if m.confirm == nil || !m.confirm.checking {
		return m
	}
	vc, _ := m.checkouts.Get(m.confirm.scope, m.confirm.vmName)
	m.handleDeleteGuardRefresh(deleteGuardRefreshMsg{scope: m.confirm.scope, vm: m.confirm.vmName, vc: vc})
	return m
}

// TestDeleteGuardExtraNeverPushedCleanIsWarned is the regression for a guard
// that stayed SILENT about the work it exists to protect.
//
// PushStateNever used to be summed into the unpushed-COMMIT total via
// Checkout.Ahead — which is defined 0 for a never-pushed branch, since there
// is no tracking ref to count against. So a clean, committed, never-pushed
// branch contributed nothing to any counter, every clause came out empty, and
// deleteGuardExtra returned "": deleting that VM destroyed the commits with no
// warning whatsoever. It looked fine in testing only because such a checkout
// is usually dirty too, and the dirty clause masked the miscount — so this
// test deliberately keeps Dirty at 0.
func TestDeleteGuardExtraNeverPushedCleanIsWarned(t *testing.T) {
	vc := checkouts.VMCheckouts{Checkouts: []checkouts.Checkout{
		{Path: "/home/u/repo", Branch: "feature", PushState: checkouts.PushStateNever},
	}}
	got := deleteGuardExtra(vc, true)
	if got == "" {
		t.Fatal("a committed, never-pushed branch must be named by the delete guard — it exists nowhere else")
	}
	if !strings.Contains(got, "never-pushed") {
		t.Fatalf("guard = %q, want it to name the never-pushed branch", got)
	}
	if !strings.Contains(got, "lost on delete") {
		t.Fatalf("guard = %q, want it in the lost-on-delete clause, not the safe-on-GitHub one", got)
	}
	// Counted as branches, not commits: there is no honest commit count.
	if strings.Contains(got, "0 ") {
		t.Fatalf("guard = %q, must not report a fabricated zero count", got)
	}
}

// TestDeleteGuardExtraNeverPushedCountsBranches pins the pluralization and
// that never-pushed branches and unpushed commits are reported as the separate
// quantities they are, rather than being added together.
func TestDeleteGuardExtraNeverPushedCountsBranches(t *testing.T) {
	two := checkouts.VMCheckouts{Checkouts: []checkouts.Checkout{
		{Path: "/a", PushState: checkouts.PushStateNever},
		{Path: "/b", PushState: checkouts.PushStateNever},
	}}
	if got := deleteGuardExtra(two, true); !strings.Contains(got, "2 never-pushed branches") {
		t.Fatalf("guard = %q, want %q", got, "2 never-pushed branches")
	}

	mixed := checkouts.VMCheckouts{Checkouts: []checkouts.Checkout{
		{Path: "/a", PushState: checkouts.PushStateNever},
		{Path: "/b", PushState: checkouts.PushStateUnpushed, Ahead: 3},
	}}
	got := deleteGuardExtra(mixed, true)
	if !strings.Contains(got, "3 unpushed commits") {
		t.Fatalf("guard = %q, want the unpushed commit count preserved", got)
	}
	if !strings.Contains(got, "1 never-pushed branch") {
		t.Fatalf("guard = %q, want the never-pushed branch named alongside it", got)
	}
}
