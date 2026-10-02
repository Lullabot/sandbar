---
id: 2
group: "agent-context"
dependencies: [1]
status: "pending"
created: 2026-10-02
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
skills: ['markdown', 'integration-testing']
---
# Document And Verify Agent Context

## Objective
Document shipped global environment context and verify the completed integration against repository gates.

## Skills Required
markdown, integration-testing.

## Acceptance Criteria
- [ ] Plan requirements covered without existing-VM migration.
- [ ] uvx --with-requirements docs/requirements.txt mkdocs build --strict; expect exit 0. Run full Python lifecycle suite, Ansible syntax check, go vet ./..., go test ./...; expect exit 0.
- [ ] Personal content remains intact and verification evidence is recorded.

## Technical Requirements
Follow plan 26, existing Ansible phase selection, supported global paths, and evidence gate. No new runtime dependency.

## Input Dependencies
Completed task 1 deployment bundle and tests.

## Output Artifacts
docs/getting-started/available-tools.md, docs/contributing/ansible-playbook.md, validation evidence

## Implementation Notes
<details>
<summary>Execution guidance</summary>

Read completed deployment code and inspect its actual boundary behavior. Update existing documentation only, including supported locations, selected agents, sudo behavior, new-VM scope, and fresh session expectations. Run relevant isolated Ansible integration tests and syntax check, Python lifecycle suite, go vet ./..., go test ./..., and strict MkDocs build via uvx --with-requirements docs/requirements.txt mkdocs build --strict. Do not boot or change user VMs. Do not claim live agent obedience. Report full outputs/exit codes and any limitation. No dedicated tests are warranted for documentation; existing integration checks exercise installation logic. Mark status in-progress then completed with evidence.

</details>
