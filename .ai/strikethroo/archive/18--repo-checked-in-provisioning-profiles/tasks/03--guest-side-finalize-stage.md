---
id: 3
group: "finalize-stage"
dependencies: [1, 2]
status: "completed"
created: 2026-07-17
model: "opus"
effort: "xhigh"
skills:
  - ansible
complexity_score: 9
complexity_notes: "The architectural core of the feature: a new gated finalize-phase stage that must validate, install, reconcile, include, enable, and seed correctly and idempotently, without disturbing the embedded-playbook or base-stamp invariants. Highest correctness risk in the plan."
---
# Wire the guest-side repo-profile finalize stage

## Objective

Add a new stage to `site.yml`, ordered immediately after the `project` role (which clones the repository — see `roles/project/tasks/main.yml`), that discovers a repo-checked-in `.sandbar/` manifest in the freshly cloned checkout and, when present, applies it: validate (Task 1's validator), install declared apt packages, reconcile the declared toolset per-clone (Task 2's shipped profiles), include declared roles from the repo's `.sandbar/roles/`, enable/start declared services, and run the repo's seed tasks file last. Repos without `.sandbar/` must take the stock path with zero new variables, prompts, or behavior changes.

## Skills Required

Ansible — role/stage composition, `when:` gating, `include_role`/`include_tasks` with dynamic role paths, idempotent task design.

## Acceptance Criteria

- [ ] The new stage is gated on the presence of the manifest in the cloned checkout (e.g. a `stat` on the well-known `.sandbar/` manifest path, registered as a fact) — repos without `.sandbar/` see no new tasks execute (verify via `ansible-playbook --syntax-check site.yml` and, at the unit level, that `provision.BuildExtraVars` for finalize phase is unchanged for no-profile repos — coordinate with Task 4's seam test).
- [ ] When present, the manifest is validated first using Task 1's validator; a validation failure aborts finalize with the validator's clear message (not a silent skip, not a generic Ansible failure).
- [ ] Declared apt packages install in a single transaction, mirroring `roles/base/tasks/main.yml:256-276`'s convention (`install_recommends: false`).
- [ ] Declared toolset tools are reconciled per-clone using Task 2's shipped profiles: a tool already present in the base is a no-op, a tool missing from the base is installed into this clone only. The shared base's `PlaybookVersion` stamp is never written or modified by this stage — verify with a test/inspection that base-stamp state is untouched after a finalize run that includes toolset reconciliation.
- [ ] Declared roles are included from `.sandbar/roles/<name>/` in the cloned checkout by extending the Ansible roles search path to the checkout — repo role content is read in place and must never be copied into, or treated as part of, the embedded playbook fileset (it must not appear in the `go:embed` list, the rsync allowlist, or the content hash).
- [ ] Declared services are enabled and started (systemd).
- [ ] The repo's seed tasks file is included last, after packages/toolset/roles/services are in place, running with the play's privileges (root), with `become_user` available to the seed tasks for user-level steps.
- [ ] The stage re-runs correctly (idempotently) on `Recreate`/`Reset` — running it twice against an unchanged manifest produces no errors and no unintended duplicate state.
- [ ] `ansible-playbook --syntax-check site.yml` passes with the new stage in place.
- [ ] `go test ./... -race` continues to pass, including the extra-vars byte-identical assertion for no-profile repos (coordinate with Task 4).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Read the plan's "Guest-Side Finalize Stage" section for the authoritative six-step sequence (validate → install packages → reconcile toolset → include roles → enable services → seed) and its rationale.
- `roles/project/tasks/main.yml` is wrapped in a `block:` gated on `project_clone_url | length > 0` and ends with the `ansible.builtin.git` clone task (its last task). Insert the new stage as a new role/role-include entry in `site.yml` immediately after `- role: project`, gated on the manifest's presence — do not bolt it onto the end of `roles/project/tasks/main.yml` itself, to keep the clone concern and the repo-profile concern separately testable and named.
- No new host-side variables, extra-vars, or registry fields are introduced by this stage — everything it needs (the manifest, roles, seed tasks) comes from the cloned checkout inside the guest, not from the host.
- Repo-supplied Ansible (the seed tasks file) runs automatically with no consent gate, per the plan's clarification #3 — this is a deliberate trust decision (cloning a repo already implies running its code in the guest), not an oversight; do not add a prompt or gate.

## Input Dependencies

- Task 1's validator and manifest schema.
- Task 2's shipped provisioning profiles (for per-clone toolset reconciliation).

## Output Artifacts

The new finalize-phase repo-profile stage in `site.yml` / its supporting role, ready for Task 4's seam tests, Task 5's full-path e2e, and Task 6's molecule scenario to exercise.

## Implementation Notes

This is the single highest-risk task in the plan — treat it with the corresponding rigor. Read the plan's "Technical Risks" and "Implementation Risks" `<details>` blocks in full before starting (embedded-playbook triple-pin breakage, unintended base churn, non-idempotent/destructive repo Ansible).

Follow RED → GREEN → REFACTOR at the level of each of the six sub-steps: for each (manifest-presence gating, validation invocation, package install, toolset reconciliation, role inclusion, service enablement, seed inclusion), write or run a concrete check that fails before the step exists and passes after — favor real `ansible-playbook --syntax-check` runs and, where feasible without a VM, Ansible's `--check`/dry-run mode or a minimal molecule-style local check over purely manual reasoning. The full real-VM proof is Task 5's job; this task should still self-verify as much as is practical without one.

Because the shared base's `PlaybookVersion` and staleness behavior must be untouched by any repo-specific content, be explicit in code/comments about *why* the new stage never writes to base-stamp state — a future reader modifying this stage needs that invariant to be obvious, not rediscovered.
