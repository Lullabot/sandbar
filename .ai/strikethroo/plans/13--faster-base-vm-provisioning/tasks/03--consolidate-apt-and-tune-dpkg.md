---
id: 3
group: "tier-1-in-guest"
dependencies: [2]
status: "pending"
created: 2026-07-13
model: "sonnet"
effort: "high"
skills:
  - ansible
  - debian-packaging
---
# Collapse six base-phase apt passes into one transaction, and tune dpkg

## Objective

Turn six serialized repo-add → update → install cycles into a single `apt-get update` and a single dpkg transaction, and remove fsync-per-file, documentation unpacking, and translation-index fetching from the base build.

## Skills Required

- **ansible** — restructuring the apt tasks across `roles/base` and `roles/dev-tools`.
- **debian-packaging** — apt sources/keyrings, the `_apt` sandbox user's permission requirements, `dpkg.cfg.d` and `apt.conf.d` fragments.

## Acceptance Criteria

- [ ] All five third-party keyrings and sources-list files (NodeSource, Docker, ddev, Cloudflare/cloudflared, GitHub CLI) are written **before** any package install.
- [ ] Exactly **one** task in the base phase performs `apt-get update` (`update_cache`), and exactly **one** task performs the package install. Verify: `grep -rn 'update_cache' roles/` shows at most one base-phase occurrence (plus the finalize-only upgrade, which task 9 removes).
- [ ] The single install task covers Debian base packages, `nodejs`, the Docker package set, ddev, cloudflared, and `gh`.
- [ ] **The `_apt` keyring assertion still passes.** Every file in `/etc/apt/keyrings/` must be readable by the `_apt` sandbox user: `sudo -u _apt test -r /etc/apt/keyrings/<each>` succeeds. This is a regression the project has been bitten by before — treat it as this task's acceptance test.
- [ ] `apt-get update` succeeds in the guest with no `NO_PUBKEY` / permission warnings.
- [ ] A `/etc/dpkg/dpkg.cfg.d/` fragment sets `force-unsafe-io` during the base phase, and `path-exclude` rules drop `/usr/share/doc/*` and `/usr/share/man/*` (with `path-include` for `copyright` files).
- [ ] `/etc/apt/apt.conf.d/` sets `Acquire::Languages "none";`.
- [ ] **`force-unsafe-io` is removed before the base becomes a clone source** (end of the base phase), so user VMs retain normal write durability. The doc/man exclusions and the language setting persist (they are safe and beneficial).
- [ ] `ansible-playbook --syntax-check site.yml` passes.
- [ ] Role boundaries survive: `base` still owns the OS/runtime layer and `dev-tools` the tooling layer. The consolidation is about *transactions*, not about merging the roles.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Today's six base-phase `update_cache: true` passes: `Install base packages` (`roles/base/tasks/main.yml` ~:47) and `Install Node.js` (~:107); Docker (`roles/dev-tools/tasks/main.yml` ~:41), ddev (~:80), cloudflared (~:101), gh (~:127). A seventh (`apt upgrade dist`, ~roles/base:18) runs only in finalize and is out of scope here (task 9 handles it). An eighth in `roles/samba` never fires (`samba_enabled: false` for Lima).
- Two keys are dearmored with `gpg --dearmor` (nodesource.gpg, ddev.gpg); the rest are written directly. `curl` and `gnupg` are supplied by the Lima dependency script as of task 2 — this is what makes a single playbook apt pass possible.
- Keyring files must be world-readable (`mode: "0644"`), because apt drops privileges to the `_apt` user when fetching.

## Input Dependencies

- Task 2: the Lima dependency script now installs `curl`, `gnupg`, and `ca-certificates`, so repo registration can run before the single install pass.

## Output Artifacts

- A base phase with one `apt-get update` and one install transaction.
- dpkg/apt tuning fragments scoped correctly (unsafe-io base-only; doc/man exclusions persistent).

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Ordering.** Restructure the base phase into three clear blocks:

1. **Write apt/dpkg config fragments** (before anything is installed, so the very first install already benefits):

```yaml
- name: Speed up dpkg for the base build (disposable builder — removed before cloning)
  ansible.builtin.copy:
    dest: /etc/dpkg/dpkg.cfg.d/99-sand-base-speed
    mode: "0644"
    content: |
      force-unsafe-io
  when: provision_phase | default('full') != 'finalize'

- name: Stop unpacking docs and man pages
  ansible.builtin.copy:
    dest: /etc/dpkg/dpkg.cfg.d/99-sand-nodoc
    mode: "0644"
    content: |
      path-exclude=/usr/share/doc/*
      path-include=/usr/share/doc/*/copyright
      path-exclude=/usr/share/man/*
  when: provision_phase | default('full') != 'finalize'

- name: Stop fetching translation indexes
  ansible.builtin.copy:
    dest: /etc/apt/apt.conf.d/99-sand-no-languages
    mode: "0644"
    content: |
      Acquire::Languages "none";
  when: provision_phase | default('full') != 'finalize'
```

2. **Register ALL keyrings and repos** — move the key/sources tasks from `roles/dev-tools` so they run before the install. Keep them in their own role files if you like, but they must execute earlier in the play. Every keyring task must set `mode: "0644"`:

```yaml
- name: Install the Docker keyring
  ansible.builtin.get_url:
    url: https://download.docker.com/linux/debian/gpg
    dest: /etc/apt/keyrings/docker.asc
    mode: "0644"      # MUST be readable by the _apt sandbox user
```

For the two that need dearmoring:

```yaml
- name: Dearmor the NodeSource key
  ansible.builtin.shell: |
    curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key \
      | gpg --dearmor -o /etc/apt/keyrings/nodesource.gpg
  args:
    creates: /etc/apt/keyrings/nodesource.gpg

- name: Make the NodeSource keyring readable by _apt
  ansible.builtin.file:
    path: /etc/apt/keyrings/nodesource.gpg
    mode: "0644"
```

3. **One update, one install:**

```yaml
- name: Refresh apt indexes (the only update in the base phase)
  ansible.builtin.apt:
    update_cache: true

- name: Install every base-phase package in a single transaction
  ansible.builtin.apt:
    name: "{{ base_packages + nodejs_packages + docker_packages + devtools_packages + optional_toolset_packages }}"
    state: present
    update_cache: false      # already refreshed above
```

Build the package list from variables so task 5 (the tool-set) can add/remove entries without touching this structure. Set `update_cache: false` explicitly on the install so a future edit cannot silently reintroduce a second refresh.

4. **Remove `force-unsafe-io` at the end of the base phase**, so clones get stock durability:

```yaml
- name: Restore safe dpkg IO before this image is used as a clone source
  ansible.builtin.file:
    path: /etc/dpkg/dpkg.cfg.d/99-sand-base-speed
    state: absent
  when: provision_phase | default('full') == 'base'
```

Keep `99-sand-nodoc` and `99-sand-no-languages` in place — they are safe and keep the user's own `apt install` fast.

**Non-apt installs stay where they are** (`uv` via `curl | sh`, Claude Code via `curl | bash`, `mkcert -install`) — they are not apt transactions and are out of scope.

**Verification.** In a built base:

```sh
for f in /etc/apt/keyrings/*; do sudo -u _apt test -r "$f" || echo "UNREADABLE: $f"; done
sudo apt-get update    # must succeed cleanly
```

This mirrors the CI assertion exactly. Run it before declaring the task done.

</details>
