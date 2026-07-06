---
id: 1
group: "ansible-playbook"
dependencies: []
status: "completed"
created: "2026-03-09"
skills:
  - ansible
---
# Add direnv to Ansible Playbook

## Objective
Install direnv via apt and activate its bash hook so it works automatically on login for provisioned VMs.

## Skills Required
- ansible: Editing role defaults and task files (blockinfile, package lists)

## Acceptance Criteria
- [ ] `direnv` is listed in `base_packages` in `roles/base/defaults/main.yml`
- [ ] `eval "$(direnv hook bash)"` is added as the last line of the managed bashrc block in `roles/user/tasks/main.yml`

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- direnv is available in Debian 13 (trixie) apt repositories
- The bash hook must be the last line in the `blockinfile` managed block to correctly wrap `PROMPT_COMMAND`

## Input Dependencies
None — this task modifies existing files with no prerequisites.

## Output Artifacts
- Modified `roles/base/defaults/main.yml` with `direnv` in `base_packages`
- Modified `roles/user/tasks/main.yml` with direnv hook in bashrc block

## Implementation Notes

<details>
<summary>Detailed implementation steps</summary>

1. **Edit `roles/base/defaults/main.yml`**: Add `- direnv` to the `base_packages` list. Insert it alphabetically (after `curl`, before `default-jdk-headless`).

2. **Edit `roles/user/tasks/main.yml`**: In the "Deploy bashrc customizations" task (the `blockinfile` task), add `eval "$(direnv hook bash)"` as the **last line** of the `block:` value. The current block ends with `export EDITOR=vim`. Add the direnv hook line after that.

File paths:
- `/home/andrew.linux/claude-code-ansible/roles/base/defaults/main.yml`
- `/home/andrew.linux/claude-code-ansible/roles/user/tasks/main.yml`

</details>
