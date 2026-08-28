---
id: 7
group: "surfaces"
dependencies: [4, 5, 6]
status: "pending"
created: 2026-08-28
model: "sonnet"
effort: "high"
skills:
  - go
  - cli
complexity_score: 7
complexity_notes: "Carries the human-confirmation gate, which is the only control standing between an agent's output and a public, permanent write."
---
# `sand publish` CLI surface

## Objective

Add `cmd/sand/publish.go`: the headless entry point that resolves a
destination, collects the change set from a guest checkout, shows the
confirmation, requires an explicit human yes, publishes, and prints the result
— including a precise partial-failure report. It holds **no publication logic
of its own**.

## Skills Required

`go` and `cli`: flag parsing, terminal/pipe handling, and error phrasing that
matches `cmd/sand/land.go`'s existing idiom.

## Acceptance Criteria

- [ ] `sand publish` accepts the VM name, the checkout path inside the guest,
      and the issue number, and refuses clearly when the VM is unknown or not
      running — reusing `land.go`'s `requireRunningVM` phrasing rather than
      inventing a second wording for the same fact.
- [ ] Before doing any work, an absent PAT (task 2's `ErrNoToken`) is reported
      as "publication is unavailable" with the conventional path named, rather
      than surfacing later as a mid-publish failure.
- [ ] The module is derived from the **targeted checkout's** origin remote, and
      a checkout whose forge is not `git.drupalcode.org` is refused with a
      message saying so.
- [ ] The confirmation from task 4 is printed in full, and publication proceeds
      only after an explicit affirmative. Declining publishes nothing and exits
      non-zero-free (a decline is not an error).
- [ ] On a non-TTY stdin, the command does **not** publish silently: it refuses
      unless an explicit confirmation flag was passed, and that flag's help
      text says it is the non-interactive form of the human confirmation.
      `land.go`'s `isTerminal` seam is reused for the TTY check.
- [ ] A `--allow-outside-issue-namespace`-style flag (name it in the codebase's
      idiom) is the only route past task 4's guard, and its help text says what
      it permits.
- [ ] Progress is streamed per commit, and the final report names which commits
      landed with their SHAs, which did not, and the first failure — printed
      from task 5's `Result` rather than re-derived.
- [ ] The command exists in the CLI dispatch alongside `land`, and
      `docs/using-sand/cli-reference.md`'s coverage is left to task 9 rather
      than duplicated here.
- [ ] Tests inject fakes for the publisher, the collector, and the VM lookup —
      following `cmd/sand/land_test.go`'s narrow-interface pattern — and cover:
      the decline path publishing nothing, the non-TTY refusal, the absent-PAT
      message, and the partial-failure report's text. No test spawns a VM or
      contacts the network.
- [ ] `go test ./cmd/... -race` passes and `go build ./...` succeeds.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Follow `cmd/sand/land.go` closely: narrow interfaces (`ghActions`,
  `vmRunningChecker`) declared at the consumer so the command is testable with
  fakes, `flag` for parsing, `text/tabwriter` where columns are printed.
- No publication logic in this file. Resolution, guard, payload, client, and
  result formatting live in `internal/drupalorg`.
- The confirmation prompt must read from stdin explicitly; it must never be
  satisfied by an environment variable.

## Input Dependencies

- Task 4: `Destination`, the guard, and the confirmation renderer.
- Task 5: `Publish` and its `Result`.
- Task 6: the guest change-set collector.

## Output Artifacts

- `cmd/sand/publish.go` — the CLI entry point.
- `cmd/sand/publish_test.go` — the confirmation, decline, non-TTY, and
  reporting tests.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

The plan's "The two surfaces" section is the contract: publication is exposed
"the way `land` already is: a CLI entry point at `cmd/sand/publish.go` and a
TUI action, both over one shared `internal/drupalorg` package that owns fork
resolution, the payload type, the client, and the guard. The surfaces differ
only in how they render the confirmation and stream progress; neither holds
publication logic of its own."

The confirmation requirement is success criterion 6 and is absolute:
"Publication cannot complete without explicit human confirmation that shows
what will change and where; declining publishes nothing, and no code path
publishes as a side effect of another command."

The non-TTY case needs a deliberate decision rather than a default. A pipe is
not a human, so a bare `sand publish < /dev/null` must not publish. An explicit
flag *is* a deliberate human act — someone typed it — so that is the sanctioned
non-interactive route, and its help text should say as much. `land.go` already
has the `isTerminal` seam (a package-level `var` so tests can force both
branches); reuse it rather than adding a second one.

The plan also notes publication being "a human-initiated act" is what keeps the
workflow clear of drupal.org's automation-and-bots provision: it is "an
individual action a user could already perform with a regular authenticated
session", not a bot. That is a reason the gate cannot be softened later, and it
belongs in a comment.

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

The decline path and the non-TTY refusal are the meaningful ones — they are the
tests that would catch someone later making publication the default.

</details>

### Per-task completion gate (required by the plan)

This task is not complete until, after its own tests pass:

1. `/code-review --fix` has run against this task's changes and every finding
   was applied or consciously rejected.
2. `/simplify` has then run, and its findings applied or consciously rejected.
3. The task's tests were **re-run** afterwards and pass.
