---
id: 5
group: "e2e-fixture"
dependencies: [2, 3]
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "high"
skills:
  - go-testing
  - ansible
complexity_score: 8
complexity_notes: "Real-VM integration test with new fixture-serving machinery (local git remote); proves the entire feature path end to end and is the plan's authoritative per-PR blocking guard for the finalize stage."
---
# Build the fixture repo and extend `lima-e2e` with the full clone-URL path

## Objective

Create a checked-in fixture repository (with a committed `.sandbar/` profile) and extend the per-PR blocking `lima-e2e` CI job so it exercises the complete path: `sand create --clone-url <fixture>` → clone → `.sandbar/` discovery → validation → package install → toolset reconciliation → role inclusion → service enablement → seed execution, asserted on real guest state. Also cover the toolset-reconciliation and malformed-manifest checks from the plan's Self Validation section, since they reuse the same fixture/serving infrastructure.

## Skills Required

Go testing (extending the `limae2e`-tagged suite, e.g. `cmd/sand/create_e2e_test.go`'s conventions) and Ansible/Lima familiarity (asserting guest state via `limactl shell` / `cli.ShellOut`).

## Acceptance Criteria

- [ ] A fixture directory is checked into the sandbar repo (e.g. under a `testdata/fixtures/` path) containing a committed `.sandbar/` profile that declares: one apt package not present in the base (e.g. `cowsay`), one systemd service, one custom role (from `.sandbar/roles/`), one shipped toolset tool absent from the fixture's base build, and a seed tasks file that writes a marker file into the project tree.
- [ ] At test time, the fixture is served as a local git remote whose URL matches `roles/project/tasks/main.yml`'s `scheme://host/org/repo` parsing (e.g. `git daemon` over `git://localhost/<org>/<repo>`, or git-http-backend) — a raw `file://` path must not be used, since it breaks the `project` role's host/org derivation. Prefer `localhost` over a real host name so the fixture stays off the token-injection branch.
- [ ] The extended e2e test (in the `cmd/sand` `limae2e` suite or a clearly-related new file following its conventions — shared-base `sync.Once` builder, `t.Cleanup` teardown, sentinel-file assertions) drives `sand create --clone-url <served-fixture-url>` and then asserts, via `limactl shell`/guest inspection: the declared apt package is installed (`dpkg -l <package>`), the declared service is enabled (`systemctl is-enabled <service>`), the custom role's effect is present, the declared toolset tool is present in the clone, and the seed marker file exists in the project tree.
- [ ] **Toolset reconciliation check**: create the fixture VM with a shipped tool disabled at the base tier (e.g. `--with-go=false`) while the fixture's `.sandbar/` declares that same tool in its `toolset` group; verify the tool is present in the clone (e.g. `go version` in the guest) while the base version stamp still renders the flag-driven toolset key *without* that tool — proving per-clone install happens without base churn.
- [ ] **Malformed-manifest check**: point a variant of the served fixture's manifest at an unknown key (or another validator-rejected shape) and re-create; verify finalize aborts with the validator's clear message (from Task 1) rather than silently skipping the profile — this proves the guest-side invocation wiring, not just the validator's own logic (already covered by Task 1's unit test).
- [ ] The extended test(s) run in the existing `lima-e2e` job in `.github/workflows/test.yml` (per-PR blocking) — confirm no new job is needed, consistent with the plan's clarification #9(c) that `lima-e2e`, not the weekly `molecule` job, is the finalize stage's authoritative per-PR guard.
- [ ] `LIMA_E2E=1 go test -tags limae2e -timeout 30m -run E2E ./cmd/sand/` passes locally (or in an environment with Lima/KVM access) including the new assertions.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Read the plan's "Fixture mechanism for the full-path e2e" paragraph and Self Validation steps 1, 2, and 6 for the authoritative fixture contents and assertion list.
- Reuse `cmd/sand/create_e2e_test.go`'s existing patterns: shared base built once via `sync.Once`, `t.Cleanup(func(){ cli.Delete(name, true) })`, sentinel-file plant/check via `cli.ShellOut`. The existing suite's shared base is headless (no tools, no clone) — this task's fixture-repo test is new machinery layered alongside it, not a modification of the existing headless tests.
- The `lima-e2e` job already has a 60-minute timeout and runs a cold + warm create cycle; be mindful of total added runtime when deciding whether the fixture test reuses an existing base or builds its own.

## Input Dependencies

- Task 2's shipped provisioning profiles (for the toolset-reconciliation check).
- Task 3's finalize stage (the mechanism under test).
- Task 1's validator (for the malformed-manifest check's expected error message).

## Output Artifacts

The checked-in fixture repository, the local-git-remote serving helper, and the extended `lima-e2e` test(s) — the plan's authoritative per-PR blocking proof that the full feature path works on a real VM.

## Implementation Notes

Test philosophy: write a few tests, mostly integration. This entire task *is* the "mostly integration" case — favor one well-structured full-path test plus the two focused checks (reconciliation, malformed-manifest) over a large matrix of small VM-dependent tests, since each VM-level test is expensive in CI time.

Follow RED → GREEN → REFACTOR at the fixture level: get the local git remote serving and a bare `sand create --clone-url` working first (RED: assert the clone succeeded, nothing more), then layer in the `.sandbar/` assertions one declaration group at a time, confirming each fails before Task 3's corresponding piece exists and passes after.
