---
id: 3
group: "vm-updates"
dependencies: [1, 2]
status: "completed"
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
- [x] Focused UI tests/goldens and CLI tests pass; go run ./cmd/sand updates -h shows flags; strict docs build succeeds.
- [x] Preserve existing VMs, unknown history and user modifications; no automatic provisioning.

## Execution evidence

- RED: update tile tests failed on missing status fields; CLI test failed on missing `doUpdates`; template tests failed because published-image revisions compared stale against a playbook hash; progress test showed `MarkManaged` replacing the marker.
- GREEN/REFACTOR: independent update states are computed during asynchronous refresh, displayed with reset advice while preserving the work badge and fixed gauges, and exposed through read-only text/JSON CLI output. Building/failed tiles suppress notices. Template list/form compare actual source base revision with the provider's desired base revision. Progress uses the serialized provider `MarkProgress` seam.
- `go test ./cmd/sand ./internal/ui ./internal/profiles ./internal/registry -count=1` passed. `go test ./internal/ui -run TestTUI -update -count=1` regenerated affected snapshots; the three existing board snapshots changed only legacy tile footers to `versions unknown · R reset`, and four new focused goldens cover base, setup, unknown, and both.
- `go run ./cmd/sand updates -h` displayed `--profile` and `--json` without provider access. `uvx --with-requirements docs/requirements.txt mkdocs build --strict` passed. `git diff --check` reports only the snapshot format's padded rows in changed golden lines; their text was visually reviewed.
- Read-only profile and registry loaders keep a missing Local profile in memory, do not rewrite a version-3 index or quarantine corrupt metadata, and read a pre-rename legacy index in place without copying/removing it. The focused disk assertion passed after correcting its local-provider fixture to `lima`.

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
