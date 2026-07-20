---
id: 11
group: "testing"
dependencies: [6, 7, 8, 9]
status: "completed"
created: 2026-07-20
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "This is the verification gate for the plan's central security claim (pool isolation); a weak or self-satisfied test here would rubber-stamp the whole feature."
skills:
  - go
  - e2e-testing
---
# testing: opt-in Proxmox e2e suite, including the pool-isolation proof

## Objective

Add a build-tagged, env-gated end-to-end suite that exercises the full Proxmox
CRUD lifecycle against a real host, plus an isolation test that proves the
pool-scoped token **cannot** affect a VM outside its pool.

## Skills Required

`go` (build tags, env gating, test skipping) and `e2e-testing` (lifecycle
sequencing, cleanup, resilience to slow operations).

## Acceptance Criteria

- [ ] `go test ./...` (no tags, no env) passes and **skips** the suite cleanly —
      verify with `go test ./internal/provider/ -run TestE2E -v` showing SKIP,
      not failure, and confirm `go vet ./...` is clean with and without the tag.
- [ ] `go test -tags proxmoxe2e ./internal/provider/` **compiles** even without a
      host configured — verify with
      `go vet -tags proxmoxe2e ./internal/provider/`.
- [ ] The suite skips with an explanatory message when `PROXMOX_E2E` is unset or
      any required `PROXMOX_E2E_*` variable is missing.
- [ ] With a host configured, the lifecycle test performs create → shell → reset
      → delete and leaves no VM behind (cleanup runs via `t.Cleanup`, including
      on failure).
- [ ] The **isolation test** creates nothing outside the pool itself; it takes an
      out-of-pool VMID from `PROXMOX_E2E_FOREIGN_VMID` and asserts that `Get`,
      `Stop`, and `Delete` against it each fail with a permission error, and that
      the VM is still present and still in its original power state afterwards.

## Technical Requirements

- Build tag `//go:build proxmoxe2e`, mirroring the existing `limae2e` convention.
- Env gate: `PROXMOX_E2E=1` plus `PROXMOX_E2E_HOST`, `PROXMOX_E2E_NODE`,
  `PROXMOX_E2E_POOL`, `PROXMOX_E2E_STORAGE`, `PROXMOX_E2E_BRIDGE`,
  `PROXMOX_E2E_TOKEN_FILE`, and optional `PROXMOX_E2E_FOREIGN_VMID`,
  `PROXMOX_E2E_INSECURE`.
- The isolation test **must skip** rather than fail when
  `PROXMOX_E2E_FOREIGN_VMID` is unset, and must never create or delete an
  out-of-pool VM itself.
- Generous timeouts — a base template build downloads a cloud image and runs the
  full Ansible playbook.
- **Do not** wire this into `.github/workflows/test.yml` as a running job. CI has
  no Proxmox host. It may be added as a `workflow_dispatch`-only job **if and
  only if** it is gated so a missing secret skips rather than fails.

## Input Dependencies

Tasks 6 (provisioning), 7 (stats), 8 (provenance), 9 (profile wiring).

## Output Artifacts

`internal/provider/proxmox_e2e_test.go` — the evidence source for the plan's
Self Validation steps 4 and 5.

## Implementation Notes

<details>

Model the file on `internal/provider/remote_e2e_test.go`, which is the
established template for a provider e2e suite: constants for the env var names,
a `skipUnlessConfigured(t)` helper, and `t.Cleanup` teardown.

```go
//go:build proxmoxe2e

package provider_test

const (
    envEnabled     = "PROXMOX_E2E"
    envHost        = "PROXMOX_E2E_HOST"
    envNode        = "PROXMOX_E2E_NODE"
    envPool        = "PROXMOX_E2E_POOL"
    envStorage     = "PROXMOX_E2E_STORAGE"
    envBridge      = "PROXMOX_E2E_BRIDGE"
    envTokenFile   = "PROXMOX_E2E_TOKEN_FILE"
    envForeignVMID = "PROXMOX_E2E_FOREIGN_VMID"
)
```

**Lifecycle test** (`TestE2EProxmoxLifecycle`): preflight → create a uniquely
named VM → assert it appears in `List` → `ShellOut` a trivial command and assert
its output → assert `HostResources` returns non-zero CPU and memory → `Reset` →
delete → assert it is gone. Register cleanup **immediately after** create so a
mid-test failure still removes the VM.

Use a name including a random-ish suffix derived from the test start time so
concurrent runs and leftovers from a failed run do not collide.

**Isolation test** (`TestE2EProxmoxPoolIsolation`) — this is the one that proves
the plan's central security claim, so write it to be genuinely adversarial rather
than confirmatory:

```go
// This test proves the negative: the pool-scoped token must be UNABLE to touch
// a VM outside its pool. It deliberately operates on a VM it did not create and
// must not clean up — the operator supplies the VMID via PROXMOX_E2E_FOREIGN_VMID.
//
// Every assertion must check for a PERMISSION error specifically. A test that
// merely asserts "an error occurred" would pass just as happily if the VMID did
// not exist, proving nothing at all.
```

For each of `Get`, `Stop`, and `Delete` against the foreign VMID: assert the
error is non-nil **and** that `pve.IsPermission(err)` is true. Then, using an
out-of-band admin check or a second read, assert the VM still exists and its
power state is unchanged.

If obtaining an admin-scoped verification client is impractical, at minimum
assert the permission errors and document in a comment that the "still exists"
half is verified manually in the docs' verification step. Do not silently drop
it — an unverified half should be visible, not invisible.

**Recording setup.** At the top of the file, document the exact env var block an
operator needs, so the docs page (task 12) and this file agree. Cross-check the
two before finishing.

### Test philosophy: "write a few tests, mostly integration"

Meaningful tests verify custom business logic, critical paths, and edge cases
specific to this application. Test *your* code, not the framework or library.

**When TO write tests:**

- Custom business logic and algorithms.
- Critical user workflows and data transformations.
- Edge cases and error conditions for core functionality.
- Integration points between components.
- Complex validation logic or calculations.

**When NOT to write tests:**

- Third-party library functionality.
- Framework features.
- Simple CRUD operations without custom logic.
- Trivial getters/setters or static configuration.
- Obvious functionality that would break immediately if incorrect.

**Test task creation rules:**

- Combine related test scenarios into a single test rather than splitting per
  operation (one lifecycle test, not separate create/shell/reset/delete tests).
- Favor integration and critical-path coverage over per-method unit tests.
- Avoid one test per CRUD operation.
- Question whether simple functions need a dedicated test.

Applied here: this suite is deliberately **two** tests — one full lifecycle and
one isolation proof — not a matrix of per-endpoint checks. The per-endpoint
encoding and semantics are already covered by the mock-server tests in tasks 1,
3, and 4, where they run in CI for free. Do not duplicate them against a real
host, where they would be slow and flaky without adding signal.

</details>
