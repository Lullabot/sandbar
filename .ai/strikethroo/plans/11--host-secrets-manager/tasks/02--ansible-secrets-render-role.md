---
id: 2
group: "vm-rendering"
dependencies: []
status: "pending"
created: 2026-07-09
model: "sonnet"
effort: "high"
skills:
  - ansible
  - git
---
# Ansible `secrets` Role — Render Targets Inside the VM

## Objective
Create the provisioning role that turns secrets (received as Ansible vars over stdin) into working in-VM configuration: a VM-global env file, a file-backed git/`gh` credential store with per-org `includeIf` overrides (live-refreshable), and managed per-directory `.env` files loaded by direnv. This defines the Ansible var contract the provisioning tasks feed.

## Skills Required
- **ansible** — a new role (e.g. `roles/secrets`) with idempotent, `no_log` tasks; wire it into `site.yml`.
- **git** — `git config` credential helpers and `includeIf "gitdir:..."` conditional includes for per-directory token selection.

## Acceptance Criteria
- [ ] `ansible-playbook --syntax-check site.yml` passes with the new role included.
- [ ] Running the role against a local test target with a sample `secrets_global` var produces `~/.config/sandbar/secrets.env` (mode `0600`) containing `export NAME=value`, and that file is sourced from the user's bashrc.
- [ ] With a `secrets_github` entry `{scope: "github.com/acme", token: "T"}`, the role writes a scope-specific credential file and a git `includeIf "gitdir:~/github.com/acme/"` block; `git -C ~/github.com/acme/<repo> config --show-origin credential.helper` resolves to the acme-specific helper/file.
- [ ] Updating the credential file's token and immediately running `git` in that dir uses the new token with **no new shell** (demonstrating the file is re-read per invocation).
- [ ] With a `secrets_dir_env` entry, the role writes `~/<scope>/.env` (mode `0600`) and runs `direnv allow` for it (no manual approval needed).
- [ ] All token/value-bearing tasks use `no_log: true`.

## Technical Requirements
- Consume these Ansible vars (the contract; tasks 4 and 5 populate them from the host store):
  - `secrets_global`: list of `{ name, value }`.
  - `secrets_github`: list of `{ scope, token }` — empty `scope` = VM-wide default GitHub token; non-empty = per-subtree override.
  - `secrets_dir_env`: list of `{ scope, name, value }`.
- GitHub auth must be **file-backed, not env-var-based**, so rotation is live. Use `gh auth`'s file-based store and/or a git `credential.helper` that reads a token file per call; select per-org via `includeIf "gitdir:~/<scope>/"`.
- Preserve the existing checkout layout (`~/<host>/<org>/…`) that `roles/project` already uses.

## Input Dependencies
None (defines the var contract; aligns with the host JSON schema from task 1).

## Output Artifacts
- `roles/secrets/` (tasks/defaults) wired into `site.yml`.
- The rendered in-VM files: `~/.config/sandbar/secrets.env`, git credential store + `includeIf` config, per-dir `.env` files.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

**Global env** — render `~/.config/sandbar/secrets.env` (`0600`, owner = user) with one `export {{ item.name }}={{ item.value | quote }}` per `secrets_global` item, and ensure bashrc sources it (add a single `[ -f ~/.config/sandbar/secrets.env ] && . ~/.config/sandbar/secrets.env` line, idempotently — reuse the bashrc-edit pattern already in `roles/user`). Use `no_log: true`.

**GitHub (live) — the core of the design.** For each `secrets_github` item:
- Write the token to a per-scope file, e.g. `~/.config/sandbar/git-credentials/<scope-slug>` in git's `store` format (`https://x-access-token:{{ token }}@github.com`), mode `0600`, `no_log`. Configure a `credential.helper=store --file=<that file>` bound to the scope.
- For the default (empty scope), set it in the user's global `~/.gitconfig`.
- For a non-empty scope, write a scoped include file (e.g. `~/.config/sandbar/gitconfig.d/<scope-slug>`) that sets `credential.helper` to that scope's file, and add to `~/.gitconfig`:
  ```
  [includeIf "gitdir:~/{{ item.scope }}/"]
      path = ~/.config/sandbar/gitconfig.d/<scope-slug>
  ```
  (Trailing slash on the gitdir pattern matches the subtree.) Because `store` re-reads its file each invocation, replacing the file's token is picked up live.
- Also refresh `gh`'s file store if present so `gh` CLI benefits (`gh auth` reads `~/.config/gh/hosts.yml` per call). Keep this best-effort.
- Note: a file-backed credential helper takes precedence appropriately only if no conflicting `GH_TOKEN` env var is set — do **not** export `GH_TOKEN` for GitHub anymore (that was the old, non-live path being replaced).

**Directory env (direnv)** — for each `secrets_dir_env` item: ensure `~/{{ item.scope }}` exists, write/merge `~/{{ item.scope }}/.env` (`0600`) with `{{ item.name }}={{ item.value }}`, then run `direnv allow ~/{{ item.scope }}` as the user (mirror the existing `become_user` + `HOME` env pattern in `roles/project`). direnv stays installed (`roles/base`) and hooked (`roles/user`); the user never runs `direnv allow` themselves.

Guard every task with `when: item... | length > 0` style conditionals so an empty var list is a no-op. Respect `provision_phase` conventions used by other roles (secrets belong in the finalize/full phase, not the base image — the base must stay identity- and secret-free).
</details>
