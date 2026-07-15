---
id: 6
group: "e2e-tests"
dependencies: []
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "high"
skills:
  - go-testing
  - lima
---
# E2E: headless `sand create` outcome + `--recreate` round-trip (limae2e-gated)

## Objective
Formalise the cross-process claims the `lima-e2e` CI job checks informally into asserted Go tests, gated behind the `limae2e` build tag: (1) a headless `sand create` produces a VM recorded as managed, and (2) a `--recreate` round-trip proves the managed-gate holds against a real registry (a sand-created VM can be recreated; `--recreate` is refused for a VM sand did not create). Test-only.

## Skills Required
`go-testing`, `lima` — real-VM test authoring with the `//go:build limae2e` tag and existing e2e teardown discipline.

## Acceptance Criteria
- [ ] New `//go:build limae2e` test(s) (e.g. `cmd/sand/create_e2e_test.go` or under an existing e2e package) that skip unless `LIMA_E2E=1`, matching the existing e2e gating.
- [ ] The create test shells the built `sand` binary (or drives the same entrypoint) to create a small VM and asserts it exists in `limactl list` **and** is recorded managed in the registry.
- [ ] The recreate test asserts (a) a sand-created VM can be recreated via `--recreate`, and (b) `--recreate` against a VM not in the managed registry is refused with the "recreate refused" error and does not replace it.
- [ ] Unconditional teardown removes any VM the test created (deferred `limactl delete --force`), matching the existing e2e tests.
- [ ] Verification: `go vet -tags limae2e ./...` passes and the tests compile; with Lima available, `LIMA_E2E=1 go test -tags limae2e ./cmd/sand/ -run E2E` passes (document that this runs in the `lima-e2e` CI job, not the fast `unit` job).
- [ ] No production `.go` files changed.

## Technical Requirements
- Follow the existing e2e pattern: `//go:build limae2e` + `if os.Getenv("LIMA_E2E") == "" { t.Skip(...) }` (see `internal/provision/lima_e2e_test.go`, `internal/ui/lima_e2e_test.go`).
- Build the binary with `go build -o sand ./cmd/sand` (the CI `lima-e2e` job already does this) or invoke `runCreate`/`doHeadlessCreate` with a real `lima.Client`.
- Use tiny resources (few vCPU, minimal memory/disk) and unique instance names to avoid collisions; reuse the base image if present.

## Input Dependencies
None (behaviorally related to task 2's managed-gate, but independently authored).

## Output Artifacts
New `limae2e`-tagged e2e test file(s) exercising create + recreate.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- **Test philosophy (write a few tests, mostly integration):** verify custom business logic, critical paths, and edge cases specific to this app — test our code, not the framework/stdlib. These ARE the integration tests — keep them to the two critical paths (create-records-managed, recreate-gate-holds), not an exhaustive matrix.
- Keep both scenarios in one file/one build-tagged package to share the teardown helper. Register cleanup with `t.Cleanup`/`defer` that force-deletes the instance even on failure.
- The `lima-e2e` CI job (`.github/workflows/test.yml`) is where these run in CI; do not add them to the fast `unit` job. You may add a step to the existing `lima-e2e` job that runs `LIMA_E2E=1 go test -tags limae2e ./cmd/sand/ -run E2E`, but do not create a new workflow.
- Assert managed state by loading the registry the same way `cmd/sand` does and checking `IsManaged`.
</details>
