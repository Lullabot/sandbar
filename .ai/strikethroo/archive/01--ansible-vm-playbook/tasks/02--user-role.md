---
id: 2
group: "ansible-vm-playbook"
dependencies: [1]
status: "completed"
created: 2026-03-01
skills:
  - ansible-playbook-authoring
  - jinja2-templating
---
# Implement User Role

## Objective
Create the user role that sets up the `claude` user with all dotfiles, SSH configuration, sudo access, tmux, git, and bashrc customizations.

## Skills Required
- Ansible playbook authoring (user module, authorized_key module, template module, blockinfile module)
- Jinja2 templating for dotfiles

## Acceptance Criteria
- [ ] `roles/user/tasks/main.yml` creates user `claude` with home directory, bash shell, and `sudo` group membership
- [ ] Deploys sudoers config (`%sudo ALL=(ALL:ALL) NOPASSWD: ALL`) via `/etc/sudoers.d/nopasswd-sudo` with mode 0440
- [ ] Fetches SSH authorized_keys from `https://github.com/deviantintegral.keys` (non-exclusive mode)
- [ ] Deploys `~/.ssh/rc` from template, executable, with SSH agent forwarding symlink logic
- [ ] Deploys `~/.tmux.conf` from template with exact content: prefix C-a, escape-time 0, base-index 1, mouse on, history-limit 50000, vi copy mode, intuitive splits (| and S), new windows keeping current path, config reload bind, `set-environment -g 'SSH_AUTH_SOCK' ~/.ssh/ssh_auth_sock`
- [ ] Deploys `~/.gitconfig` from template with user.name, user.email, push.autoSetupRemote, includeIf for ~/lullabot/
- [ ] Deploys `~/.gitconfig-lullabot` with user.email = andrew.berry@lullabot.com
- [ ] Appends bashrc customizations via `blockinfile`: PATH with ~/.local/bin, IS_SANDBOX=1, claude alias, EDITOR=vim, VISUAL=vim
- [ ] `roles/user/defaults/main.yml` defines git_user_name and git_user_email variables with defaults

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Use `ansible.builtin.user` for user creation
- Use `ansible.posix.authorized_key` with `url` parameter for GitHub keys
- Use `ansible.builtin.template` for all dotfiles (.tmux.conf, .gitconfig, .gitconfig-lullabot, .ssh/rc)
- Use `ansible.builtin.blockinfile` for bashrc extras (idempotent)
- Sudoers file must use `validate: 'visudo -cf %s'`
- Templates go in `roles/user/templates/`
- The `docker` group is NOT added here — it's done in dev-tools after Docker is installed

## Input Dependencies
- Task 1: Project scaffolding and role directory structure

## Output Artifacts
- `roles/user/tasks/main.yml`
- `roles/user/defaults/main.yml`
- `roles/user/templates/tmux.conf.j2`
- `roles/user/templates/gitconfig.j2`
- `roles/user/templates/gitconfig-lullabot.j2`
- `roles/user/templates/ssh_rc.j2`

## Implementation Notes
- The SSH rc script content:
  ```
  #!/bin/bash
  if [ ! -S ~/.ssh/ssh_auth_sock ] && [ -S "$SSH_AUTH_SOCK" ]; then
      ln -sf $SSH_AUTH_SOCK ~/.ssh/ssh_auth_sock
  fi
  ```
- Git includeIf uses `gitdir:~/lullabot/` (trailing slash important for matching subdirectories)
- Default git_user_name: "Andrew Berry", default git_user_email: "andrew@furrypaws.ca"
