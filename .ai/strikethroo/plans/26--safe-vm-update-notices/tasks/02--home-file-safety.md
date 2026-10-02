---
id: 2
group: "vm-updates"
dependencies: []
status: "completed"
created: 2026-10-02
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
skills: ["ansible", "python"]
complexity_score: 8
complexity_notes: "Truthful cross-provider provenance and preservation of user-owned data require careful integration."
---
# Preserve user home configuration during explicit reset

## Objective
Implement safe ownership tracking for sand-provisioned home configuration and preserve-home reset protection. Own Ansible roles plus internal/provision/preserve.go and related home safety helpers/tests. Coordinate new fileset changes with task 1. Preserve modified/untracked files, save proposed defaults separately, update baseline only for actually installed defaults; handle symlinks conservatively. Never run provisioning on start or shell.

## Skills Required
ansible, python.

## Acceptance Criteria
- [x] Tests execute filesystem operations and check actual original, proposed-default and baseline contents for unchanged, edited, unknown and symlink cases; Python lifecycle tests and focused preserve reset tests pass.
- [x] Preserve existing VMs, unknown history and user modifications; no automatic provisioning.

## Technical Requirements
Implement safe ownership tracking for sand-provisioned home configuration and preserve-home reset protection. Own Ansible roles plus internal/provision/preserve.go and related home safety helpers/tests. Coordinate new fileset changes with task 1. Preserve modified/untracked files, save proposed defaults separately, update baseline only for actually installed defaults; handle symlinks conservatively. Never run provisioning on start or shell.

## Input Dependencies
Plan 26 and completed dependency tasks [].

## Output Artifacts
Implementation, meaningful tests and evidence recorded here.

## Implementation Notes
<details><summary>Execution guidance</summary>
Read PRE_TASK_EXECUTION.md and follow RED → GREEN → REFACTOR for meaningful business logic. Inspect current provider source semantics before selecting revisions. Keep host state isolated in tests. Write a few tests, mostly integration: test custom business logic, critical user workflows, edge cases and component boundaries; do not test frameworks, trivial getters or obvious static configuration. Combine related scenarios and verify the far side of file/process boundaries. Run the acceptance commands and record exact outcomes. Do not commit other tasks' changes. Set status in-progress then completed after checks. Implement safe ownership tracking for sand-provisioned home configuration and preserve-home reset protection. Own Ansible roles plus internal/provision/preserve.go and related home safety helpers/tests. Coordinate new fileset changes with task 1. Preserve modified/untracked files, save proposed defaults separately, update baseline only for actually installed defaults; handle symlinks conservatively. Never run provisioning on start or shell.
</details>

## Execution evidence

- RED: `safe_home_file_test.py` failed before the guest installer existed; `TestClearFreshBaselines*` failed to compile before the symlink-safe clear script. Both passed after implementation.
- `PYTHONDONTWRITEBYTECODE=1 /usr/bin/python3 -m unittest discover -s tests -p '*_test.py'`: 31 tests passed. The suite includes an Ansible run against a temporary home that verifies installed defaults and baselines, retained edited files, separate proposals, and a zero-change rerun.
- `go test ./internal/provision -count=1`: passed, including reset ordering and actual local filesystem symlink checks for baseline clearing.
- `ansible-playbook -i localhost, --connection=local site.yml --syntax-check`: passed.
- The safe installer runs from a system-owned path; preserved-home reset clears only the new clone's baseline history before restoring the source archive. Unknown source history stays unknown. Agent config and shell blocks use the same ownership gate; credential files are left intact on preserved-home reset, with temporary clone credentials used when needed.
- `safe_home_file_test.py`: 9 filesystem cases passed, including a tracked GitLab credential include carried into refreshed `.gitconfig`; edited config remains byte-for-byte unchanged. `home_role_test.py`: production Ansible tasks passed against a temporary home with that include, edited files and a zero-change rerun.
