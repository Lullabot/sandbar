---
id: 2
group: "agent-context"
dependencies: [1]
status: "completed"
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
- [x] Plan requirements covered without existing-VM migration.
- [x] uvx --with-requirements docs/requirements.txt mkdocs build --strict; expect exit 0. Run full Python lifecycle suite, Ansible syntax check, go vet ./..., go test ./...; expect exit 0.
- [x] Personal content remains intact and verification evidence is recorded.

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

## Verification Evidence

- Updated the existing available-tools and embedded-playbook pages with the four selected-agent instruction locations, shared skill and Claude symlink, authorized guest sudo behavior, preservation and collision handling, per-VM installation scope, and fresh-session guidance. No VM lifecycle or existing-VM migration code changed. No new test was needed for these static documentation edits; the real-role integration suite exercises installation behavior.
- `uvx --with-requirements docs/requirements.txt mkdocs build --strict` exited 0 and built the site in 1.82 seconds. MkDocs reported only an informational note about the existing non-nav `contributing/image-build-audit.md` page.
- Full Python lifecycle suite (`PYTHONDONTWRITEBYTECODE=1 /usr/bin/python3 -m unittest discover -s tests -p '*_test.py' -v`) passed 26 tests in 234.658 seconds, exit 0. The targeted agent-context role suite passed 4 tests in 100.415 seconds, including actual temporary-home installation, preservation, collisions, and repeated runs. Ansible site syntax check and skill quick validation passed.
- `go vet ./...` exited 0. `GOTMPDIR=/var/tmp go test ./...` exited 0 across all packages, including `internal/lima` (16.291s), `internal/provision` (20.480s), and `internal/ui` (83.841s). `gofmt -l .` was empty and `git diff --check` exited 0.
- These checks verify deployment and documentation against the shipped files. They do not verify live agent obedience; no user VM was booted or modified.
