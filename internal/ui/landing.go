package ui

// landing.go is the Landing pane (plan 17, Component 4 / task 7): a per-VM
// pull-request cockpit opened for a focused, RUNNING VM. It reads the
// checkout registry's cached rows (task 1's sweep), groups worktrees under
// their parent repo, and — lazily, on open — reconciles each pushed GitHub
// branch's row against the AUTHORITATIVE host-side gh check (task 6's
// PRState), since the sweep's own push-state is a cheap local heuristic (see
// checkouts.PushState's doc). Exactly one action is offered per row,
// mirroring vmCommands' enabledFor idiom (commandreg.go): "Open draft PR" for
// a pushed branch with no PR (gh, or the compare-URL browser fallback when
// host gh is absent/unauthed), "Open in browser" for a branch that already
// has one, and no action at all for an at-risk (unpushed/dirty), local-only,
// or non-GitHub-forge row.
//
// Every action — including "Open draft PR" — runs through the SAME job
// registry every other sand action does (jobs.go/progress.go): it streams
// into the viewport and is retained as a reopenable ledger entry (task 8
// binds 'L' to reopen it; the retention mechanism itself is job-registry
// native and needs no change here). No guest execution happens on ANY of
// these actions — every one of them is a workstation-local gh call or an OS
// browser-open; the guest is touched only by the read-only sweep that
// populated the registry this pane reads.
//
// Wiring the 'l' key to open this pane (with the shell/u/g running-VM gating
// idiom) is task 8's job; this file exposes openLandingPane so it — and this
// file's own tests — can drive the pane directly.

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/lullabot/sandbar/internal/checkouts"
	"github.com/lullabot/sandbar/internal/landgh"
	"github.com/lullabot/sandbar/internal/provider"
	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/vm"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ghActions is the seam over internal/landgh's Client that the Landing pane
// (and its tests) act through, so a test can fake every host-side gh/browser
// call rather than spawning a real gh binary or launching a real browser.
// landgh.New() (a *landgh.Client) implements it; model.New wires that in as
// the model's default (m.ghActions).
type ghActions interface {
	Availability(ctx context.Context) landgh.Availability
	PRState(ctx context.Context, orgRepo, branch string) (*landgh.PR, error)
	CreateDraftPR(ctx context.Context, orgRepo, branch string) (*landgh.PR, error)
	OpenInBrowser(ctx context.Context, target string) error
}

// landRowKind is the row-state half of the plan's Component 4 table.
type landRowKind int

const (
	landRowLocalOnly landRowKind = iota
	landRowAtRisk
	landRowPushedNoPR
	landRowPushedHasPR
	landRowOtherForge
	// landRowNothingToLand is a checkout sitting on its repo's default branch
	// with nothing of its own to land — every pristine clone, and any checkout
	// whose work has already gone straight to the trunk. It is a state, not a
	// warning: it renders in the pane's ordinary dim chrome and offers no
	// action. See checkouts.Checkout.NothingToLand.
	landRowNothingToLand
)

// landAction is the action-key half of the table: the ONE action a row's
// enabledFor idiom exposes, or none.
type landAction int

const (
	landActionNone landAction = iota
	landActionOpenDraftPR
	landActionOpenInBrowser
	// landActionCommitAndPush is the at-risk row's action: commit whatever is
	// uncommitted (opening the user's editor, in the guest, on their real
	// terminal) and push the branch. It is the ONE landing action that is not
	// a host-side gh call — it runs entirely inside the VM, because that is
	// where the code is and where it must stay.
	landActionCommitAndPush
)

// landRow is one rendered/actionable line of the pane: a single checkout
// (repo or worktree) plus its resolved state and action.
type landRow struct {
	Checkout   checkouts.Checkout
	Indent     bool // true for a worktree nested under its parent repo
	PR         *landgh.PR
	PRResolved bool
	Kind       landRowKind
	Action     landAction
	Label      string
}

// prCheck is the state of one checkout's authoritative `gh pr list` lookup.
//
// It replaced a plain "resolved bool", which could only say yes-or-no and so
// forced every not-yet-answered row to render as "(checking…)". That was a lie
// in two of the three ways a row can lack an answer: when host gh is unusable
// no lookup is ever fired, and when a lookup FAILS its result is dropped. Both
// left a row claiming to be checking, forever, with nothing checking. A row
// that cannot know must say it does not know.
type prCheck int

const (
	// prCheckPending: a lookup is genuinely in flight. This is the ONLY state
	// that may render "(checking…)".
	prCheckPending prCheck = iota
	// prCheckDone: gh answered authoritatively. A nil PR here means a
	// confirmed "there is no PR", not "not known yet".
	prCheckDone
	// prCheckSkipped: no usable host gh, so nothing will ever check.
	prCheckSkipped
	// prCheckFailed: a lookup ran and errored.
	prCheckFailed
)

// resolvedPR is one checkout's gh PRState outcome, keyed by Checkout.Path in
// landingPane.resolved.
type resolvedPR struct {
	pr    *landgh.PR
	state prCheck
}

// hasRemote reports whether a checkout has any remote configured at all — the
// one condition under which no landing action can do anything. The sweep only
// records a Forge/OrgRepo when `git remote get-url` returned something, so a
// non-empty either way means a remote exists. Forge is checked as well as
// OrgRepo because a remote whose URL does not parse into an org/repo slug is
// still perfectly pushable: `git push` names the REMOTE, not the parsed URL.
func hasRemote(c checkouts.Checkout) bool {
	return c.OrgRepo != "" || c.Forge != ""
}

// isGitHubForge reports whether forge is the one forge land's one-key action
// covers. The plan scopes the one-key action to GitHub only; other forges
// (GitLab, drupal.org) still show state but get no one-key action — and,
// since landgh's CompareURL/PRURL are GitHub-specific by construction, "at
// most browser-open" is realized here as no action at all rather than a
// guessed-at, wrong non-GitHub URL.
func isGitHubForge(forge string) bool {
	return strings.EqualFold(forge, "github.com")
}

// classifyLandRow is the PURE row-state -> action mapping (plan Component
// 4's table), the single place that decides what a checkout's row says and
// does. pr/resolved carry the AUTHORITATIVE PRState result once it has
// landed (see handleLandingPRState); until then resolved is false and a
// pushed GitHub checkout is shown provisionally as "pushed - no PR", per the
// plan's "treat the sweep's push-state as a hint" note.
//
// Priority order matters and is deliberate:
//  1. No remote at all: nothing any action here could target. This is the
//     ONLY meaning of "local only" — it used to also swallow a branch that
//     simply had not been pushed yet, which hid the single case the pane is
//     most useful for behind a label saying there was nothing to do.
//  2. At-risk — uncommitted changes, unpushed commits, or a branch with no
//     remote-tracking ref at all — wins over every PR arm below, REGARDLESS
//     of whether an earlier, already-pushed commit on the same branch has a
//     PR: local state the forge does not yet reflect must be resolved before
//     anything points the user at a PR that misrepresents it.
//  3. On the default branch with nothing of its own: no PR to open.
//  4. A non-GitHub forge: state shown, no one-key action (deferred: glab).
//  5. Pushed on GitHub: "no PR" (offer Open draft PR) or "PR #N" (offer Open
//     in browser), depending on the authoritative check.
func classifyLandRow(c checkouts.Checkout, pr *landgh.PR, check prCheck) landRow {
	row := landRow{Checkout: c, PR: pr, PRResolved: check == prCheckDone}

	switch {
	case !hasRemote(c):
		row.Kind = landRowLocalOnly
		row.Action = landActionNone
		// No remote means no action is possible — but the row must still say
		// what is at stake. A checkout with uncommitted or unpushed work and
		// nowhere to send it is the MOST fragile thing in the VM, so "local
		// only" alone would understate it precisely where it matters most.
		row.Label = "local only"
		if c.Dirty > 0 || c.PushState == checkouts.PushStateUnpushed {
			row.Label += " · " + atRiskLabel(c)
		}
	case c.PushState == checkouts.PushStateUnpushed ||
		c.PushState == checkouts.PushStateNever ||
		c.Dirty > 0:
		// Work that exists only in this VM is exactly what the pane should be
		// able to rescue, so this row offers to commit and push it rather than
		// only naming the problem.
		//
		// PushStateNever belongs here and not under "local only": a branch
		// created in the VM and committed to but never pushed is the CENTRAL
		// case this feature exists for, and it has a remote to push to (the
		// arm above already took the checkouts that do not). Leaving it out
		// also made the pane self-contradictory — the same never-pushed branch
		// was offered a rescue when it happened to be dirty and nothing at all
		// when it was clean.
		row.Kind = landRowAtRisk
		row.Action = landActionCommitAndPush
		row.Label = atRiskLabel(c)
	case c.NothingToLand():
		// On the repo's default branch with nothing of its own: a pristine
		// clone, or work that already went straight to the trunk. Offering
		// "Open draft PR" here proposed a main -> main PR, which GitHub
		// rejects outright — so this arm sits ahead of every PR arm below and
		// exposes no action at all.
		row.Kind = landRowNothingToLand
		row.Action = landActionNone
		row.Label = "nothing to land"
	case !isGitHubForge(c.Forge):
		row.Kind = landRowOtherForge
		row.Action = landActionNone
		row.Label = fmt.Sprintf("pushed on %s (no landing action)", c.Forge)
	case check == prCheckDone && pr != nil:
		row.Kind = landRowPushedHasPR
		row.Action = landActionOpenInBrowser
		row.Label = prLabel(pr)
	default:
		// No PR to open in a browser — whether that is confirmed or merely
		// unknown, the offered action is the same, and "Open draft PR" is safe
		// either way (gh itself refuses a genuine duplicate far more cheaply
		// than this pane could re-derive it). What DIFFERS is how confidently
		// the row may state it.
		row.Kind = landRowPushedNoPR
		row.Action = landActionOpenDraftPR
		switch check {
		case prCheckDone:
			row.Label = "pushed · no PR"
		case prCheckPending:
			row.Label = "pushed · no PR (checking…)"
		case prCheckFailed:
			row.Label = "pushed · PR state unknown (check failed)"
		default: // prCheckSkipped
			row.Label = "pushed · PR state unknown (no usable gh)"
		}
	}
	return row
}

// atRiskLabel formats the at-risk row's label: unpushed commits, uncommitted
// changes, or both.
func atRiskLabel(c checkouts.Checkout) string {
	// A never-pushed branch has no honest ahead count to show (there is no
	// tracking ref to count against — Checkout.Ahead is defined 0 for it), so
	// it is named in words rather than with a fabricated "↑0". It gets its own
	// arms rather than falling through to the dirty-only default, which used
	// to render a clean never-pushed branch as "0 uncommitted" — a label that
	// managed to be both wrong and reassuring.
	switch {
	case c.PushState == checkouts.PushStateNever && c.Dirty > 0:
		return fmt.Sprintf("never pushed + %d uncommitted", c.Dirty)
	case c.PushState == checkouts.PushStateNever:
		return "never pushed"
	case c.PushState == checkouts.PushStateUnpushed && c.Dirty > 0:
		return fmt.Sprintf("↑%d unpushed + %d uncommitted", c.Ahead, c.Dirty)
	case c.PushState == checkouts.PushStateUnpushed:
		return fmt.Sprintf("↑%d unpushed", c.Ahead)
	default:
		return fmt.Sprintf("%d uncommitted", c.Dirty)
	}
}

// prLabel formats an existing PR's row label. Callers only pass a non-nil pr.
func prLabel(pr *landgh.PR) string {
	state := strings.ToLower(pr.State)
	if pr.Draft {
		return fmt.Sprintf("PR #%d (%s, draft)", pr.Number, state)
	}
	return fmt.Sprintf("PR #%d (%s)", pr.Number, state)
}

// landGroup is one repo checkout plus the worktrees the sweep found linked to
// it (task 1's Kind/Parent), or — for an orphaned worktree whose parent
// wasn't itself in the swept list (e.g. cut by the sweep's cap) — a
// standalone group holding just that worktree.
type landGroup struct {
	Repo      checkouts.Checkout
	HasRepo   bool
	Worktrees []checkouts.Checkout
}

// groupCheckouts nests every KindWorktree row under its KindRepo parent,
// preserving the sweep's own relative ordering for repos and for worktrees
// within their group. This is what the pane's acceptance criterion
// ("worktree rows grouped under their parent repo") reduces to: a pure
// reshaping of the registry's flat Checkouts slice, with no I/O of its own.
func groupCheckouts(cs []checkouts.Checkout) []landGroup {
	var groups []landGroup
	index := make(map[string]int, len(cs))
	for _, c := range cs {
		if c.Kind == checkouts.KindWorktree {
			continue
		}
		index[c.Path] = len(groups)
		groups = append(groups, landGroup{Repo: c, HasRepo: true})
	}

	var orphans []checkouts.Checkout
	for _, c := range cs {
		if c.Kind != checkouts.KindWorktree {
			continue
		}
		if i, ok := index[c.Parent]; ok {
			groups[i].Worktrees = append(groups[i].Worktrees, c)
			continue
		}
		// The sweep's own cap (or a race with a not-yet-recorded parent) can
		// leave a worktree whose Parent path isn't itself in cs. Still show
		// it — never silently drop a discovered checkout — as its own
		// standalone group rather than nesting it under nothing.
		orphans = append(orphans, c)
	}
	for _, o := range orphans {
		groups = append(groups, landGroup{Worktrees: []checkouts.Checkout{o}})
	}
	return groups
}

// buildLandRows flattens groups into the pane's display/cursor order — each
// repo row followed immediately by its worktrees — resolving every
// checkout's row state against resolved (keyed by Checkout.Path).
// dflt is the state a checkout with no recorded outcome takes — prCheckPending
// while the pane still expects lookups to fire, prCheckSkipped once host gh is
// known to be unusable and none ever will.
func buildLandRows(groups []landGroup, resolved map[string]resolvedPR, dflt prCheck) []landRow {
	at := func(path string) resolvedPR {
		if rp, ok := resolved[path]; ok {
			return rp
		}
		return resolvedPR{state: dflt}
	}
	var rows []landRow
	for _, g := range groups {
		if g.HasRepo {
			rp := at(g.Repo.Path)
			rows = append(rows, classifyLandRow(g.Repo, rp.pr, rp.state))
		}
		for _, wt := range g.Worktrees {
			rp := at(wt.Path)
			r := classifyLandRow(wt, rp.pr, rp.state)
			r.Indent = true
			rows = append(rows, r)
		}
	}
	return rows
}

// landingPane is the model's Landing-pane state (a single field on model —
// see model.go's `landing landingPane`). Plain value state, safe to copy
// with the rest of the model: nothing here outlives one Update call the way
// a job's log or a heartbeat sample does.
type landingPane struct {
	scope registry.Scope
	// vm is the VM record the pane was opened for, retained because the
	// commit-and-push action needs it to build a guest argv (Provider.RunArgv)
	// long after the board handed it over. vmName stays as the identity every
	// message is keyed by.
	vm       vm.VM
	vmName   string
	groups   []landGroup
	rows     []landRow
	cursor   int
	resolved map[string]resolvedPR

	// ghAvailability/ghChecked describe the lazy host-gh probe fired on open.
	// ghChecked is false until that result lands, so the header can say
	// "checking…" rather than guessing a mode before it knows one. The full
	// Availability (not just its OK bit) is retained so the header can name
	// WHICH failure it hit — installed-but-unauthenticated is a different fix
	// from not-installed, and the pane is where the user reads about it.
	ghAvailability landgh.Availability
	ghChecked      bool

	// sweptAt is when the data on screen was collected, carried from the
	// registry entry the pane was built from, and shown in the header — the
	// rows are a CACHE refreshed roughly every 60s, and a pane that looked
	// equally confident whether its data was 3 seconds or 3 minutes old
	// invited the user to act on a picture the VM had already moved past.
	sweptAt time.Time

	// scanning is true while an on-demand rescan (the `r` key) is in flight,
	// so the header can say so and a second press cannot race the first.
	scanning bool
}

// landingAvailableMsg carries the result of the lazy host-gh-availability
// check fired when the Landing pane opens (openLandingPane).
type landingAvailableMsg struct {
	scope        registry.Scope
	vm           string
	availability landgh.Availability
}

// landingPRStateMsg carries one checkout's AUTHORITATIVE PRState result.
// Scoped by (scope, vm, path) so a result that lands after the pane has
// moved to a different VM (or closed and reopened) is recognized as stale
// and dropped, rather than silently promoting a row that no longer belongs
// to the pane the user is looking at.
type landingPRStateMsg struct {
	scope registry.Scope
	vm    string
	path  string
	pr    *landgh.PR
	err   error
}

// openLandingPane opens the Landing pane for v: a snapshot of the checkout
// registry's cached rows for v, grouped (worktrees under their parent repo),
// followed by the lazy authoritative gh check. PR-state resolution for each
// pushed GitHub branch is deferred until THAT check comes back (see
// handleLandingAvailable) — firing N `gh pr list` calls before even knowing
// gh is usable would be pointless work on every gh-absent open.
//
// Gating this to a focused, RUNNING VM (matching shell/u/g's enabledFor
// idiom) is the CALLER's job — commandreg.go's future land verb (task 8) —
// not this method's: it opens the pane for whatever v it is given, so tests
// (and, later, that verb) can drive it directly.
func (m *model) openLandingPane(v boardVM) tea.Cmd {
	vc, _ := m.checkouts.Get(v.scope, v.Name)
	groups := groupCheckouts(vc.Checkouts)
	m.landing = landingPane{
		scope:    v.scope,
		vm:       v.VM,
		vmName:   v.Name,
		sweptAt:  vc.SweptAt,
		groups:   groups,
		resolved: map[string]resolvedPR{},
	}
	// Pending: the availability probe is about to fire, and will fire lookups
	// if gh turns out to be usable. handleLandingAvailable downgrades these to
	// prCheckSkipped if it is not.
	m.landing.rows = buildLandRows(groups, m.landing.resolved, prCheckPending)
	m.view = viewLanding
	return checkLandingAvailableCmd(m.ghActions, v.scope, v.Name)
}

// checkLandingAvailableCmd fires the lazy host-gh-availability check.
func checkLandingAvailableCmd(gh ghActions, scope registry.Scope, name string) tea.Cmd {
	return func() tea.Msg {
		return landingAvailableMsg{scope: scope, vm: name, availability: gh.Availability(context.Background())}
	}
}

// handleLandingAvailable folds the lazy Available() result into the pane
// and, only once gh is known to be usable, fires the authoritative PRState
// lookup for every pushed GitHub checkout the pane is showing — see
// checkLandingAvailableCmd's doc for why that ordering matters.
func (m *model) handleLandingAvailable(msg landingAvailableMsg) tea.Cmd {
	if msg.scope != m.landing.scope || msg.vm != m.landing.vmName {
		return nil // stale: the pane has since moved on
	}
	m.landing.ghAvailability = msg.availability
	m.landing.ghChecked = true
	if !msg.availability.OK() {
		// Graceful degradation: with no usable host gh there is nothing
		// authoritative to check, and — the part that used to be wrong —
		// nothing ever WILL be. Rebuild the rows as skipped so they say the PR
		// state is unknown, rather than sitting on "(checking…)" forever with
		// no lookup in flight. "Open draft PR" still works, falling back to
		// the compare URL (landDraftPRRun).
		m.landing.rows = buildLandRows(m.landing.groups, m.landing.resolved, prCheckSkipped)
		return nil
	}

	cmds := m.prStateCmdsFor(msg.scope, msg.vm)
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// defaultPRCheck is the state a checkout with no recorded outcome should take,
// given what the pane currently knows about host gh. Before the availability
// probe answers, a lookup may still be coming; once gh is known unusable, none
// ever will.
func (p landingPane) defaultPRCheck() prCheck {
	if p.ghChecked && !p.ghAvailability.OK() {
		return prCheckSkipped
	}
	return prCheckPending
}

// prStateCmdsFor returns a lookup command for every row that still needs an
// authoritative answer, skipping the ones already resolved and the ones with
// no branch-vs-trunk PR to look for. It is shared by the pane's initial
// availability handler and its post-rescan refresh: a rescan can surface a
// checkout that did not exist when the pane opened, and without this that row
// would sit on "(checking…)" with no lookup ever dispatched for it.
func (m *model) prStateCmdsFor(scope registry.Scope, name string) []tea.Cmd {
	var cmds []tea.Cmd
	for _, row := range m.landing.rows {
		c := row.Checkout
		// NothingToLand rows are skipped alongside the never-pushed and
		// non-GitHub ones: a checkout parked on its default branch has no
		// branch-vs-trunk PR to look up, so querying gh for one would spend a
		// network round trip per pristine clone to answer a question that is
		// already settled.
		if c.PushState != checkouts.PushStatePushed || c.OrgRepo == "" ||
			!isGitHubForge(c.Forge) || c.NothingToLand() {
			continue
		}
		if _, done := m.landing.resolved[c.Path]; done {
			continue
		}
		cmds = append(cmds, prStateCmd(m.ghActions, scope, name, c.Path, c.OrgRepo, c.Branch))
	}
	return cmds
}

// prStateCmd fires one checkout's authoritative gh PRState lookup.
func prStateCmd(gh ghActions, scope registry.Scope, vmName, path, orgRepo, branch string) tea.Cmd {
	return func() tea.Msg {
		pr, err := gh.PRState(context.Background(), orgRepo, branch)
		return landingPRStateMsg{scope: scope, vm: vmName, path: path, pr: pr, err: err}
	}
}

// handleLandingPRState folds one checkout's authoritative PR result into the
// pane and rebuilds its rows. An error (network hiccup, a `gh` rate limit) is
// deliberately NOT recorded as a resolution: the row is left on the sweep's
// provisional hint (classifyLandRow's default branch) rather than promoted
// or demoted on bad information.
func (m *model) handleLandingPRState(msg landingPRStateMsg) {
	if msg.scope != m.landing.scope || msg.vm != m.landing.vmName {
		return // stale: the pane has since moved on
	}
	if m.landing.resolved == nil {
		m.landing.resolved = map[string]resolvedPR{}
	}
	if msg.err != nil {
		// RECORDED, not dropped. A dropped failure left the row on
		// "(checking…)" permanently, which reads as "any moment now" for a
		// lookup that already finished and lost.
		m.landing.resolved[msg.path] = resolvedPR{state: prCheckFailed}
	} else {
		m.landing.resolved[msg.path] = resolvedPR{pr: msg.pr, state: prCheckDone}
	}
	m.landing.rows = buildLandRows(m.landing.groups, m.landing.resolved, m.landing.defaultPRCheck())
}

// landDraftPRRun builds the streamFunc for "Open draft PR": CreateDraftPR
// when host gh is available, or — the graceful-degradation fallback (plan
// Component 4) — opening the compare URL in the browser when it is not, so
// the action is never simply dead without gh. Both branches are gh-token-
// scoped, workstation-local calls; neither touches a guest or writes
// repository code to the host (only the returned PR's metadata/URL).
func landDraftPRRun(gh ghActions, available bool, orgRepo, branch string) streamFunc {
	return func(ctx context.Context, out io.Writer) error {
		if !available {
			url, err := landgh.CompareURL(orgRepo, branch)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "host gh unavailable — opening the compare URL in your browser instead:\n%s\n", url)
			return gh.OpenInBrowser(ctx, url)
		}
		pr, err := gh.CreateDraftPR(ctx, orgRepo, branch)
		if err != nil {
			return fmt.Errorf("create draft PR: %w", err)
		}
		fmt.Fprintf(out, "draft PR #%d created: %s\n", pr.Number, pr.URL)
		return nil
	}
}

// landOpenBrowserRun builds the streamFunc for "Open in browser": always
// gh-free, targeting the PR's own URL (falling back to PRURL if gh's
// response happened not to carry one).
func landOpenBrowserRun(gh ghActions, orgRepo string, pr *landgh.PR) streamFunc {
	return func(ctx context.Context, out io.Writer) error {
		if pr == nil {
			return fmt.Errorf("landing: no PR to open")
		}
		url := pr.URL
		if url == "" {
			u, err := landgh.PRURL(orgRepo, pr.Number)
			if err != nil {
				return err
			}
			url = u
		}
		fmt.Fprintf(out, "opening %s in your browser\n", url)
		return gh.OpenInBrowser(ctx, url)
	}
}

// runLandingAction dispatches whichever ONE action the row under the pane's
// cursor exposes (classifyLandRow's Action), as a JOB (jobs.go/progress.go) —
// exactly like every other sand action — so its output streams into the
// viewport and is retained as a reopenable ledger entry. A row with no
// action (at-risk, local-only, other-forge) does nothing.
func (m *model) runLandingAction() tea.Cmd {
	if m.landing.cursor < 0 || m.landing.cursor >= len(m.landing.rows) {
		return nil
	}
	row := m.landing.rows[m.landing.cursor]
	c := row.Checkout

	var title string
	var run streamFunc
	switch row.Action {
	case landActionOpenDraftPR:
		title = "Open draft PR: " + c.OrgRepo + "#" + c.Branch
		run = landDraftPRRun(m.ghActions, m.landing.ghAvailability.OK(), c.OrgRepo, c.Branch)
	case landActionOpenInBrowser:
		title = "Open in browser: " + c.OrgRepo + "#" + c.Branch
		run = landOpenBrowserRun(m.ghActions, c.OrgRepo, row.PR)
	case landActionCommitAndPush:
		// Not a streamFunc: this one suspends the TUI and hands the terminal
		// to the guest so `git commit` can open an editor. It therefore
		// returns directly rather than joining the job registry below.
		return landCommitPushCmd(m.provFor(m.landing.scope), m.landing.scope, m.landing.vm, c.Path)
	default:
		return nil
	}

	jk := landKey(m.landing.scope, m.landing.vmName)
	cmd, started := m.beginStream(jk, title, run)
	if started {
		m.focusJob(jk)
	}
	return cmd
}

// landingActKey triggers the row under the cursor's one action, mirroring
// vmCommands' enabledFor idiom one level down (one key, one action, per
// row).
var landingActKey = key.NewBinding(key.WithKeys("enter", "o"), key.WithHelp("enter", "act"))

// landingRefreshKey re-sweeps the VM on demand. The pane's rows come from a
// cache the background sweep refreshes about every 60s, so after committing or
// pushing inside the VM's own shell there is a window where the pane is
// confidently out of date. This is the way to close it without waiting.
var landingRefreshKey = key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "rescan"))

// landingMoveKey describes the pane's row cursor in the footer. It is a
// pane-local binding rather than the shared form keys (m.keys.Up/Down) for two
// reasons: those are labelled "prev field"/"next field", which is the wrong
// vocabulary for a list of checkouts, and m.keys.Down BINDS ENTER — so the
// footer claimed enter moved the cursor down while enter actually runs the
// row's action (see updateLanding, which dispatches on msg.Code before it ever
// reaches the act key).
var landingMoveKey = key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "move"))

// actionVerb names what the act key would actually DO to the row under the
// cursor, for the footer to show instead of a generic "act".
//
// The pane already decided one action per row (classifyLandRow); the footer
// was the last place still describing that as an abstraction. "enter act" made
// the user select a row to find out what would happen — and the actions differ
// enough (one opens an editor and pushes, one creates a PR, one opens a
// browser) that the difference is worth knowing BEFORE pressing the key.
//
// The commit-and-push verb further splits on whether there is anything to
// commit, because "commit + push" on a checkout with a clean tree would
// promise an editor that never opens.
func actionVerb(row landRow) string {
	switch row.Action {
	case landActionCommitAndPush:
		if row.Checkout.Dirty > 0 {
			return "commit + push"
		}
		return "push"
	case landActionOpenDraftPR:
		return "open draft PR"
	case landActionOpenInBrowser:
		return "open in browser"
	default:
		return ""
	}
}

// landingActBinding is landingActKey relabelled with the focused row's actual
// verb, or disabled entirely when that row has no action — so the footer never
// advertises a key that would do nothing.
func (p landingPane) landingActBinding() key.Binding {
	if p.cursor < 0 || p.cursor >= len(p.rows) {
		return landingActKey
	}
	verb := actionVerb(p.rows[p.cursor])
	if verb == "" {
		b := key.NewBinding(key.WithKeys("enter", "o"), key.WithHelp("enter", "act"))
		b.SetEnabled(false)
		return b
	}
	return key.NewBinding(key.WithKeys("enter", "o"), key.WithHelp("enter", verb))
}

// updateLanding handles keys on the Landing pane: up/down move the row
// cursor, the act key runs the row's action (if any), and Back returns to
// the board without disturbing anything in flight (a dispatched action
// keeps running in the job registry exactly like a file transfer would).
func (m model) updateLanding(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.Code {
	case tea.KeyUp:
		if m.landing.cursor > 0 {
			m.landing.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.landing.cursor < len(m.landing.rows)-1 {
			m.landing.cursor++
		}
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.Back):
		m.view = viewBoard
		return m, nil
	case key.Matches(msg, landingActKey):
		cmd := m.runLandingAction()
		return m, cmd
	case key.Matches(msg, landingRefreshKey):
		if m.landing.scanning {
			return m, nil // one in flight already; a second would race it
		}
		m.landing.scanning = true
		return m, landRefreshCmd(m.sweeps, m.landing.scope, m.landing.vmName)
	}
	return m, nil
}

// landingHelp is the Landing pane's footer. The act key carries the focused
// row's REAL verb (see landingActBinding), so the footer answers "what happens
// if I press enter" without the user having to press it.
func (m model) landingHelp() []key.Binding {
	return []key.Binding{
		landingMoveKey,
		m.landing.landingActBinding(),
		landingRefreshKey,
		m.keys.Back,
	}
}

// scanLabel is the pane header's freshness line: how old the rows on screen
// are, and how to replace them.
//
// The pane reads a cache the background sweep refreshes about every 60s. With
// no age shown, three-second-old and three-minute-old data looked identical —
// which matters most right after the user has committed or pushed inside the
// VM's own shell, exactly when they are most likely to be looking at the pane
// and least likely to be seeing the truth.
func (p landingPane) scanLabel(now time.Time) string {
	if p.scanning {
		return "rescanning…"
	}
	if p.sweptAt.IsZero() {
		// No sweep has ever completed for this VM: say so rather than
		// rendering a nonsense age off a zero time.
		return "not scanned yet · r to scan"
	}
	return "scanned " + formatAgo(now.Sub(p.sweptAt)) + " · r to rescan"
}

// ghModeLabel is the pane header's gh-mode line — the plan's "the pane
// surfaces which mode it's in" requirement for graceful degradation.
//
// The two degraded modes are named SEPARATELY and each carries its own fix.
// They used to share one "gh: unavailable" line, which read as "you don't have
// gh" to someone who demonstrably did: gh is exec'd directly, argv-only, so a
// credential that lives in a shell alias or wrapper (the 1Password shell
// plugin, and other credential injectors) is invisible to it even though the
// same command works when typed at a prompt. Saying "not authenticated" and
// naming `gh auth login` points at the actual gap instead of denying the
// binary exists.
func (p landingPane) ghModeLabel() string {
	switch {
	case !p.ghChecked:
		return "checking host gh…"
	case p.ghAvailability.OK():
		return "gh: available — Open draft PR creates it directly"
	case !p.ghAvailability.Installed:
		return "gh: not installed — Open draft PR opens the compare URL in your browser"
	case p.ghAvailability.ViaOnePassword:
		// Their gh is fine and their token is in 1Password — telling them to
		// run `gh auth login` would send them to fix the wrong thing.
		return "gh: 1Password did not authorize (unlock 1Password, or export GH_TOKEN) — " +
			"Open draft PR opens the compare URL in your browser"
	default:
		return "gh: not authenticated (run `gh auth login`, or export GH_TOKEN) — " +
			"Open draft PR opens the compare URL in your browser"
	}
}

// styleForLandRow picks a row's colour by its Kind: amber for the actionable
// "pushed, no PR" row (the same warn vocabulary the tile badge uses), green
// for an existing PR, red/at-risk styling for unpushed/dirty, and the plain
// dim status colour for everything else (local-only, other-forge, and
// nothing-to-land — all states, not warnings).
func styleForLandRow(k landRowKind) lipgloss.Style {
	switch k {
	case landRowPushedNoPR:
		return warnStyle
	case landRowPushedHasPR:
		return okStyle
	case landRowAtRisk:
		return errStyle
	default:
		return statusStyle
	}
}

// landingView renders the pane: a title naming the VM, the gh-mode line, and
// one line per row (a worktree indented under its parent repo), the cursor
// marked, styled by state, and the footer.
func (m model) landingView() string {
	cw := m.layout.ContentWidth
	var b strings.Builder
	b.WriteString(titleStyle.Render("Landing: " + m.landing.vmName))
	b.WriteString("\n\n")
	b.WriteString(hintStyle.Width(cw).Render(m.landing.ghModeLabel()))
	b.WriteString("\n")
	b.WriteString(hintStyle.Width(cw).Render(m.landing.scanLabel(time.Now())))
	b.WriteString("\n\n")

	if len(m.landing.rows) == 0 {
		b.WriteString(statusStyle.Render("No git checkouts discovered yet — the sweep runs about every 60s."))
		b.WriteString("\n")
	}
	for i, row := range m.landing.rows {
		cursor := "  "
		if i == m.landing.cursor {
			cursor = "> "
		}
		prefix := ""
		if row.Indent {
			prefix = "  └─ "
		}
		branch := row.Checkout.Branch
		if branch == "" {
			branch = "(detached)"
		}
		line := fmt.Sprintf("%s%s%s (%s) — %s", cursor, prefix, row.Checkout.Path, branch, row.Label)
		b.WriteString(m.clipLine(styleForLandRow(row.Kind).Render(line)))
		b.WriteString("\n")
	}

	b.WriteString("\n" + m.footerView(m.landingHelp()))
	return appStyle.Render(b.String())
}

// commitAndPushExpr is the guest-side script the commit-and-push action runs.
//
// It is a FIXED, LITERAL string, and must stay one. The checkout it acts on is
// selected entirely by the working directory Provider.RunArgv sets
// (`--workdir <path>`, its own argv element), never by interpolating anything
// into this text — the path, branch, and remote all come from a sweep of the
// GUEST, and splicing any of them into a script the guest's `bash -c` parses
// would hand that sweep output a shell. Everything the script needs about the
// checkout it therefore works out for itself, in the guest, at run time.
//
// Behaviour, in order:
//   - Commit only if there is actually something uncommitted, so a row that is
//     merely unpushed goes straight to the push. `git commit` (no -m) opens the
//     user's editor, which is the whole reason this action needs a real TTY.
//   - Resolve the remote the same way the sweep does: the branch's configured
//     remote, falling back to the first configured one — never assuming
//     "origin".
//   - Push with -u so a never-pushed branch gets its upstream set, and by HEAD
//     so the branch name never has to be spelled.
//
// `set -e` means an aborted commit (the user quits their editor without saving
// a message, so git exits non-zero) stops before the push. That is the correct
// reading of "I changed my mind": nothing is pushed.
const commitAndPushExpr = `set -e
if [ -n "$(git status --porcelain)" ]; then
  git commit -a
fi
b=$(git symbolic-ref --short HEAD)
r=$(git config --get "branch.$b.remote") || true
[ -n "$r" ] || r=$(git remote | head -n 1)
if [ -z "$r" ]; then
  echo "sand: this checkout has no remote configured — nothing to push to" >&2
  exit 1
fi
git push -u "$r" HEAD`

// landCommitPushCmd suspends the TUI and runs commitAndPushExpr inside the
// guest, against the user's real terminal.
//
// It is tea.ExecProcess for the same reason shellCmd's suspending branch is:
// `git commit` opens an editor, and an editor needs a TTY, not the captured
// pipe every other landing action is happy with. The TUI restores itself when
// the command exits.
//
// This is also the one landing action that touches the guest at all — and it
// does so WITHOUT moving any code to the host, which is the invariant that
// matters: the commit and the push both happen inside the VM, using the
// guest's own least-privilege push token. The host never sees a diff, a patch,
// or a working tree. See AGENTS.md's Landing invariants.
func landCommitPushCmd(p provider.Provider, scope registry.Scope, v vm.VM, path string) tea.Cmd {
	argv := p.RunArgv(v, path, commitAndPushExpr)
	if len(argv) == 0 {
		return nil
	}
	c := exec.Command(argv[0], argv[1:]...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return landCommitPushDoneMsg{scope: scope, vm: v.Name, path: path, err: err}
	})
}

// landCommitPushDoneMsg reports the interactive commit-and-push finishing, so
// the pane can re-read the checkout it just changed rather than showing the
// state that prompted the action.
type landCommitPushDoneMsg struct {
	scope registry.Scope
	vm    string
	path  string
	err   error
}

// handleLandCommitPushDone folds the finished commit-and-push back in. The
// checkout's state has almost certainly changed, so it re-sweeps rather than
// leaving the pane advertising the work it just landed. A non-zero exit is
// reported but NOT treated as a reason to skip the re-read: an aborted commit
// leaves the checkout exactly as it was, and a push that failed halfway may
// still have committed.
func (m *model) handleLandCommitPushDone(msg landCommitPushDoneMsg) tea.Cmd {
	if msg.err != nil {
		m.logMsg("commit/push on " + msg.path + " did not complete")
	}
	if m.landing.scope != msg.scope || m.landing.vmName != msg.vm {
		return nil
	}
	// The checkout just changed, so any PR answer recorded for it is stale —
	// a push that created the branch may now have a PR to find.
	delete(m.landing.resolved, msg.path)
	m.landing.scanning = true
	return landRefreshCmd(m.sweeps, msg.scope, msg.vm)
}

// landRefreshCmd re-sweeps the VM the pane is showing. Same one-shot read the
// delete guard uses (sweepRegistry.sweepOnce), against a VM the pane already
// required to be running.
func landRefreshCmd(sweeps *sweepRegistry, scope registry.Scope, name string) tea.Cmd {
	return func() tea.Msg {
		vc, err := sweeps.sweepOnce(context.Background(), scope, name)
		return landRefreshMsg{scope: scope, vm: name, vc: vc, err: err}
	}
}

// landRefreshMsg carries a post-action re-sweep back to the pane.
type landRefreshMsg struct {
	scope registry.Scope
	vm    string
	vc    checkouts.VMCheckouts
	err   error
}

// handleLandRefresh rebuilds the pane's rows from a fresh sweep, keeping the
// cursor where the user left it (clamped, since the row count can change).
func (m *model) handleLandRefresh(msg landRefreshMsg) tea.Cmd {
	if m.landing.scope != msg.scope || m.landing.vmName != msg.vm {
		return nil // stale: the pane has moved on
	}
	// Cleared on BOTH paths: a failed rescan must not leave the header saying
	// "rescanning…" forever, or the key appears dead from then on.
	m.landing.scanning = false
	if msg.err != nil {
		m.logMsg("rescan of " + msg.vm + " did not complete")
		return nil
	}
	_ = m.checkouts.Set(msg.scope, msg.vm, msg.vc)
	m.landing.sweptAt = msg.vc.SweptAt
	m.landing.groups = groupCheckouts(msg.vc.Checkouts)
	m.landing.rows = buildLandRows(m.landing.groups, m.landing.resolved, m.landing.defaultPRCheck())
	if m.landing.cursor >= len(m.landing.rows) {
		m.landing.cursor = len(m.landing.rows) - 1
	}
	if m.landing.cursor < 0 {
		m.landing.cursor = 0
	}
	// A rescan can surface a checkout that did not exist when the pane opened
	// — the user just created a branch, or cloned another repo. Those rows
	// have no recorded outcome and no lookup in flight, so without this they
	// would sit on "(checking…)" with nothing ever checking them.
	if !m.landing.ghAvailability.OK() {
		return nil
	}
	cmds := m.prStateCmdsFor(msg.scope, msg.vm)
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}
