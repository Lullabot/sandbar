---
id: 2
group: "tier1-tests"
dependencies: []
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "high"
skills:
  - go-testing
---
# Tier-1: lock in the managed-VM recreate gate (internal/manage + cmd/sand refusal)

## Objective
Author unit tests for the drift-guard that decides which VMs are sand-managed and recreate-able — the gate that stops `sand create --recreate` from clone-replacing a VM sand did not create. Cover it at the package level (`internal/manage`, currently 0% with no test file) and at the CLI integration level (`cmd/sand` `doHeadlessCreate` refusal). Test-only; no production code edits.

## Skills Required
`go-testing` — stdlib table-driven tests against `internal/registry` + `internal/vm` and the `cmd/sand` `stubProvisioner` seam.

## Acceptance Criteria
- [ ] New `internal/manage/manage_test.go` covers `Reconcile` (drops entries whose VM is absent from the live list and returns the dropped names), `RecordSuccess` (records a `CreateConfig` retrievable from the registry), and `RecreateBase` (returns `ok=false` for an unmanaged VM; for a managed VM returns the recorded base, and the default base name when none was recorded).
- [ ] A new test in `cmd/sand/create_test.go` drives `doHeadlessCreate(..., recreate=true, ...)` against a registry with **no** managed entry for the target and asserts (a) the returned error contains "recreate refused" and (b) the `stubProvisioner` was **not** invoked (no `CreateVMWithOptions`/`RecreateWithOptions` call).
- [ ] Verification: `go test ./internal/manage/ ./cmd/sand/ -run 'Reconcile|RecreateBase|RecordSuccess|Refus|Recreate' -v` passes; `go test ./internal/manage/ -cover` reports > 0% (was 0.0%).
- [ ] No production `.go` files changed (only `*_test.go`).

## Technical Requirements
- `internal/manage/manage.go`: `Reconcile(reg, live)`, `RecreateBase(reg, name)`, `RecordSuccess(reg, cfg)`.
- `cmd/sand/create.go`: `doHeadlessCreate(ctx, reg, prov headlessProvisioner, cfg, recreate, rebuild, out)` returns the "not a sand-managed VM — recreate refused" error when `RecreateBase` reports `ok=false`.
- Reuse the existing `stubProvisioner` in `cmd/sand/create_test.go`; extend it to record whether it was called if it does not already.

## Input Dependencies
None.

## Output Artifacts
`internal/manage/manage_test.go`; added test(s) in `cmd/sand/create_test.go`.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- **Test philosophy (write a few tests, mostly integration):** verify custom business logic, critical paths, and edge cases specific to this app — test our code, not the framework/stdlib. Combine related scenarios into one task; favor integration/critical-path over per-method units; don't test trivial getters or stdlib behavior.
- Build registries with `registry.NewEmpty()` and populate via `reg.Add(cfg)` / `RecordSuccess`. Use `vm.CreateConfig{Name, BaseName, ...}` and `vm.VM{Name: ...}` for the live list.
- `RecreateBase` default-base branch: record a managed VM whose config has an empty `BaseName` and assert `RecreateBase` returns `vm.DefaultCreateConfig().BaseName` with `ok=true`.
- For the refusal test: `stubProvisioner` must expose a "called" flag. If the current struct only records rebuild opts, add a boolean it sets in `CreateVMWithOptions`/`RecreateWithOptions`; assert it stays false after the refused call.
- Do NOT test the `--rebuild` base-delete path — it was removed from `doHeadlessCreate` in 0.4.0, and the `--rebuild`/`--recreate` pass-down is already covered by `TestHeadlessCreatePassesRebuildDownToTheProvisioner` / `TestHeadlessRecreatePassesRebuildDownToTheProvisioner`.
- No `t.Parallel()` (the suite is deliberately serial; see AGENTS.md conventions from task 9).
</details>
