---
id: 2
group: "guest-config"
dependencies: []
status: "pending"
created: 2026-11-05
models:
  anthropic: "claude-sonnet-5"
  openai: "gpt-6-sol"
effort: "medium"
skills:
  - ansible
  - jinja2
---
# Guest ddev-ports role and sandbar-environment skill references

## Objective
Write the VM's slot ports into the guest user's `~/.ddev/global_config.yaml` during finalize/full, and teach the in-guest `sandbar-environment` agent skill this VM's ddev ports and URL form.

## Skills Required
Ansible role authoring; Jinja2 templating of the agent-context references.

## Acceptance Criteria
- [ ] New role `roles/ddev-ports` (`tasks/main.yml`) sets these top-level keys in `<guest home>/.ddev/global_config.yaml` with one `ansible.builtin.lineinfile` per key (`regexp: '^<key>:'`, `line: '<key>: <port>'`, `create: true`), owned by `user_name`, creating `~/.ddev` (owned by the user) if absent: `router_http_port`, `router_https_port`, `mailpit_http_port`, `mailpit_https_port`, `xhgui_http_port`, `xhgui_https_port`, `traefik_monitor_port`, from vars `ddev_router_http_port`, `ddev_router_https_port`, `ddev_mailpit_http_port`, `ddev_mailpit_https_port`, `ddev_xhgui_http_port`, `ddev_xhgui_https_port`, `ddev_traefik_monitor_port`.
- [ ] The role does nothing when the port vars are undefined (guard on `ddev_router_http_port is defined`), never references `toolset_ddev`, and needs neither the ddev binary nor Docker.
- [ ] `site.yml` lists the role with `when: provision_phase | default('full') != 'base'`, placed after `mkcert-ca`, with a short comment on why it is not in `dev-tools` (skipped in finalize) and why it edits keys rather than calling `ddev config global` (no XHGui flags).
- [ ] A second run with the same vars reports no change (idempotent by construction).
- [ ] `roles/agent-context/templates/references/this-vm.md.j2` states, when `ddev_router_https_port is defined`, the router HTTP/HTTPS, Mailpit, XHGui ports and the URL form `https://<project>.ddev.site:<https-port>`; when undefined it says ddev uses its default ports (or ddev is not configured) — no Jinja error either way.
- [ ] `roles/agent-context/files/references/ports.md` drops the "move the router to 8080/8443" advice, points to `this-vm.md` for this VM's ddev ports, notes that a project's own `.ddev/config.yaml` port settings override the global ones, and updates the `remote-ssh` `ssh -L` example to forward this VM's HTTPS port.
- [ ] The skill's `SKILL.md` description/index mentions ddev URLs and ports so the skill triggers for them.
- [ ] `ansible-playbook --syntax-check` (as the `lint` job runs it) and `PYTHONDONTWRITEBYTECODE=1 /usr/bin/python3 -m unittest discover -s tests -p '*_test.py'` pass.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Var names above are fixed by task 1 (`internal/provision/vars.go` emits them as integers for finalize/full on Lima with a recorded slot). Do not rename.
- `roles/` is a single `go:embed` entry, so a new role needs no embed or rsync-filter change; do not add files under a role's `files/` that are package-manager output.
- Follow the home-lookup pattern in `roles/mkcert-ca/tasks/main.yml` (`ansible.builtin.getent` on `user_name`, home = `ansible_facts.getent_passwd[user_name][4]`).

## Input Dependencies
None (var names are fixed in this task's criteria).

## Output Artifacts
- `roles/ddev-ports/tasks/main.yml`, `site.yml` entry, updated `roles/agent-context` references — exercised by task 4's `lima-e2e` assertion.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- Read `site.yml` first: `dev-tools` is gated `!= 'finalize'`, so it never runs on a clone. `mkcert-ca`, the agent roles, `agent-context` and `project` run in finalize/full. Place `ddev-ports` right after `mkcert-ca`.
- Read `.ddev`'s global config format: a flat YAML map of top-level keys. ddev rewrites the whole file when it saves config but preserves values it read, so per-key edits persist. Whole-home preservation restores `~/.ddev` before finalize, so this role's writes win on a reset; that is intended.
- Task sketch:
  ```yaml
  - name: Look up {{ user_name }}'s home
    ansible.builtin.getent: {database: passwd, key: "{{ user_name }}"}
    when: ddev_router_http_port is defined
  - name: Ensure ~/.ddev exists
    ansible.builtin.file: {path: "{{ home }}/.ddev", state: directory, owner: "{{ user_name }}", group: "{{ user_name }}", mode: "0755"}
  - name: Set this VM's ddev ports in the global config
    ansible.builtin.lineinfile:
      path: "{{ home }}/.ddev/global_config.yaml"
      regexp: "^{{ item.key }}:"
      line: "{{ item.key }}: {{ item.value }}"
      create: true
      owner/group: user_name, mode: "0644"
    loop: [...seven key/value pairs...]
  ```
  Use a block with the `when:` guard rather than repeating it. Check the group name the codebase uses for the user (other roles may use `user_name` for both owner and group, or omit group) and match it.
- Check `tests/` for Python tests that pin the `site.yml` role list or role structure (e.g. `tests/agent_roles_test.py`); update any that enumerate roles so they include `ddev-ports`, and run them.
- In `this-vm.md.j2`, follow the file's existing tone and how it uses `sandbar_profile_type` (local vs remote-ssh vs proxmox phrasing). Proxmox never receives the vars, so the undefined branch covers it.
- Keep wording consistent with Lima behaviour: on local Lima the same port is forwarded to the workstation's `127.0.0.1`, so `https://<project>.ddev.site:<https-port>` works in the workstation browser; on remote-ssh the user forwards that port with `ssh -L`.
- Verification: run the `lint` job's commands from `.github/workflows/test.yml` locally (syntax check + Python unit tests). Also render the template mentally/with a quick `python3 -c` jinja2 render if jinja2 is available, for both defined and undefined vars.
</details>
