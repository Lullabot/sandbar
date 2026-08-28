---
id: 8
group: "surfaces"
dependencies: [4, 5, 6]
status: "completed"
created: 2026-08-28
model: "sonnet"
effort: "high"
skills:
  - go
  - bubbletea
complexity_score: 7
complexity_notes: "Fills an existing dead arm in the Landing pane's row classifier and adds a confirmation view and a streaming job — three coupled pieces in a model whose update loop has strict conventions."
---
# TUI Landing pane publish action

## Objective

Fill the arm `classifyLandRow` already reserves for a non-GitHub forge: a
`git.drupalcode.org` checkout row gains a publish action, a confirmation view
showing what will change and where, and progress streamed through the existing
job registry. No new pane, and no publication logic in the UI layer.

## Skills Required

`go` and `bubbletea`: the Landing pane's row model, key handling, view
rendering, and the job registry's message flow.

## Acceptance Criteria

- [ ] `classifyLandRow` (`internal/ui/landing.go`) routes a checkout whose
      `Forge` is `git.drupalcode.org` to a new row kind offering a publish
      action, instead of today's `landRowOtherForge` / `landActionNone` dead
      end. Every other forge keeps the existing no-action behaviour, and a
      GitHub checkout's classification is unchanged — asserted by test.
- [ ] The row's label states what publication would do, in the pane's existing
      terse idiom.
- [ ] Selecting the action opens a confirmation view rendering task 4's
      confirmation text — every commit, its files and paths, the resulting
      contents, and the destination — with the pane's existing confirm/cancel
      idiom. Cancelling publishes nothing.
- [ ] Publication runs through the job registry every other sand action streams
      through, so progress and the final result appear the way other jobs do.
      The per-commit progress and the partial-failure report from task 5's
      `Result` are both surfaced; a partial result is never rendered as a plain
      success or a plain failure.
- [ ] An absent PAT (task 2's `ErrNoToken`) disables the action with a visible
      reason rather than offering it and failing later.
- [ ] The pane holds no resolution, guard, payload, or client logic; it calls
      `internal/drupalorg` for all of it.
- [ ] Tests cover the classifier's new arm, the cancel path performing no
      publish, and the confirmation view containing the destination and the
      file list. Existing landing tests still pass.
- [ ] `go test ./internal/ui/... -race` passes.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Bubble Tea v2 (`charm.land/bubbles/v2`), matching the rest of `internal/ui`.
- `classifyLandRow` is documented as "the PURE row-state -> action mapping …
  the single place that decides what a checkout's row says and does". Keep it
  pure: no I/O, no network, no credential read inside it. Availability of the
  PAT must be passed in as state, the way `prCheck` already is.
- The arm's priority order matters. **This requirement was wrong as first
  written and was corrected during execution** — it is left here with its
  correction rather than silently rewritten, because the defect is instructive.

  It originally said the new arm belongs where `!isGitHubForge(c.Forge)` sits
  today, after the local-only, at-risk and nothing-to-land arms, "so a dirty or
  unpushed drupal.org checkout still shows the at-risk rescue first. Do not
  move it up." That reasoning was carried over from GitHub and does not hold
  here: a sand guest holds **no drupal.org credential by design**, so the
  commit-and-push the at-risk arm offers can never succeed against
  git.drupalcode.org — and the unpushed-commits state that arm claims is
  precisely the state publication exists to serve (clone a canonical project
  you cannot push to, commit locally, publish from the host). Below at-risk,
  the publish action was reachable only once there was nothing left to publish.

  The drupal.org arm therefore sits **ahead** of the at-risk arm — the one
  place the pane's usual priority is deliberately inverted — while a dirty
  working tree is still named in the label, because publication carries
  committed commits only. See TestClassifyLandRowDrupalOrgUnpushedStillOffersPublish.

## Input Dependencies

- Task 4: the confirmation renderer and the `Destination`.
- Task 5: `Publish` and its `Result`.
- Task 6: the guest change-set collector.

## Output Artifacts

- Changes to `internal/ui/landing.go` (row kind, action, classifier arm,
  confirmation view) and the model/job wiring they require.
- Tests in `internal/ui` for the classifier arm and the cancel path.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

The plan's "The two surfaces" section explains why this is smaller than it
sounds:

> The TUI half is smaller than it sounds, because the Landing pane already
> reserves a place for it. `classifyLandRow` in `internal/ui/landing.go`
> carries an arm for *"a non-GitHub forge: state shown, no one-key action
> (deferred: glab)"*, and `checkouts.Checkout` already records the remote's
> `Forge`. A `git.drupalcode.org` checkout flows into that dead arm today.
> Filling it reuses the pane's row model, its one-action-per-row idiom, and the
> job registry every other sand action streams through — so the work is an
> action and a confirmation view, not a new pane.

Read `classifyLandRow`'s existing doc comment before editing: it enumerates its
arms and their priority order, and that comment must be updated to describe the
new arm rather than left describing five arms when there are six.

The existing arm reads:

```go
case !isGitHubForge(c.Forge):
    row.Kind = landRowOtherForge
    row.Action = landActionNone
    row.Label = fmt.Sprintf("pushed on %s (no landing action)", c.Forge)
```

Split it: drupal.org gets a publish action; every other non-GitHub forge keeps
the existing label and no action. A test asserting that a `gitlab.com` checkout
is *unchanged* is worth as much as the one asserting drupal.org's new arm.

The confirmation view is the pane's rendering of task 4's pure text — the plan
requires that neither surface build the confirmation twice in two idioms, so
the *content* comes from `internal/drupalorg` and only the framing is the
pane's.

**Test philosophy — write a few tests, mostly integration.** Meaningful tests
verify custom business logic, critical paths, and edge cases specific to this
application. Test *your* code, not the framework or library. Write tests for:
custom business logic and algorithms; critical user workflows and data
transformations; edge cases and error conditions for core functionality;
integration points between components; complex validation logic. Do **not**
write tests for: third-party library functionality; framework features; simple
CRUD without custom logic; trivial getters/setters; obvious functionality that
would break immediately if incorrect. Combine related scenarios into one test
rather than one per method.

`classifyLandRow` is a pure function with an existing table-driven test — extend
it rather than writing a parallel one. The cancel path is the other meaningful
case, for the same reason as in task 7.

</details>

### Per-task completion gate (required by the plan)

This task is not complete until, after its own tests pass:

1. `/code-review --fix` has run against this task's changes and every finding
   was applied or consciously rejected.
2. `/simplify` has then run, and its findings applied or consciously rejected.
3. The task's tests were **re-run** afterwards and pass.
