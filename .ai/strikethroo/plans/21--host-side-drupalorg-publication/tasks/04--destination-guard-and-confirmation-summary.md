---
id: 4
group: "drupalorg-core"
dependencies: [1, 3]
status: "completed"
created: 2026-08-28
model: "sonnet"
effort: "high"
skills:
  - go
  - unit-testing
complexity_score: 7
complexity_notes: "This is where the split of authority is enforced and where the human's only control over a public, permanent write is rendered. Both are security-relevant and both are covered by adversarial tests."
---
# Destination guard and the shared confirmation summary

## Objective

Assemble the publication destination **entirely from host-side inputs**, refuse
any commit destination outside the `issue/` namespace unless the developer
overrides deliberately, and render the confirmation both surfaces show a human
before anything is sent — every commit, every file action and path, the
resulting contents, and where it is going.

## Skills Required

`go` for the destination type and guard; `unit-testing` for the adversarial
payload cases, which are the reason this task exists as its own unit.

## Acceptance Criteria

- [ ] A `Destination` type carries the fork path, the commit branch, the
      canonical parent project's ID and path, and the parent's target branch.
      It is built by a constructor that takes **only** host-side inputs — the
      targeted checkout's module, the issue number, and the anonymous
      resolution results from task 3 — and takes no `ChangeSet` argument at
      all, so a payload cannot reach it.
- [ ] The guard refuses a commit destination whose fork path is not under
      `issue/`, returning an error that names what was refused and how to
      override. An explicit override parameter (not a default, not an
      environment variable) is the only way past it.
- [ ] The guard is scoped to **commit destinations only**. The merge request's
      `target_project_id` names the canonical parent by construction and must
      not be refused; a comment records why — an MR is a proposal directed at a
      project, not a write to its code, and its target is derived from the
      fork's own `forked_from_project` rather than supplied.
- [ ] A pure rendering function produces the confirmation text: for each
      commit in order, its message and author, and for each file action its
      kind, path (and previous path for a move), and the resulting content —
      readable, not a summary count. Binary content is named as binary with its
      size rather than dumped. The destination is stated explicitly.
- [ ] Adversarial test: a change set whose file paths, contents, commit
      messages, and author fields contain another project's path
      (`project/drupal`), absolute paths, `..` traversal, and shell
      metacharacters produces a `Destination` **identical** to the one produced
      by a benign change set, and traversal-style paths are refused rather than
      normalised.
- [ ] Test: constructing a destination for a fork path outside `issue/` fails
      without the override and succeeds with it.
- [ ] `go test ./internal/drupalorg/... -race` passes.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- The constructor signature is the enforcement mechanism. If it cannot accept
  guest data, no reviewer has to check that it does not use it.
- The rendering function must be pure (no I/O, no terminal escapes) so the CLI
  can print it and the TUI can put it in a view without either reimplementing
  the confirmation.
- Content rendering must not be truncated into meaninglessness — the plan is
  explicit that the confirmation must show "what will change and where in a
  form a human can actually read, not a summary count". Long text content may
  be elided with a clearly marked line count, but the mechanism must not hide
  a whole file.

## Input Dependencies

- Task 1: the `ChangeSet`/`Commit`/`FileAction` types and `ValidateRepoPath`.
- Task 3: `ForkPath`, `Project`, `Branches`, and the parent derived from
  `forked_from_project`.

## Output Artifacts

- `internal/drupalorg/destination.go` — the `Destination` type, its
  constructor, and the `issue/` guard.
- `internal/drupalorg/confirm.go` — the shared confirmation renderer.
- Tests covering the adversarial payload case and the guard's two arms.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

The plan's "Destination selection and confirmation" section is the contract.
The central claim it makes — the one this task enforces — is that **the agent
decides content and the host decides destination**. Everything else in the
design rests on it.

Two controls sit on top of that, and both belong here:

1. Refuse a commit destination outside `issue/` unless overridden, "so writing
   to a canonical `project/<module>` — the case that motivated the entire
   design — cannot happen as a default or a bug, only as an explicit act."
2. Require explicit human confirmation showing what will change and where.
   "Declining publishes nothing. No code path may publish as a side effect of
   any other command." (The surfaces enforce the *asking*; this task supplies
   the *what is shown*.)

The scoping of the guard was a correction in refinement 7 and is easy to get
wrong: an MR **necessarily** names the canonical project as its
`target_project_id`, because on drupal.org every MR runs fork -> parent. A
guard that refused to name a canonical project at all "would forbid the one
thing publication exists to do". So guard commits, not MR targets, and write
down why.

Suggested shape:

```go
type Destination struct {
    ForkPath      string // "issue/<module>-<nid>"
    Branch        string // the issue branch commits land on
    ParentID      int    // forked_from_project.id
    ParentPath    string // "project/<module>"
    ParentBranch  string // parent's default/development branch
}

// NewDestination takes no ChangeSet. That is the enforcement: the payload
// cannot influence the destination because it is not in scope here.
func NewDestination(module string, issue int, fork Project, allowOutsideIssueNS bool) (Destination, error)
```

For the adversarial test, build one benign change set and one hostile one that
differ only in their guest-supplied fields, run both through the same
resolution inputs, and assert `reflect.DeepEqual` on the two destinations.
That is a stronger statement than asserting a particular value, and it stays
true as the type grows.

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

The adversarial destination test and the guard test are the meaningful ones.
The confirmation renderer needs one golden-ish test proving a delete and a
move render legibly — the two kinds the earlier payload design could not
express at all.

</details>

### Per-task completion gate (required by the plan)

This task is not complete until, after its own tests pass:

1. `/code-review --fix` has run against this task's changes and every finding
   was applied or consciously rejected.
2. `/simplify` has then run, and its findings applied or consciously rejected.
3. The task's tests were **re-run** afterwards and pass.
