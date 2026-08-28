---
id: 5
group: "drupalorg-publish"
dependencies: [1, 2, 3, 4]
status: "pending"
created: 2026-08-28
model: "opus"
effort: "xhigh"
skills:
  - go
  - rest-api
complexity_score: 9
complexity_notes: "The only task that writes publicly and permanently. Commit replay, last_commit_id chaining, partial-failure reporting and resumption interact, and every failure mode it gets wrong leaves an unrevocable artefact on a public forge."
---
# Authenticated publish: commit replay, merge request, partial failure and resumption

## Objective

Publish a change set by replaying **each local commit as its own**
`POST /projects/<fork>/repository/commits`, then open a merge request against
the canonical parent if and only if none is already open. Report precisely what
landed when a replay fails partway, and make a re-run complete the remainder
rather than duplicating what is already on the branch.

## Skills Required

`go` and `rest-api`: authenticated GitLab content-API calls, sequential
consistency via `last_commit_id`, and a result type that describes a partial
public write honestly.

## Acceptance Criteria

- [ ] `Publish(ctx, dest, changeSet)` replays commits in order, one
      authenticated `POST /projects/<forkEnc>/repository/commits` per commit,
      each carrying `branch`, the commit's `commit_message`, its `author_name`
      and `author_email`, and its `actions` array mapped from the payload's
      file actions (create/update/delete/move, `content` plus `encoding` for
      the kinds that carry one).
- [ ] `start_branch` is sent **only** when the target branch does not exist,
      and then starts from the fork's own `default_branch`. Branch existence is
      resolved **anonymously, before any authenticated call**. A test covers
      both arms and asserts `start_branch` is absent in the common case.
- [ ] `last_commit_id` is sent on update actions and chained across the replay:
      each call's expected parent is the commit the previous call created. A
      conflict is returned as a distinct, `errors.Is`-checkable "re-derive and
      retry" outcome, not a generic failure.
- [ ] Per-call size is checked **before sending**: a commit whose encoded body
      exceeds the 300 MB rejection threshold is refused with an explanation,
      and one above the 20 MB rate-limit threshold is reported as such. The
      check is per call, so a large change set may be publishable as a replay
      where it would not be as one commit — say so in the message.
- [ ] The merge request call is `POST /projects/<forkEnc>/merge_requests` with
      `target_project_id` set to the canonical parent's ID and `target_branch`
      set to the parent's branch — both from the `Destination`, never from the
      payload. It is **skipped** when an open merge request already exists for
      the source branch.
- [ ] A `Result` type names, for a run: which commits landed (with their
      returned SHAs), which did not, and the first failure and its cause. A
      partial result is never presented as a clean failure or a clean success.
- [ ] Publication is **resumable**: before replaying, the branch's existing
      commits are read anonymously and commits already present are skipped, so
      re-running the same publish lands only the remainder and leaves no
      duplicates. A test fails an interior call of a three-commit replay,
      asserts the report, then re-runs against the mutated fixture state and
      asserts exactly the remaining commits are sent.
- [ ] A drupal.org edge refusal during publication is reported through task 3's
      refusal error, not as a generic API error.
- [ ] The token is read through task 2's loader at the moment of the first
      authenticated call, and its value never appears in an error, a log line,
      or a request URL. It is sent as a `PRIVATE-TOKEN` header.
- [ ] All tests run against `httptest.Server`; no test contacts
      git.drupalcode.org or performs a real write. `go test ./internal/drupalorg/... -race`
      passes.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- `net/http` only. No `glab`, no `gh`, no git, no shell-out — success criteria
  4 and 15.
- No rollback is attempted, ever. The plan is explicit that there is none and
  that the design "must not pretend otherwise".
- Resumption identity: decide and document how an already-landed commit is
  recognised (commit message plus author plus tree effect is the pragmatic
  option, since a replayed commit's SHA differs from the guest's local SHA).
  Whatever the rule, it must be stated in a comment and covered by the test —
  a wrong rule here either duplicates public commits or silently skips work.

## Input Dependencies

- Task 1: the payload types and the size helper.
- Task 2: the PAT loader and its `ErrNoToken` sentinel.
- Task 3: the client, anonymous branch/commit reads, the parent lookup, the
  open-MR lookup, and the edge-refusal error.
- Task 4: the `Destination`, already guarded.

## Output Artifacts

- `internal/drupalorg/publish.go` — the replay, the merge request, the result
  type, and the resumption logic.
- `internal/drupalorg/publish_test.go` — the branch-exists/absent arms, the
  interior-failure-then-resume test, the conflict outcome, the size refusal,
  and the MR skip.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

The plan's "The publish call" section is the contract. Read it in full before
starting; four of its statements corrected earlier defects.

**One call is one commit.** Refinement 7 decided replay over squashing, so a
publish preserves the guest's history and messages on the fork. That decision
brought a cost the plan accepted knowingly: "N sequential calls means call 2 of
3 can fail with call 1 already public and unrevocable. There is no rollback and
the design must not pretend otherwise."

**The branch usually already exists.** drupal.org creates the issue branch
alongside the fork — `issue/drupal-3181657` already carries
`3181657-fix-views-timezone` and `3181657-test-only`. `start_branch` against an
existing branch is an **error**, so the normal path omits it. An earlier
revision described publication as "one call creates the branch and lands the
change set", which is the uncommon case.

**The merge request is cross-project**, verified on `project/dubbot`:
`source_project_id` 241450 -> `target_project_id` 106348, `target_branch`
`2.x`. Listing MRs on the issue fork returns `[]` because forks hold none.
Publishing more commits to an open MR must not attempt to open a second one.

**Partial failure is a first-class outcome, not an error path.** The result
must "name exactly which commits landed, which did not, and why the first
failure happened", and re-running must complete the remainder. Model it as a
value returned alongside an error, not as an error alone — an error alone
cannot carry "commit 1 landed as abc123".

Request body shape:

```json
{
  "branch": "3181657-fix-views-timezone",
  "commit_message": "...",
  "author_name": "...",
  "author_email": "...",
  "actions": [
    {"action":"create","file_path":"src/Foo.php","content":"...","encoding":"text"},
    {"action":"delete","file_path":"src/Old.php"},
    {"action":"move","file_path":"new/path","previous_path":"old/path"},
    {"action":"update","file_path":"README.md","content":"...","encoding":"text","last_commit_id":"<sha>"}
  ]
}
```

`start_branch` is added only for the create case.

Three content-API behaviours differ observably from a git push and belong in
comments so nobody rediscovers them as bugs: commits created this way are
**not GPG-signed**; `last_commit_id` is the **only** concurrency guard if the
fork moved underneath; and requests are rate-limited above 20 MB and rejected
above 300 MB.

For the tests, an `httptest` handler holding fixture branch state — a list of
commits it "has" — lets the same handler serve the anonymous reads, accept
commits, and be made to fail the second call. That one fixture covers the
replay, the failure, and the resume without three separate scaffolds.

**Test philosophy — write a few tests, mostly integration.** Meaningful tests
verify custom business logic, critical paths, and edge cases specific to this
application. Test *your* code, not the framework or library. Write tests for:
custom business logic and algorithms; critical user workflows and data
transformations; edge cases and error conditions for core functionality;
integration points between components; complex validation logic. Do **not**
write tests for: third-party library functionality; framework features; simple
CRUD without custom logic; trivial getters/setters; obvious functionality that
would break immediately if incorrect. Combine related scenarios into one test
rather than one per method; favour integration and critical-path coverage.

This task is nearly all critical path, so it earns more test surface than any
other in the plan — but keep it to the scenarios above rather than one test per
request field.

</details>

### Per-task completion gate (required by the plan)

This task is not complete until, after its own tests pass:

1. `/code-review --fix` has run against this task's changes and every finding
   was applied or consciously rejected.
2. `/simplify` has then run, and its findings applied or consciously rejected.
3. The task's tests were **re-run** afterwards and pass.
