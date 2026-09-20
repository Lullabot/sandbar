// resetpreserve.go turns the host-side checkout registry — the same cached
// sweep the unlanded-work badge, the delete guard and the Landing pane read —
// into the list of directories the reset form offers to carry across a rebuild.
//
// It contacts NO guest. That is the whole reason it reads the registry rather
// than sweeping: the form opens on a key press, and the VM being reset may be
// stopped (a reset starts it later, only once something has actually been
// selected to preserve). What the registry holds is what the last sweep of that
// VM saw, which is why every row's help names how long ago that was — the same
// "(as of <ago>)" honesty the delete guard applies to the same data.
//
// A row that has since been deleted inside the guest is not a problem this file
// has to solve: the reset probes each selected path against the live guest
// before it stages anything, and says so instead of failing (see planExtras in
// internal/provision).
package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lullabot/sandbar/internal/checkouts"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/registry"
)

// resetPreserveMaxRows caps how many checkout rows the reset form lists.
//
// The form is a single non-scrolling column that already carries eleven inputs,
// and the sweep will happily report up to fifty checkouts — a VM with a
// dependency tree full of vendored repos would push the footer, which is the
// only statement of how to save or leave, off the bottom of an 80x24 terminal.
// Eight is comfortably more than the "1-3 VMs, a repo or two each" this tool is
// designed around, and going over it is reported rather than silently dropped
// (see resetPreserveCandidates's overflow count), with the whole-home option as
// the answer for a VM that genuinely holds more.
const resetPreserveMaxRows = 8

// resetCheckout is one selectable row: a git checkout or linked worktree the
// last sweep found inside the VM being reset.
type resetCheckout struct {
	// path is the checkout's absolute path in the guest, exactly as the sweep
	// recorded it. It is what reaches provision.ResetOptions.PreservePaths — the
	// label below is for the eye only, and is derived from a GUESS at the guest
	// home (see resetPreserveCandidates), which must never be what gets acted on.
	path string

	// label is the display form: "~/src/app" when path sits under the guessed
	// home, otherwise the absolute path unchanged.
	label string

	// help is the row's one-paragraph explanation, shown while it has focus.
	help string

	// selected is the toggle's state.
	selected bool
}

// resetPreserveCandidates builds the rows the reset form offers, from the
// checkout registry's cached view of this VM, and returns how many further
// checkouts the row cap left out.
//
// Checkouts inside the project's own per-org directory are left out: the
// "Preserve ~/<host>/<org>" toggle already covers that whole directory, and
// offering the checkout a second time would mean two rows that stage the same
// bytes. (The reset de-duplicates overlapping paths anyway — pruneCovered — but
// a form that offers the same thing twice is confusing before it is wasteful.)
//
// Nothing else is filtered, deliberately. A worktree that lives OUTSIDE the
// guest home cannot be preserved, but the home here is only a guess from the
// configured user name, so hiding rows on the strength of it would silently drop
// exactly the unusual checkout a user most wants to keep. A selected path that
// turns out to be outside the home is refused by the reset before the VM is
// touched, which loses nothing and says why.
//
// The overflow count is returned beside the rows, not computed by a second
// function over the same registry: a count that can disagree with the list it
// describes is worse than no count at all.
func resetPreserveCandidates(reg *checkouts.Registry, scope registry.Scope, name, user, cloneURL string, now time.Time) ([]resetCheckout, int) {
	if reg == nil {
		return nil, 0
	}
	vc, ok := reg.Get(scope, name)
	if !ok || len(vc.Checkouts) == 0 {
		return nil, 0
	}

	home := guessGuestHome(user)
	covered := ""
	if orgRel, ok := provision.OrgRelDir(cloneURL); ok && home != "" {
		covered = home + "/" + orgRel
	}

	var rows []resetCheckout
	for _, c := range vc.Checkouts {
		if c.Path == "" {
			continue
		}
		if covered != "" && (c.Path == covered || strings.HasPrefix(c.Path, covered+"/")) {
			continue
		}
		rows = append(rows, resetCheckout{
			path:  c.Path,
			label: "Preserve " + shortenGuestPath(c.Path, home),
			help:  checkoutPreserveHelp(c, vc.SweptAt, now),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].path < rows[j].path })

	overflow := 0
	if len(rows) > resetPreserveMaxRows {
		overflow = len(rows) - resetPreserveMaxRows
		rows = rows[:resetPreserveMaxRows]
	}
	return rows, overflow
}

// guessGuestHome is the conventional home for a guest user, used ONLY to shorten
// a path for display and to spot the project's own org directory. Every guest
// sand builds is a Debian cloud image where /home/<user> is where the user role
// puts the account, so the guess is right in practice — but it is a guess, and
// the reset itself resolves the real home from the guest's passwd entry rather
// than trusting this.
func guessGuestHome(user string) string {
	if user == "" {
		return ""
	}
	return "/home/" + user
}

// shortenGuestPath renders an absolute guest path as "~/rest" when it is under
// home, and unchanged when it is not — so a checkout somewhere unexpected reads
// as unexpected instead of being disguised.
func shortenGuestPath(p, home string) string {
	if home != "" && strings.HasPrefix(p, home+"/") {
		return "~/" + strings.TrimPrefix(p, home+"/")
	}
	return p
}

// checkoutPreserveHelp is one row's focused help: what the toggle copies, what
// state that checkout was last seen in, and how stale that observation is.
func checkoutPreserveHelp(c checkouts.Checkout, sweptAt, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Copies %s out of the VM to this host and back after the rebuild.", c.Path)
	if c.Kind == checkouts.KindWorktree {
		b.WriteString(" A linked worktree.")
	}
	if c.Branch != "" {
		fmt.Fprintf(&b, " On %s", c.Branch)
		// LocalOnly, not Ahead: this row is telling the user what a rebuild
		// would destroy if they left the toggle off, and only LocalOnly
		// counts commits that exist nowhere else. Ahead measures against one
		// possibly-stale ref by hash, so a rebased branch would claim
		// thousands of commits at stake that are safely published upstream —
		// see checkouts.Checkout.LocalOnly.
		switch {
		case c.Dirty > 0 && c.LocalOnly > 0:
			fmt.Fprintf(&b, ", %d uncommitted file(s) and %d unpushed commit(s).", c.Dirty, c.LocalOnly)
		case c.Dirty > 0:
			fmt.Fprintf(&b, ", %d uncommitted file(s).", c.Dirty)
		case c.LocalOnly > 0:
			fmt.Fprintf(&b, ", %d unpushed commit(s).", c.LocalOnly)
		default:
			b.WriteString(".")
		}
	}
	// formatAgo already carries its own "ago" (and says "just now" under a
	// minute), so this must not add one.
	if !sweptAt.IsZero() {
		fmt.Fprintf(&b, " Last seen %s.", formatAgo(now.Sub(sweptAt)))
	}
	return b.String()
}
