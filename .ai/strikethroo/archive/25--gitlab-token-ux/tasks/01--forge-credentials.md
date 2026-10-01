---
id: 1
group: "gitlab-support"
dependencies: []
status: "completed"
created: 2026-09-29
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
skills: [go,ansible]
---
# Implement forge selection and GitLab credential delivery

## Objective
Implement forge selection and GitLab credential delivery to satisfy issue 188 including self-hosted GitLab.

## Skills Required
go,ansible

## Acceptance Criteria
- [x] Deliver requirements below and preserve existing GitHub behavior.
- [x] Run `go test ./internal/vm ./internal/provision; ansible-playbook -i localhost, site.yml --syntax-check` with exit 0 and capture evidence.

## Technical Requirements
Own internal/vm, internal/provision, roles/project. Add CreateConfig.CloneForge string and vm.ResolveCloneForge(cloneURL, selection string) (string,error), returning github/gitlab or empty when auto cannot identify host. Empty or auto infers github.com and gitlab.com; explicit gitlab allows custom HTTPS hosts; explicit github may support github.com only. Add vm.CloneTokenKey(cloneURL, selection string) string returning GH_TOKEN/GITLAB_TOKEN or empty. Validate token on unknown hosts with actionable --clone-forge gitlab guidance; require HTTPS and safe URL when token supplied. Pass selection to Ansible vars. Clone with no token on argv; helpers tied to host; GitLab fresh-shell helper persists. Scoped GITLAB_TOKEN takes host from scope first segment, default gitlab.com for scopes without host. Ensure multiple recognized tokens per scope are not overwritten and GitHub precedence remains correct. Use real Git credential tests with isolated HOME plus auth clone role test if feasible. Do not change CLI/UI/docs; notify parent of helper API.

## Input Dependencies
Tasks []; issue 188 and plan 25.

## Output Artifacts
Implementation and verification evidence in owned files.

## Implementation Notes
<details><summary>Implementation guidance</summary>
Own internal/vm, internal/provision, roles/project. Add CreateConfig.CloneForge string and vm.ResolveCloneForge(cloneURL, selection string) (string,error), returning github/gitlab or empty when auto cannot identify host. Empty or auto infers github.com and gitlab.com; explicit gitlab allows custom HTTPS hosts; explicit github may support github.com only. Add vm.CloneTokenKey(cloneURL, selection string) string returning GH_TOKEN/GITLAB_TOKEN or empty. Validate token on unknown hosts with actionable --clone-forge gitlab guidance; require HTTPS and safe URL when token supplied. Pass selection to Ansible vars. Clone with no token on argv; helpers tied to host; GitLab fresh-shell helper persists. Scoped GITLAB_TOKEN takes host from scope first segment, default gitlab.com for scopes without host. Ensure multiple recognized tokens per scope are not overwritten and GitHub precedence remains correct. Use real Git credential tests with isolated HOME plus auth clone role test if feasible. Do not change CLI/UI/docs; notify parent of helper API.
Read PRE_TASK_EXECUTION.md and follow RED/GREEN/REFACTOR for meaningful auth/workflow tests. Write a few tests, mostly integration: verify custom logic, critical workflows, transformations, application edge cases and component integration. Avoid third-party/framework, trivial getter/setter, simple CRUD or static configuration tests. Combine related scenarios; no test per operation. Update status in real time. Do not commit; parent verifies and commits each phase.
</details>
