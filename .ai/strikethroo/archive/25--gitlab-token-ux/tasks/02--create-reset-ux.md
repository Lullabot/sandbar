---
id: 2
group: "gitlab-support"
dependencies: [1]
status: "completed"
created: 2026-09-29
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
skills: [go,bubbletea]
---
# Connect CLI and TUI forge-aware create/reset and saved credentials

## Objective
Connect CLI and TUI forge-aware create/reset and saved credentials to satisfy issue 188 including self-hosted GitLab.

## Skills Required
go,bubbletea

## Acceptance Criteria
- [ ] Deliver requirements below and preserve existing GitHub behavior.
- [ ] Run `go test ./cmd/sand ./internal/ui; go run ./cmd/sand create --help` with exit 0 and capture evidence.

## Technical Requirements
Own cmd/sand and internal/ui. Use vm.ResolveCloneForge and vm.CloneTokenKey from task 1. Add --clone-forge auto/github/gitlab CLI; persist selection via reset config merge. TUI has neutral Repository URL and Clone token labels, a selectable Git service row cycling Auto/GitHub/GitLab with space or left/right (enter still advances), automatic display names inferred public forge, forge selection suitable for self-hosted GitLab, URL-derived focused help (GitHub permissions vs GitLab Code Download/Push), no misleading labels/placeholders, narrow-terminal usable form. GitLab clone token saved under GITLAB_TOKEN in the repository parent scope (provision.OrgRelDir); GitHub existing GH_TOKEN global saving retained. Update reset saved-token recognition and forge selection from recorded config. Verify CLI/TUI token seeding, reset, and goldens. Clarify private reset re-clone requires token as existing behavior; preserved checkout reapplies stored wiring. No changes to provisioning/roles/docs.

## Input Dependencies
Tasks [1]; issue 188 and plan 25.

## Output Artifacts
Implementation and verification evidence in owned files.

## Implementation Notes
<details><summary>Implementation guidance</summary>
Own cmd/sand and internal/ui. Use vm.ResolveCloneForge and vm.CloneTokenKey from task 1. Add --clone-forge auto/github/gitlab CLI; persist selection via reset config merge. TUI has neutral Repository URL and Clone token labels, a selectable Git service row cycling Auto/GitHub/GitLab with space or left/right (enter still advances), automatic display names inferred public forge, forge selection suitable for self-hosted GitLab, URL-derived focused help (GitHub permissions vs GitLab Code Download/Push), no misleading labels/placeholders, narrow-terminal usable form. GitLab clone token saved under GITLAB_TOKEN in the repository parent scope (provision.OrgRelDir); GitHub existing GH_TOKEN global saving retained. Update reset saved-token recognition and forge selection from recorded config. Verify CLI/TUI token seeding, reset, and goldens. Clarify private reset re-clone requires token as existing behavior; preserved checkout reapplies stored wiring. No changes to provisioning/roles/docs.
Read PRE_TASK_EXECUTION.md and follow RED/GREEN/REFACTOR for meaningful auth/workflow tests. Write a few tests, mostly integration: verify custom logic, critical workflows, transformations, application edge cases and component integration. Avoid third-party/framework, trivial getter/setter, simple CRUD or static configuration tests. Combine related scenarios; no test per operation. Update status in real time. Do not commit; parent verifies and commits each phase.
</details>

## Noteworthy Events
- [2026-09-29] `sand create --recreate --clone-token` needed a second validation after adopting the recorded repository URL and forge; early validation had rejected the token before that adoption.
- [2026-09-29] Verified `go test ./cmd/sand ./internal/ui -count=1`, `go run ./cmd/sand create --help`, focused 80x24 GitLab token golden, and formatter checks. Live private-forge cloning remains untested without credentials.
