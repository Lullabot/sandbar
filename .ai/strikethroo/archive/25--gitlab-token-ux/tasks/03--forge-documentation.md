---
id: 3
group: "gitlab-support"
dependencies: [1]
status: "completed"
created: 2026-09-29
models:
  anthropic: "claude-haiku-4-5"
  openai: "gpt-6-luna"
effort: "low"
skills: [technical-writing]
---
# Document GitHub/GitLab and self-hosted token UX

## Objective
Document GitHub/GitLab and self-hosted token UX to satisfy issue 188 including self-hosted GitLab.

## Skills Required
technical-writing

## Acceptance Criteria
- [x] Deliver requirements below and preserve existing GitHub behavior.
- [x] Run `uvx --with-requirements docs/requirements.txt mkdocs build --strict` with exit 0 and capture evidence.

## Technical Requirements
Own docs and AGENTS.md. Document --clone-forge gitlab for custom HTTPS hosts; auto selection github.com/gitlab.com. GitLab create token stored in non-empty repository parent scope and helpers support scope hostname. GITLAB_TOKEN in <host>/<namespace> scope binds the first component (including single-label host or host:port); a bare one-component scope defaults gitlab.com. github.com scopes use gitlab.com for GITLAB_TOKEN so both tokens may coexist. Safe scope grammar now includes colon; IPv6 literal URLs with token are rejected (use DNS name instead). Explain scoped GITLAB_TOKEN, glab behavior, reset reapplication vs re-clone token, rotation, no self-hosted auto detection and GitHub-only land --pr/--web. Fine-grained GitLab Code Download clone/pull and Code Push push permissions with project/group boundary; legacy read_repository/write_repository separately. Primary source https://docs.gitlab.com/auth/tokens/fine_grained_access_tokens_other/ verified by parent. Update existing onboarding docs where appropriate. No new tests for prose; strict docs build.

## Input Dependencies
Tasks [1]; issue 188 and plan 25.

## Output Artifacts
Implementation and verification evidence in owned files.

## Implementation Notes
<details><summary>Implementation guidance</summary>
Own docs and AGENTS.md. Document --clone-forge gitlab for custom HTTPS hosts; auto selection github.com/gitlab.com. GitLab create token stored in non-empty repository parent scope and helpers support scope hostname. GITLAB_TOKEN in <host>/<namespace> scope binds the first component (including single-label host or host:port); a bare one-component scope defaults gitlab.com. github.com scopes use gitlab.com for GITLAB_TOKEN so both tokens may coexist. Safe scope grammar now includes colon; IPv6 literal URLs with token are rejected (use DNS name instead). Explain scoped GITLAB_TOKEN, glab behavior, reset reapplication vs re-clone token, rotation, no self-hosted auto detection and GitHub-only land --pr/--web. Fine-grained GitLab Code Download clone/pull and Code Push push permissions with project/group boundary; legacy read_repository/write_repository separately. Primary source https://docs.gitlab.com/auth/tokens/fine_grained_access_tokens_other/ verified by parent. Update existing onboarding docs where appropriate. No new tests for prose; strict docs build.
Read PRE_TASK_EXECUTION.md and follow RED/GREEN/REFACTOR for meaningful auth/workflow tests. Write a few tests, mostly integration: verify custom logic, critical workflows, transformations, application edge cases and component integration. Avoid third-party/framework, trivial getter/setter, simple CRUD or static configuration tests. Combine related scenarios; no test per operation. Update status in real time. Do not commit; parent verifies and commits each phase.
</details>

## Verification Evidence
- `uvx --with-requirements docs/requirements.txt mkdocs build --strict` — exit 0.
- `git diff --check` — clean.
- `go run ./cmd/sand create --help` — confirmed the documented forge and token flag descriptions; the literal help sample is synchronized with its output.
