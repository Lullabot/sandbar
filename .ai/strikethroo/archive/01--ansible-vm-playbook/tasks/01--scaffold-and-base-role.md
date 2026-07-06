---
id: 1
group: "ansible-vm-playbook"
dependencies: []
status: "completed"
created: 2026-03-01
skills:
  - ansible-playbook-authoring
---
# Scaffold Project Structure and Implement Base Role

## Objective
Create the Ansible project scaffolding (site.yml, inventory, group_vars, role directories) and implement the base role that configures hostname, locale, and installs core APT packages.

## Skills Required
- Ansible playbook authoring (roles, variables, handlers)

## Acceptance Criteria
- [ ] `site.yml` exists and references all four roles in order: base, user, dev-tools, claude-code
- [ ] `inventory` file exists with placeholder host and `ansible_user=root`
- [ ] `group_vars/all.yml` exists with configurable variables (hostname, locale, etc.)
- [ ] `roles/base/tasks/main.yml` configures hostname (`/etc/hostname` + `/etc/hosts`), locale (using `locale-gen`), and installs all base APT packages
- [ ] `roles/base/handlers/main.yml` exists (for hostname change handler if needed)
- [ ] `roles/base/defaults/main.yml` defines default values for hostname (`claude`) and domain (`lan`), locale (`en_CA.UTF-8`)
- [ ] All four role directory structures exist (even if tasks/main.yml are stubs for roles 2-4)
- [ ] Base APT package list includes: `vim`, `htop`, `ncdu`, `curl`, `wget`, `rsync`, `unzip`, `git`, `tmux`, `bats`, `default-jdk-headless`, `python3`, `python3-pip`, `nodejs`, `npm`, `golang`, `mkcert`, `screen`, `lsof`, `traceroute`, `openssh-server`, `sudo`, `ca-certificates`, `gnupg`, `bash-completion`, `locales`

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Ansible playbook format (YAML)
- Debian 13 (trixie) as target OS
- Connect as root via SSH key
- Use `ansible.builtin.hostname`, `ansible.builtin.template` or `ansible.builtin.lineinfile` for /etc/hosts, `ansible.builtin.locale_gen`, `ansible.builtin.apt` modules
- `/etc/hosts` must have: `127.0.1.1 claude.lan claude` (FQDN first, short name second)

## Input Dependencies
- Plan document with architectural details

## Output Artifacts
- Complete project scaffolding in `/home/claude/ansible/`
- Working base role
- Stub role directories for user, dev-tools, claude-code

## Implementation Notes
- The `site.yml` playbook should target `all` hosts and connect as root
- Role execution order matters: base → user → dev-tools → claude-code
- Ensure `apt update` runs before package installation (use `update_cache: yes`)
