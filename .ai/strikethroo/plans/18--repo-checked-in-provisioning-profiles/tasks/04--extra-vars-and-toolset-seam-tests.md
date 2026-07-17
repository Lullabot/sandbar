---
id: 4
group: "verification"
dependencies: [2, 3]
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "high"
skills:
  - go-testing
complexity_score: 7
complexity_notes: "Verification-gate task: risk floor applies regardless of scope — this is the regression guard proving no-profile repos and the restructured toolset defaults are unaffected."
---
# Add the no-profile regression and restructured-toolset-defaults seam tests

## Objective

Add Go-level seam tests, run in the per-PR blocking `unit` CI job, proving two things the plan requires as explicit success criteria: (1) `provision.BuildExtraVars` produces byte-identical output for repositories without `.sandbar/`, in both base and finalize phases, after Tasks 2 and 3 land; and (2) the restructured shipped-profile toolset defaults (Task 2) still produce the same templated Ansible output (`toolset_packages`, `devtools_ddev_packages`, etc.) as before the restructuring.

## Skills Required

Go testing — table-driven tests, and (following the existing convention in `internal/provision/toolset_packages_test.go`) driving real `ansible-playbook`/Jinja templating from Go rather than reimplementing template logic.

## Acceptance Criteria

- [ ] A test (extending `internal/provision/vars_test.go` or added alongside it) asserts `provision.BuildExtraVars` output for a no-`.sandbar/` scenario is unchanged from pre-Task-2/3 behavior, for both `phase == "base"` and `phase != "base"` — assert on the full byte output or an equivalent structural comparison, not just "no error".
- [ ] A test extending `internal/provision/toolset_packages_test.go`'s `resolveBasePackages`-style approach (running real role defaults through Ansible's Jinja engine) confirms the restructured shipped profiles produce identical `toolset_packages`/`devtools_ddev_packages` output to the pre-restructuring baseline for the four tools (`claude`, `ddev`, `go`, `java`).
- [ ] Both tests run under `go test ./internal/provision/... -race` and pass.
- [ ] `go test ./... -race -covermode=atomic -coverpkg=./internal/... -coverprofile=coverage.out` passes with coverage at or above the existing 87% floor.
- [ ] The tests are placed to run in the existing `unit` CI job (`.github/workflows/test.yml`) with no new job or `if:` guard needed — confirm by inspecting the workflow file, no changes to `.github/workflows/test.yml` should be required for these two tests to run per-PR.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- This task depends on Tasks 2 and 3 being complete: it tests the *result* of the restructuring and the new finalize stage's non-effect on no-profile repos, not the mechanisms themselves.
- Do not duplicate Task 1's validator-corpus test (that is a separate concern — the validator's own correctness) or Task 3's finalize-stage Ansible-level checks. This task's scope is specifically the two Go-level seam tests named above: extra-vars byte-identity and restructured toolset-default parity.

## Input Dependencies

- Task 2's restructured shipped profiles (to compare templated output against).
- Task 3's finalize stage (to confirm it introduces zero extra-vars for no-profile repos).

## Output Artifacts

Two Go seam tests in `internal/provision/`, part of the per-PR blocking `unit` job's coverage, serving as the plan's Success Criteria #3 regression guard ("Repositories without `.sandbar/` behave exactly as today").

## Implementation Notes

Test philosophy: write a few tests, mostly integration. This task exists precisely because the plan calls out extra-vars byte-identity and toolset-default parity as their own named verification-surface rows — do not skip it or fold it silently into Tasks 2/3's own acceptance criteria; keep it as an explicit, separately-reviewable regression guard.

Follow RED → GREEN → REFACTOR: first confirm these tests would have caught a real regression by temporarily introducing one (e.g. add a stray extra-var) locally, confirming the test fails, then revert and confirm green — this validates the test actually exercises what it claims to, rather than trivially passing.
