---
id: 12
group: "tests"
dependencies: [7, 8, 9, 10]
status: "pending"
created: 2026-07-15
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Cross-cutting verification gate spanning the whole fleet — same-name isolation, zero-config parity, self-heal, coverage floor. Risk floor applies (verification gate)."
skills:
  - go
  - integration-testing
---
# Fleet-wide integration & regression tests and coverage-floor gate

## Objective
Add the cross-cutting integration/regression tests that exercise the whole fleet
end-to-end (which cannot live inside any single implementation task), and confirm
the suite passes with `-race` at or above the 87% coverage floor with **no unit
test requiring a real limactl/ssh target**.

## Skills Required
- `go` — teatest + golden harness, `providerfake` scenarios.
- `integration-testing` — cross-package regression coverage; this repo uses Go's `testing` + teatest (no JS test runner).

## Acceptance Criteria
- [ ] **Zero-config parity:** with no `profiles.yaml`, the store seeds one enabled Local profile and the board/header/create-form render **behaviorally identical** to the pre-profiles local path (assert via teatest/golden).
- [ ] **Two-profile aggregation:** with a Local + a RemoteSSH (fake) profile, tiles from **both** appear, each labelled by profile, and the status bar shows **two** host-stats bands.
- [ ] **Unreachable-remote resilience + self-heal:** a blocking/failing remote leaves local tiles interactive, contributes no tiles, shows a banner; a fail-then-succeed fake shows the errored profile's retry **backs off** and its tiles appear **automatically** on recovery.
- [ ] **Same-name coexistence & secrets isolation (the HIGH-severity regression):** enable two profiles, create same-named VMs on each with distinct secrets; both render with independent secrets/heartbeats; deleting one leaves the other's secrets/heartbeat **untouched**. Plus: a **pre-fleet (v2) secrets file loads intact as local-scoped**.
- [ ] **Block-until-idle gate:** with a job in flight on a profile, disable/delete is refused naming the job, and succeeds once the job clears.
- [ ] `go test ./... -race` passes and the coverage check meets/exceeds **87%** (`.github/workflows/test.yml` `COVERAGE_FLOOR=87`). Confirm locally with the same coverage invocation CI uses.
- [ ] **No unit test requires a real limactl/ssh target** (real backends only behind the `limae2e` tag); grep confirms new tests use `providerfake`/fixtures.

## Technical Requirements
- Files: `internal/ui/*_test.go` (teatest + goldens under `internal/ui/testdata/`), `internal/secrets/*_test.go` (v2 fixture), plus any cross-package harness. Uses `internal/providerfake`.
- Reproduce CI's coverage command over `./internal/...` and check the floor.

## Input Dependencies
- Tasks 7, 8, 9, 10: the full fleet UI must exist to exercise these flows.
  (Task 2's migration and task 6's prune-safety are exercised here at integration level; task 1's profiles tests and per-task unit tests already exist.)

## Output Artifacts
- Integration/regression suite covering the plan's success criteria + a green coverage gate.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- **Test philosophy — "write a few tests, mostly integration"** (restated per the
  skill): meaningful tests verify *this app's* custom logic and critical paths —
  fleet aggregation, per-profile async, the same-name secrets-isolation regression,
  the zero-config parity, the v2→v3 secrets migration, and the idle gate. Do NOT
  add per-method unit tests for framework/library behavior or trivial getters. Unit
  tests for each package already live in their implementation tasks (1, 2, 6, …);
  this task adds only the cross-cutting integration/regression coverage that needs
  the whole fleet assembled.
- **Drive everything with `providerfake`** (func-field double) and teatest — no real
  backend. For "blocking remote", give a fake provider a `ListFunc` that blocks;
  for "fail-then-succeed", a fake that errors N times then returns a list.
- **Same-name regression** is the plan's HIGH data-loss guard: two scopes, same VM
  name, distinct secrets; delete one; assert the other's secrets/heartbeat survive.
  This complements task 6's lower-level test at full-fleet level.
- **Coverage.** If new code dips below 87%, add targeted tests to the thinnest
  areas rather than padding — the floor is over `./internal/...`.
- Regenerate any goldens the earlier UI tasks did not, and keep them deterministic
  (fixed sizes, fixed fake data).
</details>
