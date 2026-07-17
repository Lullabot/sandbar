---
id: 8
group: "documentation"
dependencies: [4, 5, 6]
status: "pending"
created: 2026-07-17
model: "haiku"
effort: "low"
skills:
  - technical-writing
---
# Document the new ownership model and marker contract

## Objective
Update the project's docs to describe the relocated ownership model: markers are
the source of truth, the registry is a cache + known-targets + one-release
fallback, `Scope` is UI grouping (not ownership), the marker file location/schema/
lifecycle, and the `lima_home` → remote `LIMA_HOME` behavior.

## Skills Required
- `technical-writing` — update `AGENTS.md` and package/doc comments.

## Acceptance Criteria
- [ ] `AGENTS.md` (and relevant package doc comments in `internal/registry` and
  `internal/provider`) describe: markers as ownership truth, registry as
  cache/known-targets/one-release fallback, and `Scope` as grouping only.
- [ ] The marker contract is documented for future provider implementers
  (Proxmox/cloud): file location `<LimaHome>/<name>/sandbar.json`, JSON schema
  (fields + schema version), and lifecycle (written on create, removed with the
  instance, adopted once from the legacy registry).
- [ ] The `lima_home` → remote `LIMA_HOME` behavior is noted in the profile/remote
  docs, since it now affects discovery, not just file reads.
- [ ] The one-release fallback window and the follow-up to remove the legacy
  registry read path are recorded.
- [ ] Verification: `git diff --stat` shows the doc files changed, and a
  reviewer reading `AGENTS.md` can find the ownership-model section. Run
  `grep -rn "sandbar.json" AGENTS.md docs/ 2>/dev/null` and confirm the marker
  contract is present (non-empty output).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Files: `AGENTS.md`, `internal/registry` / `internal/provider` doc comments,
  and any profile/remote doc under `docs/` that mentions `lima_home`.
- Only edit the project's own docs (not `.ai/` or skills dirs).

## Input Dependencies
- Tasks 4, 5, 6 — document the shipped behavior, not the plan's intent.

## Output Artifacts
- Updated human- and agent-facing docs.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

Write concisely and accurately against what tasks 3–6 actually implemented (read
the final code, not just this task). Cover:

1. Ownership model: "A VM is managed iff it carries a provenance marker on its
   host. The local `managed-vms.json` registry is a cache + known-targets list +
   a one-release legacy fallback, not the source of truth. `Scope` groups the UI
   and keys known targets; it no longer decides ownership."
2. Marker contract (for Proxmox/cloud implementers): location, JSON schema (list
   the fields and the `schema` version), and lifecycle (create → write; delete →
   removed with the instance dir; upgrade → adopted once from the registry).
3. `lima_home`/`LIMA_HOME`: setting a profile's `lima_home` now also scopes the
   remote `limactl` (discovery), not only sand's file reads.
4. The removable one-release fallback + adoption path, so a future cleanup PR
   knows what to delete.

Keep edits scoped to the project's own documentation files. Do not touch `.ai/`
or the skills directories.
</details>
