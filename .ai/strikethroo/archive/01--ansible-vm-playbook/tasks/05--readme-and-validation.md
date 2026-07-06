---
id: 5
group: "ansible-vm-playbook"
dependencies: [2, 3, 4]
status: "completed"
created: 2026-03-01
skills:
  - ansible-playbook-authoring
  - documentation
---
# Create README and Run Static Analysis

## Objective
Write the project README.md documenting how to use the playbook, then install Ansible and run static analysis (`ansible-playbook --syntax-check` and `ansible-lint`) to validate the playbook.

## Skills Required
- Documentation writing
- Ansible static analysis tools

## Acceptance Criteria
- [ ] `README.md` exists in `/home/claude/ansible/` with: prerequisites, inventory configuration instructions, how to run the playbook, and list of configurable variables with defaults
- [ ] Ansible is installed from apt on this machine
- [ ] `ansible-playbook --syntax-check -i inventory site.yml` passes
- [ ] `ansible-lint site.yml` runs (fix any critical errors, warnings are acceptable)
- [ ] Any errors found during validation are fixed in the role files

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Install `ansible` and `ansible-lint` from apt: `sudo apt-get install -y ansible ansible-lint`
- Run syntax check: `ansible-playbook --syntax-check -i inventory site.yml`
- Run linting: `ansible-lint site.yml` (from the ansible project directory)
- README should document all variables from `group_vars/all.yml` and role defaults

## Input Dependencies
- Task 2: User role (complete)
- Task 3: Dev-tools role (complete)
- Task 4: Claude-code role (complete)

## Output Artifacts
- `/home/claude/ansible/README.md`
- Validated, lint-clean playbook

## Implementation Notes
- The README should be practical and concise
- If ansible-lint reports errors, fix them in the affected role files
- Focus on critical errors; informational warnings are acceptable
