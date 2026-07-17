---
id: 6
group: "molecule"
dependencies: [3]
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "medium"
skills:
  - ansible
---
# Add a molecule scenario for the repo-profile finalize stage

## Objective

Add a new molecule scenario (following the existing `molecule/base/` and `molecule/samba/` conventions) that exercises the repo-profile finalize stage against a staged checkout, providing finer-grained assertions on individual declaration groups (packages, services, roles, toolset, seed) than the full-path `lima-e2e` test. This runs on the existing weekly/dispatch, non-blocking `molecule` job — it is explicitly supplementary depth, not a per-PR gate.

## Skills Required

Ansible / Molecule scenario authoring.

## Acceptance Criteria

- [ ] A new scenario directory exists (e.g. `molecule/repo-profile/`) with `molecule.yml`, `converge.yml`, `verify.yml`, and a `prepare.yml` if needed to stage a fake `.sandbar/` checkout — following `molecule/base/molecule.yml`'s conventions (Docker driver, digest-pinned `geerlingguy/docker-debian13-ansible` image, `/sbin/init` + privileged + cgroup mount for real systemd).
- [ ] The scenario's `converge.yml` runs the new finalize stage from Task 3 against a staged/prepared checkout containing a `.sandbar/` manifest exercising all five declaration groups.
- [ ] `verify.yml` asserts each declaration group's effect: declared package installed, declared service enabled, declared role's effect present, declared toolset tool present, seed tasks' effect present.
- [ ] The scenario name is added to the `molecule` CI job's matrix in `.github/workflows/test.yml` (alongside `base` and `samba`), preserving the job's existing `if: schedule || workflow_dispatch` and `continue-on-error: true` gating — this scenario must NOT become a per-PR blocking gate; the plan is explicit that `lima-e2e` (Task 5) is the finalize stage's authoritative per-PR guard and molecule is supplementary only.
- [ ] Run `molecule test -s repo-profile` locally (or via `workflow_dispatch`) and confirm the scenario passes through molecule's full `test_sequence` (`dependency, create, prepare, converge, idempotence, verify, destroy`), including the `idempotence` step — this is a direct proof of the finalize stage's idempotency requirement.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Read the plan's Verification Surface table row: "Profile stage against a staged checkout ... molecule scenario ... weekly + dispatch only, non-blocking — supplementary depth, not the PR gate."
- Do not weaken or remove the `if: schedule || workflow_dispatch` / `continue-on-error: true` gating on the `molecule` job when adding this scenario to its matrix.

## Input Dependencies

Task 3's finalize stage (the mechanism under test).

## Output Artifacts

A new `molecule/repo-profile/` scenario, wired into the existing weekly/dispatch `molecule` CI matrix.

## Implementation Notes

This task's primary value is the `idempotence` step in molecule's `test_sequence` — it is the most direct, repeatable proof that the finalize stage is safe to re-run on `Recreate`/`Reset`, which Task 3 requires but cannot itself prove without something like molecule (the `lima-e2e` full-path test in Task 5 does not necessarily re-run the stage twice). Prioritize getting a real fake-checkout fixture staged correctly over exhaustively re-testing what Task 5's e2e already covers at the VM level.
