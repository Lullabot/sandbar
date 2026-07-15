---
id: 1
group: "base-image-clis"
dependencies: []
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "medium"
complexity_score: 5
complexity_notes: "Single-domain Ansible change following an existing pattern, but with two distinct install mechanisms and an architecture-mapping edge case that must be correct for both amd64 and arm64 guests."
skills:
  - ansible
---
# Install glab and drupalorg in the dev-tools role

## Objective
Extend the `dev-tools` Ansible role so the base image ships the GitLab CLI
(`glab`) and the Drupal.org CLI (`drupalorg`), alongside the existing GitHub CLI
(`gh`). Both are installed in the same role/phase that already bakes `gh` into
the clone source, so every provisioned VM inherits them for free.

## Skills Required
- `ansible` — authoring role tasks and defaults using `get_url`, `apt`, `fail`,
  and fact-based architecture mapping.

## Acceptance Criteria
- [ ] `roles/dev-tools/defaults/main.yml` defines pinned versions for both tools
      (`glab` and `drupalorg`) and an architecture map for the `glab` `.deb`.
- [ ] `roles/dev-tools/tasks/main.yml` installs `glab` from GitLab's official
      signed `.deb` release asset, selecting the correct asset for the guest
      architecture and failing loudly on unsupported architectures.
- [ ] `roles/dev-tools/tasks/main.yml` installs `php-cli` + `php-curl` and places
      the pinned `drupalorg.phar` at `/usr/local/bin/drupalorg`, executable and
      on `PATH`.
- [ ] The new blocks are placed after the existing `gh` block; the `gh` install
      and all other existing tasks are unchanged.
- [ ] Any existing enumerated list of pre-installed base-image tooling that
      mentions `gh` is updated to also list `glab` and `drupalorg` (skip if no
      such list exists).
- [ ] `ansible-playbook --syntax-check site.yml` exits 0.
- [ ] `grep -nE 'glab|drupalorg|php-cli' roles/dev-tools/tasks/main.yml` shows
      both install blocks, and `grep -E 'glab|drupalorg' roles/dev-tools/defaults/main.yml`
      shows both pinned versions.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Edit only `roles/dev-tools/tasks/main.yml` and `roles/dev-tools/defaults/main.yml`
  (plus one docs file iff an enumerated tool list exists).
- Follow the existing idioms in the file (see the `gh`, `ddev`, `cloudflared`,
  and `uv` blocks).
- `glab`: no official GitLab apt repo exists — install the vendor's official
  `.deb` release asset via `apt`, version pinned in defaults.
- `drupalorg`: PHAR requiring PHP 8.1+ with cURL; Debian trixie's `php-cli`
  metapackage provides PHP 8.4. `git` is already installed via `base_packages`.

## Input Dependencies
None. Reference implementation is the existing `gh` block at
`roles/dev-tools/tasks/main.yml` lines 103–127.

## Output Artifacts
- Updated `roles/dev-tools/tasks/main.yml` (two new install blocks).
- Updated `roles/dev-tools/defaults/main.yml` (three new variables).
- Optionally an updated docs file listing the newly baked-in CLIs.
These are consumed by Task 2 (end-to-end verification).

## Implementation Notes

<details>
<summary>Step-by-step implementation</summary>

### 1. Add defaults

Append to `roles/dev-tools/defaults/main.yml` (before the final `devtools_user_name`
line is fine, or at the end — order does not matter):

```yaml
# GitLab CLI (glab) — pinned official .deb release asset
devtools_glab_version: "1.108.0"
devtools_glab_deb_arch_map:
  x86_64: amd64
  aarch64: arm64

# Drupal.org CLI (drupalorg) — pinned PHAR release
devtools_drupalorg_cli_version: "0.10.3"
```

> Confirm these are still the latest upstream releases at implementation time and
> bump if newer exist:
> - glab releases: https://gitlab.com/gitlab-org/cli/-/releases
> - drupalorg-cli releases: https://github.com/mglaman/drupalorg-cli/releases

### 2. Add the glab install block

In `roles/dev-tools/tasks/main.yml`, insert **after** the `Install GitHub CLI`
task (currently ending at line 127) and **before** the `# uv` block (line 129):

```yaml
# GitLab CLI (glab)
- name: Resolve glab .deb architecture
  ansible.builtin.set_fact:
    devtools_glab_deb_arch: "{{ devtools_glab_deb_arch_map[ansible_architecture] | default('') }}"

- name: Fail on unsupported architecture for glab
  ansible.builtin.fail:
    msg: "Unsupported architecture '{{ ansible_architecture }}' for the glab .deb package"
  when: devtools_glab_deb_arch == ''

- name: Download GitLab CLI (glab) .deb package
  ansible.builtin.get_url:
    url: "https://gitlab.com/gitlab-org/cli/-/releases/v{{ devtools_glab_version }}/downloads/glab_{{ devtools_glab_version }}_linux_{{ devtools_glab_deb_arch }}.deb"
    dest: "/tmp/glab_{{ devtools_glab_version }}_linux_{{ devtools_glab_deb_arch }}.deb"
    mode: "0644"

- name: Install GitLab CLI (glab)
  ansible.builtin.apt:
    deb: "/tmp/glab_{{ devtools_glab_version }}_linux_{{ devtools_glab_deb_arch }}.deb"
    state: present
```

The asset-name arch (`amd64`/`arm64`) differs from Ansible's `ansible_architecture`
(`x86_64`/`aarch64`); the map handles that. `ansible.builtin.apt` with `deb:`
installs a local package file and resolves dependencies.

### 3. Add the drupalorg install block

Immediately after the glab block:

```yaml
# Drupal.org CLI (drupalorg)
- name: Install PHP runtime for the Drupal.org CLI
  ansible.builtin.apt:
    name:
      - php-cli
      - php-curl
    state: present
    update_cache: true

- name: Install Drupal.org CLI (drupalorg)
  ansible.builtin.get_url:
    url: "https://github.com/mglaman/drupalorg-cli/releases/download/{{ devtools_drupalorg_cli_version }}/drupalorg.phar"
    dest: /usr/local/bin/drupalorg
    owner: root
    group: root
    mode: "0755"
```

The PHAR is architecture-independent (runs on the installed PHP). Mode `0755`
makes it directly executable as `drupalorg` since `/usr/local/bin` is on `PATH`.

> Idempotency note: `get_url` with a fixed `dest` will not re-download when the
> file already exists, which is correct for fresh base builds. If you want a
> version bump to take effect on an already-provisioned host, add a
> `force: true` or a version check — but do not over-engineer this; fresh base
> images are rebuilt from a clean VM, so the simple form is sufficient.

### 4. Update documentation (conditional)

Search for an enumerated list of pre-installed base-image tooling that names
`gh`:

```bash
grep -rniE '\bgh\b|github cli' README.md AGENTS.md docs/ 2>/dev/null
```

If such a human- or agent-facing list exists (e.g. "the base image ships gh,
ddev, cloudflared, ..."), add `glab` (GitLab CLI) and `drupalorg` (Drupal.org
CLI) to it. If no enumerated list exists, make no documentation change — do not
invent one.

### 5. Verify (task-local)

```bash
ansible-playbook --syntax-check site.yml
grep -nE 'glab|drupalorg|php-cli' roles/dev-tools/tasks/main.yml
grep -E 'glab|drupalorg' roles/dev-tools/defaults/main.yml
```

All three must succeed and show the new content. (Live in-VM verification is
Task 2.)

### Scope guardrails
Install the binaries only — no shell completions, no auth setup, no aliases, no
wrapper commands. `gh` is installed with none of those, and the work order says
"like with the GitHub CLI".
</details>
