---
id: 3
group: "coding-agents"
dependencies: [1, 2]
status: "pending"
created: 2026-09-21
model: "sonnet"
effort: "high"
skills: ["go", "bubbletea"]
complexity_score: 8
---
# Wire remembered agent checkboxes into create and reset

## Objective
Expose all four agent choices in the create form and CLI, remember the last submitted selection, and use the unified reset preservation checkbox.

## Skills Required
go, bubbletea.

## Acceptance Criteria
- [ ] Expose all four agent choices in the create form and CLI, remember the last submitted selection, and use the unified reset preservation checkbox.
- [ ] go test ./cmd/sand ./internal/ui passes; terminal integration test at 80x24 demonstrates all four selectable checkboxes, preserved focus/footer, persisted reopen/all-off choices, and unified reset option.

## Technical Requirements
Own internal/ui and cmd/sand only. Consume AgentPtrs and agentprefs APIs from task 1. Agent checkboxes are per-VM choices; ddev/go/java and rebuild remain base controls. Remember valid submitted create choices across process restarts, including all-off; do not save cancelled forms or make reset choices overwrite remembered creates. Seed absent preferences from old base stamps but keep new preferences authoritative and prevent late async reads from overwriting user's edits. Reuse async host-read seam and capture immutable values. Add with-opencode/with-pi flags and retain with-claude/with-codex names with per-VM help. Reset replays all four recorded booleans and has one Preserve agent settings and files checkbox using PreserveAgents; keep project option. Ensure form focus/scroll/help remain usable at 80x24. Update tests through real key input and provider-captured final config; use existing providerfake and isolated state, no t.Parallel. Fix stale base-tool tests for new contract.

## Input Dependencies
Completed shared agent model and installation lifecycle from tasks 1, 2.

## Output Artifacts
TUI/CLI changes and behavior tests with updated snapshots.

## Implementation Notes
<details>
<summary>Execution guidance</summary>

Read PRE_TASK_EXECUTION.md and follow RED → GREEN → REFACTOR for meaningful behavior. Own internal/ui and cmd/sand only. Consume AgentPtrs and agentprefs APIs from task 1. Agent checkboxes are per-VM choices; ddev/go/java and rebuild remain base controls. Remember valid submitted create choices across process restarts, including all-off; do not save cancelled forms or make reset choices overwrite remembered creates. Seed absent preferences from old base stamps but keep new preferences authoritative and prevent late async reads from overwriting user's edits. Reuse async host-read seam and capture immutable values. Add with-opencode/with-pi flags and retain with-claude/with-codex names with per-VM help. Reset replays all four recorded booleans and has one Preserve agent settings and files checkbox using PreserveAgents; keep project option. Ensure form focus/scroll/help remain usable at 80x24. Update tests through real key input and provider-captured final config; use existing providerfake and isolated state, no t.Parallel. Fix stale base-tool tests for new contract.

Test philosophy: write a few tests, mostly integration. Meaningful tests verify custom business logic, critical paths, application-specific edge cases, core error conditions, data transformations, complex validation and integration boundaries. Do not test third-party library/framework behavior, simple CRUD, getters/setters or static configuration that obviously fails when incorrect. Combine related scenarios, favor integration and critical-path coverage, avoid per-method or per-CRUD tasks, and question dedicated tests for trivial functions.

Update task status as work proceeds and report runnable evidence. Do not commit; parent performs phase commit after independent verification.
</details>

