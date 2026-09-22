---
id: 2
group: "coding-agents"
dependencies: []
status: "completed"
created: 2026-09-21
model: "sonnet"
effort: "high"
skills: ["ansible", "integration-testing"]
complexity_score: 7
---
# Install selected agents during VM finalization

## Objective
Move agent installs out of the base, add OpenCode and Pi, and clean legacy agents from base convergence.

## Skills Required
ansible, integration-testing.

## Acceptance Criteria
- [x] Move agent installs out of the base, add OpenCode and Pi, and clean legacy agents from base convergence.
- [x] ansible-playbook --syntax-check -i inventory site.yml passes, and targeted tests assert no agent install in base, selected agents in finalize/full, preserved settings survive, and base cleanup only runs in base.

## Technical Requirements
Own site.yml, roles/, group_vars/, shell standalone installer if present, and a focused phase/role regression test in a new root or dedicated test file (coordinate). Do not edit core Go domain/provision/provider, UI, cmd or docs. Use toolset_claude/codex/opencode/pi booleans; all agents default false. Agents run full/finalize only; runtimes stay base. Research official installers and Linux state paths. Fresh clones must install current releases including when reset restores state. Never overwrite preserved user settings: seed config templates only when absent, retaining required sand defaults for fresh users. Base convergence must remove prior Claude/Codex install artifacts and generated state so previously installed agents are absent in deselected new clones; cleanup gated base-only and exact known paths. Preserve clipboard image-only guards. Validate all-on/all-off gates via an actual Ansible harness when feasible without installing tools on host. Use temporary directories and fake installer boundary to avoid host mutation; record limits.

## Input Dependencies
Existing repository code and plan requirements; no task dependencies.

## Output Artifacts
Agent roles, site phase gates, base cleanup, any standalone provisioning updates and focused Ansible verification.

## Implementation Notes
<details>
<summary>Execution guidance</summary>

Read PRE_TASK_EXECUTION.md and follow RED → GREEN → REFACTOR for meaningful behavior. Own site.yml, roles/, group_vars/, shell standalone installer if present, and a focused phase/role regression test in a new root or dedicated test file (coordinate). Do not edit core Go domain/provision/provider, UI, cmd or docs. Use toolset_claude/codex/opencode/pi booleans; all agents default false. Agents run full/finalize only; runtimes stay base. Research official installers and Linux state paths. Fresh clones must install current releases including when reset restores state. Never overwrite preserved user settings: seed config templates only when absent, retaining required sand defaults for fresh users. Base convergence must remove prior Claude/Codex install artifacts and generated state so previously installed agents are absent in deselected new clones; cleanup gated base-only and exact known paths. Preserve clipboard image-only guards. Validate all-on/all-off gates via an actual Ansible harness when feasible without installing tools on host. Use temporary directories and fake installer boundary to avoid host mutation; record limits.

Test philosophy: write a few tests, mostly integration. Meaningful tests verify custom business logic, critical paths, application-specific edge cases, core error conditions, data transformations, complex validation and integration boundaries. Do not test third-party library/framework behavior, simple CRUD, getters/setters or static configuration that obviously fails when incorrect. Combine related scenarios, favor integration and critical-path coverage, avoid per-method or per-CRUD tasks, and question dedicated tests for trivial functions.

Update task status as work proceeds and report runnable evidence. Do not commit; parent performs phase commit after independent verification.
</details>

## Execution evidence

- RED: actual Ansible fixture failed five expected assertions: cleanup absent,
  Claude installed in base and absent in finalize, OpenCode missing, and custom
  Claude settings overwritten.
- GREEN: `PYTHONDONTWRITEBYTECODE=1 /usr/bin/python3 -m unittest discover -s tests -p '*_test.py'`
  passes three integration tests (58 seconds). The fixture evaluates production
  phase/selection gates with harmless roles, executes actual settings templates
  against fresh and restored files, executes real installer shells with fake curl
  to prove old binaries do not suppress fetching, and executes actual legacy
  cleanup plus bashrc migration while verifying unrelated tools/secrets survive.
- `ansible-playbook --syntax-check -i inventory site.yml` and `git diff --check` pass.
- Parent independently installed current npm distributions into a temporary
  prefix and verified `opencode --version` (1.18.32) and `pi --version` (0.87.0).
  No real guest provisioning performed by this task; download fixtures never
  invoke upstream installers on the host.
- Claude launcher moved from common bashrc customization into the selected
  Claude role. Re-converging a base removes the old launcher and its exact known
  binary/config paths. Clipboard shim content is unchanged.

## Official installer and state references

- Claude: https://code.claude.com/docs/en/setup and https://claude.ai/install.sh.
  Explicit latest channel; native binary under `.local/share/claude/versions`
  with `.local/bin/claude` symlink. Preserve `.claude` and `.claude.json`.
- Codex: https://chatgpt.com/codex/install.sh and
  https://developers.openai.com/codex/config-basic/. Native installer stores
  releases under `.codex/packages/standalone`; it always runs after restore,
  with noninteractive mode enabled. Preserve `.codex`.
- OpenCode: https://opencode.ai/docs/ and
  https://opencode.ai/download. Official `@opencode/cli@latest` npm
  distribution; user-local `.local` install prefix. Settings/data reside in
  `.config/opencode`, `.local/share/opencode`, and `.local/state/opencode`.
- Pi: https://pi.dev/news/2026/5/7/pi-has-a-new-home and
  https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/README.md.
  Official package is now `@earendil-works/pi-coding-agent@latest`; old
  `@mariozechner` scope is deprecated. State remains `.pi/agent`.
  https://raw.githubusercontent.com/earendil-works/pi/main/packages/coding-agent/package.json
  requires Node >=22.19.0; the base's Node 24 satisfies it.
