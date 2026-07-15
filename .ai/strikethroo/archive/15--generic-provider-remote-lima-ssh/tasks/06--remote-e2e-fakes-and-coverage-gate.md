---
id: 6
group: "verification"
dependencies: [3, 5]
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "high"
skills:
  - go
  - integration-testing
complexity_score: 7
complexity_notes: "Verification/quality gate (risk floor applies): backend-agnostic fakes, a real-remote E2E for the new provider, and holding the inherited -race + 87% coverage floor."
---
# Remote-provider E2E, backend-agnostic fakes, and the coverage-floor gate

## Objective
Prove the refactor is non-regressive and the remote provider works, under the CI
gates inherited from the `worktree-refine-plan-08-tests` anchor. Add
backend-agnostic fakes for the `Provider` and host-access seam, an E2E for the
remote provider analogous to `cmd/sand/create_e2e_test.go`, and confirm the suite
stays `-race`-clean and at or above the 87% coverage floor over `./internal/...`.

## Skills Required
`go`, `integration-testing` (fakes, build-tag-gated E2E, coverage measurement).

## Acceptance Criteria
- [ ] Backend-agnostic fakes exist for `provider.Provider` and the host-access seam, usable by consumer tests (`internal/ui`, `internal/browse`, `cmd/sand`) without a real `limactl` or a real remote host.
- [ ] A remote-provider E2E, gated behind a build tag in the `limae2e` family (e.g. a loopback-SSH tag), exercises create → shell (assert `main` tmux survives detach) → copy-and-read-back → list → stop → delete against a real remote/loopback target, and asserts local and remote lists do not show each other's instances.
- [ ] Existing behaviour-locking tests from plan 08 (`manage`, `baselock`, `secrets`, `transfer`) and the `teatest` goldens pass unchanged.
- [ ] **Verification**: `go test ./... -race` passes; running the coverage command from `.github/workflows/test.yml` — `go test ./... -race -covermode=atomic -coverpkg=./internal/... -coverprofile=coverage.out` then `go tool cover -func=coverage.out | grep total` — reports total ≥ **87%**; the new E2E runs green under its build tag on this KVM host.

## Technical Requirements
- Follow `AGENTS.md` testing conventions: no unit test may require a real `limactl` or write host state; use `isolateHostState`-style isolation of `XDG_DATA_HOME` and `LIMA_HOME`.
- The E2E must be skipped by plain `go test ./...` (build-tag gated), like the existing `limae2e` tests.
- No `t.Parallel()` — keep the suite serial per the repo convention.

## Input Dependencies
- Task 3: consumers on the interface (so fakes can drive them).
- Task 5: the working remote provider (the E2E target).

## Output Artifacts
- Reusable `Provider`/host-access fakes.
- A remote-provider E2E and a coverage-floor-clean suite.

## Implementation Notes
<details>
<summary>Test philosophy — "write a few tests, mostly integration"</summary>

Meaningful tests verify *this app's* custom logic and critical paths, not the
framework or library. WRITE tests for: custom business logic and algorithms;
critical user workflows and data transformations; edge cases and error
conditions for core functionality; integration points between components; complex
validation. Do NOT write tests for: third-party library behaviour; framework
features; simple CRUD without custom logic; trivial getters/setters; obvious
functionality that would break immediately if wrong.

Here that means: cover the provider seam's critical paths (argv construction for
local vs SSH, the two-stage copy topology, the attach-argv invariants, the
registry migration) and the genuine integration behaviours end-to-end; do NOT
add per-method unit tests for trivial pass-through delegations or re-test
`limactl` itself. Combine related scenarios into one task/test rather than one
test per operation. Favour the integration/E2E path over exhaustive unit
coverage of glue code.
</details>

<details>
<summary>Detailed guidance</summary>

The `Provider` interface (task 2) makes a `fakeProvider` straightforward — one
struct with function fields per method — that `internal/ui` and `cmd/sand` tests
drive instead of a real client. Keep the existing runner-level fakes for the
local provider's own tests (they already exist per `AGENTS.md`).

For the remote E2E, mirror `cmd/sand/create_e2e_test.go`'s structure but target
the remote provider against `ssh localhost` (this dev box has Lima+KVM). Assert
the attach invariant the same way the local attach tests do: after attach+detach,
`main` is still present. Prove copy placement by copying a sentinel file and
reading it back from the guest.

Defend the coverage floor per-package as you go; the SSH argv-building and the
migration are unit-testable without infrastructure, so most of the new code is
covered by fast tests, with only the genuinely integration-only behaviours behind
the build tag. If total dips below 87%, add unit coverage for the pure argv/topology
functions rather than loosening the floor.
</details>
