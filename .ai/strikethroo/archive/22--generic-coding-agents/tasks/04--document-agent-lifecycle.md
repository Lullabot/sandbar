---
id: 4
group: "coding-agents"
dependencies: [1, 2]
status: "completed"
created: 2026-09-21
model: "haiku"
effort: "low"
skills: ["documentation"]
---
# Update existing documentation for generic agents

## Objective
Make existing user and repository guidance accurate for per-VM agents, migration, remembered choices, and unified reset preservation.

## Skills Required
documentation.

## Acceptance Criteria
- [x] Make existing user and repository guidance accurate for per-VM agents, migration, remembered choices, and unified reset preservation.
- [x] uvx --with-requirements docs/requirements.txt mkdocs build --strict exits 0; search affected docs to ensure old base-agent/preserve-Claude-only guidance replaced.

## Technical Requirements
Own docs/, README.md, AGENTS.md; coordinate final user-facing labels/API with task 3. Update affected existing pages, no new documentation system. Explain base dependencies vs per-VM installs, four choices/defaults, last selection persistence/migration, recorded selections on reset, preserved state paths and credentials disclosure. Avoid claiming live tests ran if only fixtures were used. Keep security claims precise. Validate links and strict build.

## Input Dependencies
Completed shared agent model and installation lifecycle from tasks 1, 2.

## Output Artifacts
Updated existing docs, README landing wording if needed, and AGENTS.md.

## Implementation Notes
<details>
<summary>Execution guidance</summary>

Read PRE_TASK_EXECUTION.md and follow RED → GREEN → REFACTOR for meaningful behavior. Own docs/, README.md, AGENTS.md; coordinate final user-facing labels/API with task 3. Update affected existing pages, no new documentation system. Explain base dependencies vs per-VM installs, four choices/defaults, last selection persistence/migration, recorded selections on reset, preserved state paths and credentials disclosure. Avoid claiming live tests ran if only fixtures were used. Keep security claims precise. Validate links and strict build.

This is documentation work; no new code tests are warranted. Run the strict documentation build.

Update task status as work proceeds and report runnable evidence. Do not commit; parent performs phase commit after independent verification.
</details>

## Execution Evidence

Updated existing create/reset, provisioning, tooling, state, security and troubleshooting documentation, README landing text and AGENTS lifecycle guidance. Parent independently ran the strict MkDocs build: exit 0, built in 1.18 seconds. No application test was added for prose-only changes.
