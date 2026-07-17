---
id: 7
group: "docs"
dependencies: [1, 2, 3]
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "medium"
skills:
  - technical-writing
---
# Document repo-checked-in provisioning profiles

## Objective

Write the new provisioning-profiles documentation page and update the existing docs pages the plan names, so the feature is discoverable, the manifest schema is documented, the two-tier execution model and per-clone toolset reconciliation are explained, the idempotency/root-execution trust contract is stated plainly, and the "provisioning profile" vs "connection profile" terminology collision is disambiguated everywhere it could confuse a reader.

## Skills Required

Technical writing, with enough familiarity with the implemented feature (Tasks 1-3) to document it accurately rather than from the plan alone.

## Acceptance Criteria

- [ ] New page under `docs/using-sand/` (e.g. `docs/using-sand/provisioning-profiles.md`) documents: the `.sandbar/` directory layout, the manifest schema (all five declaration groups from Task 1), guest-only discovery (no host-side fetch or parsing), the two-tier execution model (shipped profiles at base tier vs. repo profiles at per-clone finalize tier), per-clone toolset reconciliation semantics, the idempotency requirement and root/`become_user` execution contract for seed tasks, and the shipped profiles as worked examples.
- [ ] `docs/getting-started/available-tools.md` is updated to describe the four optional tools as shipped provisioning profiles and how to toggle them (still via `--with-*` flags / TUI toggles).
- [ ] `docs/getting-started/how-it-works.md` is updated to extend the base/clone/finalize explanation (and its diagram, if it has one) with the new repo-profile stage's position in the flow.
- [ ] `docs/contributing/ansible-playbook.md` documents the new finalize stage, the shipped-profile directory, and the invariant that repo-supplied content never enters the embedded fileset/rsync allowlist/content hash (only shipped-profile content does).
- [ ] `docs/reference/files-and-state.md` documents `.sandbar/` as an in-repo path, read only inside the guest — explicitly noting no new host-side path is introduced.
- [ ] `docs/using-sand/cli-reference.md` notes the flag-shorthand behavior (flags/toggles enable the corresponding shipped profile).
- [ ] `docs/using-sand/connection-profiles.md` gets an explicit disambiguation note distinguishing "connection profiles" (this page's subject) from the new "provisioning profiles" feature.
- [ ] `docs/reference/security-model.md` records the trust decisions: repo profiles execute automatically in the guest with no consent gate, and repository content never reaches the host in any form.
- [ ] `AGENTS.md` is updated if the playbook layout (embed/rsync/hash triple-pin file lists) or test-layer instructions changed materially as a result of Tasks 1-3 — read its existing "base image / clone / finalize provisioner" section first and extend it rather than duplicating it.
- [ ] Every new/updated doc uses the term "provisioning profile" consistently (never "profile" unqualified where it could be confused with a connection profile).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Read the plan's "Documentation" section for the authoritative file list (reproduced in the acceptance criteria above) and the plan's "Terminology collisions" background note.
- Do not document `resources` (CPUs/memory/disk) as part of the manifest — the plan explicitly excludes it (refinement #6); resources remain a host-specific flags/TUI concern.
- Do not overstate the reproducibility guarantee: per the plan's Notes, apt package versions are not pinned, so the honest guarantee is "configuration-identical and reproducible," not literal byte-identical images. State this precisely.

## Input Dependencies

Tasks 1, 2, and 3's implementation (schema, shipped profiles, finalize stage) — document the actual shipped behavior, not the plan's pre-implementation description, in case any detail shifted during implementation.

## Output Artifacts

One new documentation page and seven updated existing pages/files, giving repo authors and Sandbar users a complete, accurate picture of the feature.

## Implementation Notes

Write a few tests, mostly integration — this guidance does not apply to a pure documentation task; skip the TDD cycle and go straight to writing accurate documentation grounded in the merged implementation.

Cross-check each doc claim against the actual code/Ansible from Tasks 1-3 rather than copying the plan's prose verbatim — the plan describes intent at design time, and minor details (exact file paths, exact flag names, exact manifest field names) may have been refined during implementation.
