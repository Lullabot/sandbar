---
id: 1
group: "agent-context"
dependencies: []
status: "completed"
created: 2026-10-02
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
skills: ['ansible', 'python-integration-testing']
---
# Deploy Global Agent Context

## Objective
Create and deploy shared global instructions and the sandbar-environment skill for selected agents in new VM provisioning.

## Skills Required
ansible, python-integration-testing.

## Acceptance Criteria
- [x] Plan requirements covered without existing-VM migration.
- [x] PYTHONDONTWRITEBYTECODE=1 /usr/bin/python3 -m unittest discover -s tests -p agent_context_test.py; expect exit 0 and installed-path assertions passed. Run ansible-playbook -i localhost, -c local site.yml --syntax-check; expect exit 0.
- [x] Personal content remains intact and verification evidence is recorded.

## Technical Requirements
Follow plan 26, existing Ansible phase selection, supported global paths, and evidence gate. No new runtime dependency.

## Input Dependencies
Plan 26 and existing provisioning roles.

## Output Artifacts
roles/agent-context/, site.yml, tests/agent_context_test.py, related lifecycle tests

## Implementation Notes
<details>
<summary>Execution guidance</summary>

Use the plan as the contract. Read skill-creator guidance for authoring the portable skill. Verify environment facts against roles/user/templates/tmux.conf.j2 and existing docs. Keep the global block short and skill references local. Install via a shared role outside base; do not alter provider orchestration or add update commands. Preserve personal instruction content/modes and unrelated skills, and avoid silent collision replacement. Test actual Ansible writes against isolated temporary guest homes, including all agents individually, combinations, no selection, phase gates, reruns, and collisions. Apply RED/GREEN to meaningful custom installation behavior before production implementation. Write a few tests, mostly integration: verify custom business logic, critical paths, application edge cases, transformations, and component integration; do not test library/framework features, trivial getters, static config, or simple CRUD. Combine related scenarios, avoid per-method tests, and question every test. Mark task in-progress on starting and completed only after concrete checks.

</details>

## Verification Evidence

- RED: the real-role integration test failed because `agent-context` did not exist; a later task-count test failed because dynamic `include_tasks` listed one task but emitted 13 banners.
- GREEN: `PYTHONDONTWRITEBYTECODE=1 /usr/bin/python3 -m unittest discover -s tests -p agent_context_test.py` passed 4 tests, including actual temporary-home writes, collision detection, rerun/refresh, and `--list-tasks` parity after switching to `import_tasks`.
- `PYTHONDONTWRITEBYTECODE=1 /usr/bin/python3 -m unittest discover -s tests -p agent_roles_test.py` passed 7 tests; `ansible-playbook -i localhost, -c local site.yml --syntax-check`, the skill-creator `quick_validate.py` check, and `git diff --check` also passed.
- Integration assertions confirmed personal instruction text, a private `0700` directory and `0600` file, and an unrelated skill survived. Global context was absent for base and no selection, and installed at the expected paths for selected agents.
