---
id: 1
group: "coding-agents"
dependencies: []
status: "completed"
created: 2026-09-21
model: "sonnet"
effort: "high"
skills: ["go", "integration-testing"]
complexity_score: 8
---
# Separate agent configuration and migrate reset state

## Objective
Implement the shared agent selection model, persistent last-create preferences and legacy migration, and all-agent reset preservation for Lima and Proxmox.

## Skills Required
go, integration-testing.

## Acceptance Criteria
- [x] Implement the shared agent selection model, persistent last-create preferences and legacy migration, and all-agent reset preservation for Lima and Proxmox.
- [x] go test ./internal/vm ./internal/agentprefs ./internal/provision ./internal/provider ./internal/registry passes; fixtures prove explicit opt-outs, old config round trips, base-key independence, and four-agent archive bytes/modes.

## Technical Requirements
Own internal/vm, internal/provision, internal/provider, internal/registry as needed and a new internal/agentprefs package; do not edit UI, cmd, roles or site.yml. Keep WithClaude/WithCodex JSON compatibility; add WithOpenCode and WithPi. ToolPtrs/ToolsetKey represent only base dependencies (ddev/go/java); add AgentPtrs for agents. Provide a simple shared agentprefs API for loading, saving, applying booleans and seeding absent preferences from a distinguishable legacy base stamp. Expose/read legacy versus current base stamp format explicitly so old all-off stamps migrate and modern agent-free stamps do not switch off default Claude. Persist under XDG_DATA_HOME/sandbar, so existing isolated tests remain isolated. Coordinate the precise API with parent. Emit toolset_opencode/toolset_pi alongside existing agent extra vars. Rename reset option to PreserveAgents and use a common AgentStatePaths list; migrate legacy external option only if any persisted reset options exist (currently none). Preserve .claude, .claude.json, .codex, .config/opencode, .local/share/opencode, .local/state/opencode, .cache/opencode if relevant, .pi/agent. Investigate installer-binary paths within preserved trees: fresh reset must install latest selected binaries even when state restored. Ensure missing paths harmless on both providers and archive cleanup/private modes preserved. Add disk migration and actual shell archive roundtrip tests, with temporary home/state only.

## Input Dependencies
Existing repository code and plan requirements; no task dependencies.

## Output Artifacts
Go domain/configuration, preference API, base stamp migration helper, extra vars, reset staging and provider tests.

## Implementation Notes
<details>
<summary>Execution guidance</summary>

Read PRE_TASK_EXECUTION.md and follow RED → GREEN → REFACTOR for meaningful behavior. Own internal/vm, internal/provision, internal/provider, internal/registry as needed and a new internal/agentprefs package; do not edit UI, cmd, roles or site.yml. Keep WithClaude/WithCodex JSON compatibility; add WithOpenCode and WithPi. ToolPtrs/ToolsetKey represent only base dependencies (ddev/go/java); add AgentPtrs for agents. Provide a simple shared agentprefs API for loading, saving, applying booleans and seeding absent preferences from a distinguishable legacy base stamp. Expose/read legacy versus current base stamp format explicitly so old all-off stamps migrate and modern agent-free stamps do not switch off default Claude. Persist under XDG_DATA_HOME/sandbar, so existing isolated tests remain isolated. Coordinate the precise API with parent. Emit toolset_opencode/toolset_pi alongside existing agent extra vars. Rename reset option to PreserveAgents and use a common AgentStatePaths list; migrate legacy external option only if any persisted reset options exist (currently none). Preserve .claude, .claude.json, .codex, .config/opencode, .local/share/opencode, .local/state/opencode, .cache/opencode if relevant, .pi/agent. Investigate installer-binary paths within preserved trees: fresh reset must install latest selected binaries even when state restored. Ensure missing paths harmless on both providers and archive cleanup/private modes preserved. Add disk migration and actual shell archive roundtrip tests, with temporary home/state only.

Test philosophy: write a few tests, mostly integration. Meaningful tests verify custom business logic, critical paths, application-specific edge cases, core error conditions, data transformations, complex validation and integration boundaries. Do not test third-party library/framework behavior, simple CRUD, getters/setters or static configuration that obviously fails when incorrect. Combine related scenarios, favor integration and critical-path coverage, avoid per-method or per-CRUD tasks, and question dedicated tests for trivial functions.

Update task status as work proceeds and report runnable evidence. Do not commit; parent performs phase commit after independent verification.
</details>
