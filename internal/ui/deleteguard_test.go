package ui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

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
	// A checkout that has never been pushed (Ahead is always 0 for
	// PushStateNever, per the Checkout doc) but has uncommitted changes:
	// only the dirty half of the "lost" clause should appear, and — because
	// this row is not PushStatePushed — no "safe on GitHub" clause either.
	vc := checkouts.VMCheckouts{Checkouts: []checkouts.Checkout{
		{Path: "/home/user/repo", PushState: checkouts.PushStateNever, Dirty: 2},
	}}
	got := deleteGuardExtra(vc, true)
	want := "uncommitted changes (only in this VM — lost on delete)."
	if got != want {
		t.Fatalf("deleteGuardExtra(dirty-only) = %q, want %q", got, want)
	}
}

func TestDeleteGuardExtraBoth(t *testing.T) {
	// Aggregated across two checkouts: one with unpushed commits, one with
	// uncommitted changes only. Both fold into the single "lost on delete"
	// clause.
	vc := checkouts.VMCheckouts{Checkouts: []checkouts.Checkout{
		{Path: "/home/user/repo-a", PushState: checkouts.PushStateUnpushed, Ahead: 3},
		{Path: "/home/user/repo-b", PushState: checkouts.PushStateNever, Dirty: 1},
	}}
	got := deleteGuardExtra(vc, true)
	want := "3 unpushed commits + uncommitted changes (only in this VM — lost on delete)."
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
// pass. This is the primary acceptance signal for this task (plan 17,
// Component 3's "hard boundary" and the "Delete guard drifting into guest
// contact" security risk).
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

// TestDeleteGuardNoGuestContact drives the REAL 'd' verb (commandreg.go),
// over a VM whose cached checkout registry has plenty to warn about (so the
// guard's copy-composition path actually runs, not a no-op early return),
// and asserts the confirmation is raised with the expected warning — all
// while every guest-reaching Runner method would fail the test if invoked.
// The test passing IS the proof: had the guard done anything but read
// m.checkouts (a mutex-guarded, host-only, in-memory value), one of
// fatalOnGuestContact's methods would have fired.
func TestDeleteGuardNoGuestContact(t *testing.T) {
	cli := lima.New(fatalOnGuestContact{t: t})
	m := newTestModelWithCli(t, cli)

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
	if cmd != nil {
		// Raising the overlay must not itself dispatch anything — the actual
		// delete only runs once the user confirms with 'y'. This also means
		// deleteCmd's closure (which WOULD call the provider's Delete) is
		// never invoked by this test either.
		t.Fatal("raising the delete confirm overlay must not itself dispatch a command")
	}
	want := `Delete "claude"? 3 unpushed commits + uncommitted changes (only in this VM — lost on delete).`
	if m.confirm.prompt != want {
		t.Fatalf("confirm prompt = %q, want %q", m.confirm.prompt, want)
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
