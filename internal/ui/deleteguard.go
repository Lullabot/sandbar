package ui

// deleteguard.go extends the `d` (delete) confirmation copy with a warning
// about work the target VM's CACHED checkout registry (internal/checkouts,
// plan 17 task 1) shows living only inside that VM — without ever touching
// the guest to check. See plan 17, Component 3 and the "Delete guard
// drifting into guest contact" security risk: a "smarter" guard that
// refreshed state at delete time would execute in a possibly-compromised
// VM, which is exactly the thing deleting it is meant to let you avoid.
//
// The functions below are therefore deliberately PURE: every one of them
// takes only data the caller already read out of *checkouts.Registry (a
// plain value, no registry handle, no shell, no interface capable of I/O of
// any kind) and returns a string. There is nothing here for a caller to
// misuse into a guest round-trip. deleteguard_test.go's
// TestDeleteGuardNoGuestContact drives the real confirm-raising code path
// (commandreg.go's delete verb) with a Runner that fails the test the moment
// any of its methods are called, and proves it is never invoked.
import (
	"fmt"
	"strings"
	"time"

	"github.com/lullabot/sandbar/internal/checkouts"
)

// deleteGuardExtra composes the confirmation-copy fragment naming any work in
// vc that would be lost on delete or is already safe on GitHub, aggregated
// across every checkout the registry recorded for the VM:
//
//   - "lost on delete": unpushed commits (checkouts whose PushState is
//     Unpushed or Never — see checkouts.PushState's doc on why Never rows
//     contribute no numeric commit count) plus, independently, any checkout
//     with uncommitted changes (Dirty > 0) — dirty files are by definition
//     uncommitted, so they are "only in this VM" regardless of whether that
//     checkout's own branch has otherwise been pushed.
//   - "safe on GitHub": checkouts whose PushState is Pushed. This registry
//     (task 1) carries no PR-existence field — that lazy, authoritative check
//     is Component 4/task 6-7, out of scope here — so, matching task 4's
//     badge ("until PR state resolves, reflect push state alone"), every
//     pushed checkout is counted here: it cannot be lost, whether or not a PR
//     already exists for it.
//
// found is the registry's own "do we have an entry at all" bool (Registry.Get's
// second return) — kept separate from an empty Checkouts slice so a never-swept
// VM and a swept-but-checkout-free VM both correctly yield "" (today's plain
// prompt, unchanged) without the caller having to special-case either.
//
// Returns "" when there is nothing worth naming, which is what leaves the
// existing "Delete %q?" prompt exactly as it renders today (see
// TestConfirmDeletePromptHasNoRecreateBranch's exact-string assertion).
func deleteGuardExtra(vc checkouts.VMCheckouts, found bool) string {
	if !found {
		return ""
	}

	var unpushedCommits int
	var dirty bool
	var pushedCount int
	for _, c := range vc.Checkouts {
		switch c.PushState {
		case checkouts.PushStateUnpushed, checkouts.PushStateNever:
			unpushedCommits += c.Ahead
		case checkouts.PushStatePushed:
			// DELIBERATELY not filtered through Checkout.NothingToLand, unlike
			// the tile badge and the Landing pane. Those two answer "is there
			// something here worth turning into a PR", so a pristine clone is
			// noise to them. This guard answers a different question — "what is
			// about to be destroyed" — and a checkout on the default branch is
			// still a real checkout the user may not expect to lose. Applying
			// the predicate here would quietly weaken the guard.
			pushedCount++
		}
		if c.Dirty > 0 {
			dirty = true
		}
	}

	var lost []string
	if unpushedCommits > 0 {
		lost = append(lost, fmt.Sprintf("%d unpushed %s", unpushedCommits, pluralize(unpushedCommits, "commit", "commits")))
	}
	if dirty {
		lost = append(lost, "uncommitted changes")
	}

	var clauses []string
	if len(lost) > 0 {
		clauses = append(clauses, strings.Join(lost, " + ")+" (only in this VM — lost on delete)")
	}
	if pushedCount > 0 {
		clauses = append(clauses, fmt.Sprintf(
			"%d %s pushed without a PR (safe on GitHub)",
			pushedCount, pluralize(pushedCount, "branch", "branches")))
	}

	if len(clauses) == 0 {
		return ""
	}
	return strings.Join(clauses, "; ") + "."
}

// pluralize returns singular when n == 1, plural otherwise.
func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// deleteGuardPrompt is the full delete-confirmation prompt for name: the
// existing base `Delete %q?` (unchanged when deleteGuardExtra finds nothing),
// plus that fragment when there is one. running is whether the VM is
// currently Lima-reported as Running — for a VM that is NOT (a stopped VM,
// whose registry entry can only reflect a past sweep, since a stopped guest
// cannot be swept), the fragment is suffixed with an "(as of <ago>)" label
// keyed off vc.SweptAt, formatted with the same formatAgo helper the tile
// footer uses for "last used 3d ago" — so the warning is never mistaken for a
// live read. now is injected (mirrors tileFooterLine's now parameter) so
// tests are deterministic.
//
// This performs no I/O: it only touches the values the caller passes in,
// which the caller (commandreg.go's delete verb) already read from
// *checkouts.Registry.Get — a pure, mutex-guarded, in-memory host read.
func deleteGuardPrompt(name string, vc checkouts.VMCheckouts, found, running bool, now time.Time) string {
	base := fmt.Sprintf("Delete %q?", name)
	extra := deleteGuardExtra(vc, found)
	if extra == "" {
		return base
	}
	if !running {
		extra += fmt.Sprintf(" (as of %s)", formatAgo(now.Sub(vc.SweptAt)))
	}
	return base + " " + extra
}
