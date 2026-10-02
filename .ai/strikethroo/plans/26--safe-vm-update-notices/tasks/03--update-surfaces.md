---
id: 3
group: "vm-updates"
dependencies: [1, 2]
status: "pending"
created: 2026-10-02
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
skills: ["go", "bubbletea"]
complexity_score: 8
complexity_notes: "Truthful cross-provider provenance and preservation of user-owned data require careful integration."
---
# Expose update notices on tiles and CLI

## Objective
Use task 1 shared API to expose independent states in tiles and read-only sand updates NAME with JSON. Keep gauges fixed, avoid I/O during render, show reset advice, preserve failed/building behavior. Own internal/ui, cmd/sand and documentation/AGENTS.md. Document safe resets from task 2 and truthful unknown metadata. Update existing golden-template UI/CLI comparisons to use the actual base-revision semantics now stored in SnapshotResult.PlaybookVersion, avoiding always-stale templates caused by comparing image revisions with a playbook hash.

## Skills Required
go, bubbletea.

## Acceptance Criteria
- [ ] Focused UI tests/goldens and CLI tests pass; go run ./cmd/sand updates -h shows flags; strict docs build succeeds.
- [ ] Preserve existing VMs, unknown history and user modifications; no automatic provisioning.

## Technical Requirements
Use task 1 shared API to expose independent states in tiles and read-only sand updates NAME with JSON. Keep gauges fixed, avoid I/O during render, show reset advice, preserve failed/building behavior. Own internal/ui, cmd/sand and documentation/AGENTS.md. Document safe resets from task 2 and truthful unknown metadata. Update existing golden-template UI/CLI comparisons to use the actual base-revision semantics now stored in SnapshotResult.PlaybookVersion, avoiding always-stale templates caused by comparing image revisions with a playbook hash.

## Input Dependencies
Plan 26 and completed dependency tasks [1, 2].

## Output Artifacts
Implementation, meaningful tests and evidence recorded here.

## Implementation Notes
<details><summary>Execution guidance</summary>
Read PRE_TASK_EXECUTION.md and follow RED → GREEN → REFACTOR for meaningful business logic. Inspect current provider source semantics before selecting revisions. Keep host state isolated in tests. Write a few tests, mostly integration: test custom business logic, critical user workflows, edge cases and component boundaries; do not test frameworks, trivial getters or obvious static configuration. Combine related scenarios and verify the far side of file/process boundaries. Run the acceptance commands and record exact outcomes. Do not commit other tasks' changes. Set status in-progress then completed after checks. Use task 1 shared API to expose independent states in tiles and read-only sand updates NAME with JSON. Keep gauges fixed, avoid I/O during render, show reset advice, preserve failed/building behavior. Own internal/ui, cmd/sand and documentation/AGENTS.md. Document safe resets from task 2 and truthful unknown metadata. Update existing golden-template UI/CLI comparisons to use the actual base-revision semantics now stored in SnapshotResult.PlaybookVersion, avoiding always-stale templates caused by comparing image revisions with a playbook hash.
</details>
