---
id: 7
group: "testing"
dependencies: [3, 4, 5]
status: "pending"
created: 2026-07-09
model: "sonnet"
effort: "high"
skills:
  - go
  - integration-testing
---
# End-to-End Tests — Multi-Token, Live Rotation, Recreation Persistence

## Objective
Add gated end-to-end tests (real Lima VM) that verify the load-bearing, business-logic behaviours of the secrets manager: secrets survive VM recreation, two GitHub tokens coexist in one VM selected by directory, and rotating a token takes effect live. Follow "write a few tests, mostly integration" — one focused e2e task covering the critical paths, not per-scenario unit sprawl.

## Skills Required
- **go** — a build-tag/`-run`-gated e2e test in the style of the existing `internal/provision/lima_e2e_test.go`.
- **integration-testing** — drive `sand` create/secret/sync + `limactl shell` and assert observable outcomes.

## Acceptance Criteria
- [ ] The e2e test is gated like the existing Lima e2e (build tag or `-run` guard) so `go test ./...` without the gate stays fast, and the gated run passes on this KVM-capable host.
- [ ] **Recreation persistence**: set a global secret + a directory-scoped GitHub token, recreate the VM, then assert (in a fresh `limactl shell`) the global var is present and the per-scope credential file exists — with no re-entry of values.
- [ ] **Multi-token in one VM**: configure two org-scoped GitHub tokens; from each org's checkout, assert `git` resolves to that org's credential (e.g. inspect `git config --show-origin credential.helper` / the effective credential file per directory).
- [ ] **Live rotation**: in one already-open shell, rotate an org's token via `sand secret set --github` + `sand secret sync`, then run the next `git`/`gh` call in the same shell and assert it uses the new token (no new shell).
- [ ] **Legacy create parity**: `sand create --clone-url <private-repo> --clone-token <T>` clones the private repo successfully.

## Technical Requirements
- Reuse the gating and helper patterns from `internal/provision/lima_e2e_test.go` (per project memory, real Lima VMs boot on this host; run the gated test rather than trusting a "no nested virt" assumption).
- Prefer asserting on files/`git config` resolution over network calls where a private repo/token isn't available in CI; where a real private repo is needed, guard behind an env-provided token so the test skips cleanly when absent.
- Test the manager's own logic (rotation, scoping, persistence), not git/direnv internals.

## Input Dependencies
- Task 3: `sand secret` CLI (set/list).
- Task 4: provisioning integration + `--clone-token` reshape.
- Task 5: `sand secret sync`.

## Output Artifacts
- A gated e2e test file covering the four scenarios above.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

**Test philosophy — "write a few tests, mostly integration."** Meaningful tests verify custom business logic, critical paths, and edge cases specific to this application — test *your* code, not the framework. Write tests for: the manager's rotation/scoping/persistence logic and the create/reset integration. Do **not** write tests for git's `includeIf`, direnv, or `gh` internals themselves, nor per-CRUD unit tests for the store (task 1 already covers store round-trips). Combine the related scenarios into this single integration task rather than splitting per scenario.

Structure: gate with the same mechanism as `lima_e2e_test.go`. Create a throwaway VM, drive `sand secret set`/`sync`/`create` as subprocesses (or the underlying Go entry points), and assert via `limactl shell` commands. For multi-token, create two `~/github.com/<org>/<repo>` dirs (or stub git repos) and assert per-dir credential resolution. For live rotation, hold one shell/session, rotate + sync, and assert the next git invocation in that same session authenticates with the new token — the key property the file-backed credential helper provides. Skip cleanly (with a clear message) when a required private repo/token env var is absent, so the test never silently passes without exercising the path.
</details>
