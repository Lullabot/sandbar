---
id: 1
group: "ansible-provisioning"
dependencies: []
status: "completed"
created: 2026-07-16
model: "sonnet"
effort: "medium"
skills:
  - ansible
---
# Create the codex Ansible role and gate it as an opt-in toolset selection

## Objective

Add a `roles/codex` Ansible role that installs the OpenAI Codex CLI via the official installer and provisions a friction-free `~/.codex/config.toml`, gated from `site.yml` by a new `toolset_codex` selection variable that defaults to **false** (the first opt-in tool).

## Skills Required

`ansible` — role authoring following this repository's existing conventions (see `roles/claude-code` as the structural mirror).

## Acceptance Criteria

- [x] `roles/codex/tasks/main.yml` exists and mirrors `roles/claude-code/tasks/main.yml`'s sequence: getent home lookup, set `user_home` fact, create `~/.codex` (owner/group = VM user, mode 0755), deploy `config.toml` from template (mode 0644), run the official installer as the VM user with a `creates:` guard, ensure `~/.local/bin` is on PATH via the same idempotent `lineinfile` pattern.
- [x] The installer task uses `set -o pipefail && curl -fsSL https://chatgpt.com/codex/install.sh | sh` with `executable: /bin/bash`, `become_user: "{{ user_name }}"`, and `creates: "{{ user_home }}/.local/bin/codex"` (the verified install destination).
- [x] `roles/codex/templates/codex-config.toml.j2` sets exactly `approval_policy = "never"` and `sandbox_mode = "danger-full-access"` (valid TOML, no other keys).
- [x] `roles/codex/defaults/main.yml` carries a comment (no tunables) explaining, like `roles/claude-code/defaults/main.yml` does, that selection lives in `roles/base/defaults/main.yml` as `toolset_codex` (`sand create --with-codex`).
- [x] `site.yml` gains a `codex` role entry directly after the `claude-code` entry, gated on `toolset_codex | default(false) | bool` AND `provision_phase | default('full') != 'finalize'`, with a comment noting it is opt-in (default false), unlike its siblings.
- [x] `roles/base/defaults/main.yml` gains `toolset_codex: false` alongside the other `toolset_*` selections, and the surrounding comment block (currently saying "All four default true" and naming the four flags) is reworded to stay accurate: five selections, codex opt-in/default-false, still no apt packages for claude or codex.
- [x] Verification: `ansible-playbook site.yml --syntax-check` exits 0 (run from the repo root; it validates the role wiring and templates parse).
- [x] Verification: `python3 -c "import tomllib,sys; print(tomllib.load(open('/tmp/x.toml','rb')))"` (after rendering or copying the template body with no Jinja substitutions needed) confirms the config.toml template body is valid TOML containing both keys.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Follow `roles/claude-code` exactly for task style: `ansible.builtin.getent`, `ansible.builtin.set_fact`, `ansible.builtin.file`, `ansible.builtin.template`, `ansible.builtin.shell` with `args.creates`, `ansible.builtin.lineinfile`.
- The PATH `lineinfile` task is duplicated from claude-code deliberately (regexp `(^|^export )PATH=.*\.local/bin`) — the role must be self-sufficient because codex can be selected with claude de-selected. The upstream installer also appends its own PATH export to a shell profile; the lineinfile regexp is idempotent against that.
- The role installs no apt packages, so `toolset_packages` in `roles/base/defaults/main.yml` is NOT touched.
- Do not provision any credential, `notify` hook, or notification config — plain install plus the two config keys only.

## Input Dependencies

None — first-phase task. Reference material: `roles/claude-code/` (mirror), `site.yml` lines 26–32 (gate pattern), `roles/base/defaults/main.yml` lines 53–76 (selection home and comment block to reword).

## Output Artifacts

- `roles/codex/tasks/main.yml`, `roles/codex/templates/codex-config.toml.j2`, `roles/codex/defaults/main.yml`
- Updated `site.yml` and `roles/base/defaults/main.yml`
- Consumed by Task 4 (docs describe what this role provisions).

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

Verified upstream facts (do not re-derive): the installer at `https://chatgpt.com/codex/install.sh` 302-redirects to the latest GitHub release asset; it installs a symlink at `$HOME/.local/bin/codex`; Linux x86_64 and aarch64 are static musl builds; it needs only `sh`/`curl`/`tar`+gzip (all already in the base image); it self-verifies with `codex --version`.

Copy `roles/claude-code/tasks/main.yml` as the starting point and adapt names: directory `~/.codex`, template `codex-config.toml.j2` → dest `{{ user_home }}/.codex/config.toml`, installer URL and `creates:` path as in the acceptance criteria. Keep task names descriptive ("Install OpenAI Codex using the official installer" etc.).

For the `site.yml` entry, mirror the claude-code block's comment style: two-line comment explaining codex is opt-in (`sand create --with-codex`, default false) while the other toolset roles are opt-out.

In `roles/base/defaults/main.yml`, insert `toolset_codex: false` in alphabetical position among the selections and rework the comment block: it currently reads "All four default true so an unconfigured `sand create` ... installs everything" and "toolset_claude gates the whole claude-code role from site.yml (it installs no apt packages...)". Preserve its explanations but state that codex is the first opt-in selection (default false) and that, like claude, it contributes nothing to `toolset_packages`.

The `defaults/main.yml` for the role itself contains only a comment (see `roles/claude-code/defaults/main.yml` for tone): no tunable defaults; whether it runs is the `toolset_codex` selection living with its siblings.

Syntax-check note: run `ansible-playbook site.yml --syntax-check` from the repo root (ansible.cfg is there). If ansible is not installed on this host, `pip install --user ansible-core` is acceptable, or use `python3 -m venv`; the check must actually run and exit 0 — do not skip it.
</details>
