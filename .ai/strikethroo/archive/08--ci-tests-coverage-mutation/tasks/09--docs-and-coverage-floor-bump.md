---
id: 9
group: "finalize"
dependencies: [1, 2, 3, 4, 5]
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "medium"
skills:
  - technical-writing
  - github-actions
---
# Finalize: bump the coverage floor + document testing conventions

## Objective
After the new unit tests raise `./internal/...` coverage, bump the committed coverage floor to just under the new measured total, and document the testing/coverage/mutation conventions in README and AGENTS.md — including the two style cautions and the test/CI-only no-app-code rule. Docs + a one-value CI edit.

## Skills Required
`technical-writing` (README/AGENTS.md), `github-actions` (the floor value edit).

## Acceptance Criteria
- [ ] Re-measure `./internal/...` coverage (`go test ./... -race -covermode=atomic -coverpkg=./internal/...`) after tasks 2–5 land, and set `COVERAGE_FLOOR` in `test.yml` just under the new total; the `unit` job stays green.
- [ ] README gains a testing section: how to run unit tests, `-race`, coverage (and where the artifact is / how the floor is set and bumped), and how to run gremlins locally.
- [ ] AGENTS.md records: the **test/CI-only, no-app-code** convention; the coverage-floor expectation for new code; the `limae2e` build tag for real-VM tests; and the two style cautions — (a) concurrency tests are timing-based, prefer channel/barrier determinism if the concurrency model changes; (b) **no `t.Parallel()`** on tests touching the package-level function-var seams (`hostMemBytesFn`, `playbookVersionFn`, `buildVersion`).
- [ ] Role/test docs mention how to run the `base`/`samba` molecule scenarios, the systemd-image requirement, and the deferred roles.
- [ ] Verification: `go test ./... -race -covermode=atomic -coverpkg=./internal/...` prints a total ≥ the new floor; `grep -n COVERAGE_FLOOR .github/workflows/test.yml` shows the bumped value; README and AGENTS.md contain the new sections.

## Technical Requirements
- The floor lives in `test.yml` from task 1; this task bumps it once, in-repo (never auto-committed by CI).
- README is at the repo root; AGENTS.md at the repo root.

## Input Dependencies
Task 1 (coverage gate + floor to bump), tasks 2–5 (the unit tests that raise coverage). Soft: tasks 6–8 (their local-usage notes feed the docs).

## Output Artifacts
Bumped `COVERAGE_FLOOR`; testing sections in README, AGENTS.md, and role/test docs.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- Run the coverage command after the unit-test tasks merge; set the floor a point or two under the measured total (leave a little slack so an unrelated PR doesn't trip it, but keep the ratchet meaningful).
- AGENTS.md is AI-facing — state the conventions as rules future agents must preserve through the upcoming refactor. Keep it concise and imperative.
- Do not touch production Go source. The only code-adjacent edit is the `COVERAGE_FLOOR` value in the workflow.
</details>
