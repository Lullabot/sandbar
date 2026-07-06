---
id: 4
group: "ansible-vm-playbook"
dependencies: [1]
status: "completed"
created: 2026-03-01
skills:
  - ansible-playbook-authoring
---
# Implement Claude Code Role

## Objective
Create the claude-code role that installs the Claude Code CLI and configures it for autonomous (dangerously-skip-permissions) operation.

## Skills Required
- Ansible playbook authoring (shell, file, template modules)

## Acceptance Criteria
- [ ] Claude Code installed via official installer (`curl -fsSL https://claude.ai/install.sh | sh`) as claude user
- [ ] Installation is idempotent — checks for `~/.local/bin/claude` before running installer
- [ ] `~/.claude/` directory created with correct ownership
- [ ] `~/.claude/settings.json` deployed with `{"skipDangerousModePermissionPrompt": true}`
- [ ] `roles/claude-code/tasks/main.yml` is complete
- [ ] `roles/claude-code/templates/claude-settings.json.j2` exists

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Use `ansible.builtin.shell` with `creates: /home/claude/.local/bin/claude` for idempotent installation
- Run installer as claude user via `become_user: claude`
- Use `ansible.builtin.file` to create `~/.claude/` directory
- Use `ansible.builtin.template` or `ansible.builtin.copy` with `content` for settings.json
- `~/.local/bin` is already in PATH via the bashrc block set up by the user role

## Input Dependencies
- Task 1: Project scaffolding and role directory structure

## Output Artifacts
- `roles/claude-code/tasks/main.yml`
- `roles/claude-code/templates/claude-settings.json.j2`

## Implementation Notes
- The installer script downloads a standalone binary to `~/.local/share/claude/versions/` and symlinks to `~/.local/bin/claude`
- No additional configuration beyond settings.json is needed
- The bashrc alias (`claude --dangerously-skip-permissions`) is handled by the user role
