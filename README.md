# Claude Code Development VM Playbook

Ansible playbook to provision a Debian 13 (trixie) VM as a Claude Code development environment.

## Quick start with Lima (recommended)

The fastest and recommended way to get a VM is `sand`, a small Go CLI/TUI with
the playbook embedded in it. Install it via Homebrew:

```bash
brew install lullabot/sandbar/sand
```

Then either drive it headlessly:

```bash
sand create --git-name "Your Name" --git-email you@example.com
```

or launch the interactive TUI:

```bash
sand
```

Either way, `sand` starts a [Lima](https://lima-vm.io/docs/installation/)
instance that installs Ansible and runs the embedded playbook against itself
with `--connection=local` — no manual cloning, inventory editing, or `ansible`
install required.

- **Just want a VM** — the Homebrew-installed binary works from anywhere; it
  materializes its embedded playbook to a private temp dir and mounts that.
- **Hacking on the playbook** — run `sand` from a checkout of this repository
  (e.g. via `go build ./cmd/sand`). It detects the working tree (by walking up
  to a directory containing `site.yml`) and mounts that instead of its embedded
  copy, so uncommitted edits provision the VM. See
  [README-sand.md](README-sand.md#playbook-resolution-working-tree-first-embedded-fallback)
  for the resolution order.

### Base image and clones

To avoid re-running the heavy install (packages, Docker, Node, Claude Code, …)
for every VM, the script provisions that identity-free work **once** into a
stopped base image (`claude-base` by default), then makes each VM a fast
[`limactl clone`](https://lima-vm.io/docs/) of it. Cloning copies the
provisioned disk (near-instant on a copy-on-write filesystem), so a new VM is
ready in seconds instead of minutes.

After cloning, a light **finalize** pass applies the per-VM bits: hostname, your
git identity, an `apt upgrade` (so a clone off an older base isn't carrying
stale packages), and the optional repo clone. The split is driven by the
`provision_phase` variable (`base` / `finalize` / `full`); heavy roles are
skipped on `finalize`, the `project` role is skipped on `base`.

- The base is built automatically the first time. Use `--rebuild` to recreate
  it after changing the playbook or to refresh installed packages — this rebuilds
  the base, then makes the VM.
- `--recreate` deletes and re-clones the **named** VM from the existing base (a
  fast reset of one VM, without rebuilding the base).
- With `sand create` (headless mode), `cpus` / `memory` / `disk` are set when
  the base is built and inherited by clones; pass them with `--rebuild` to
  change. (`disk` is baked into the base image, so growing it on a clone built
  this way needs `limactl disk resize`.)

After cloning, `sand` restarts the VM once so your first shell lands with
the right group membership (e.g. `docker`), the new hostname, and any kernel or
library updates the finalize `apt upgrade` installed.

**Per-VM disk sizing.** `sand` sizes each VM individually rather than
inheriting the base's size. It builds the base at a small virtual-disk floor
(`20GiB`) and grows every clone to its requested size (`cpus` and `memory` are
likewise applied per clone), so disk size is chosen per VM. A clone can grow
from the floor but never below it, so an effective "shrink" is simply a fresh
clone grown to a smaller number than the old VM. **One-time migration:** a base
built before this change keeps its old (larger) virtual size, and clones can't go
below it until the base is rebuilt — delete `claude-base` so the next TUI
create/reset rebuilds it at the floor.

> `Disk Used` reports allocated blocks (`st_blocks × 512`), so it can sit far
> below the maximum (qcow2 is sparse); on APFS it reflects shared-block
> accounting and may differ for blocks shared with a clone source.

Non-interactive use (CI, scripting) is supported via `sand create`'s flags —
see `sand create --help`. Every flag has a default, and `--git-name` /
`--git-email` fall back to your host's `git config`, so on a configured machine
`sand create` needs no flags. Pass them explicitly where the host has no git
identity (e.g. a bare CI runner) — otherwise `sand create` errors rather than
fabricate a commit author:

```bash
sand create --git-name "Your Name" --git-email you@example.com
```

How it spins up the VM:

- It inherits Lima's shipped **`template:_images/debian-13`**, so Lima manages
  the image, architecture, and download cache. Nothing image-specific is
  committed to this repo.
- The VM is fully **ephemeral**: the only mount is the playbook, read-only.
  There is **no writable host mount** (not even the stock Lima host-home
  mount), so the VM cannot modify anything on your machine and
  `limactl delete <name>` provably removes everything it produced — important
  when the whole point is to throw away potentially compromised code. Move
  files in or out with the TUI's **Upload**/**Download** actions on a VM's
  detail view: each transfer is a discrete, user-initiated `limactl copy` under
  the hood, so there is still **no writable host mount or standing share** and
  `limactl delete` provably removes everything.
- Your answers are passed to Ansible as `--extra-vars`, so there is no
  `group_vars/all.yml` to maintain per VM; each instance is independent.

Prerequisites: [Lima](https://lima-vm.io/docs/installation/) (`limactl`).

### Interactive TUI

`sand` run with no arguments (instead of `sand create`) opens a Bubble Tea
terminal UI that manages these VMs (list, create, start/stop/restart, and
delete/recreate) using the same base-image / clone / finalize flow as headless
`sand create`. See [README-sand.md](README-sand.md) for build, usage, and
keybindings.

**Reset a VM.** On a managed VM, the recreate action opens the create form
**pre-filled** with the VM's last-used settings, with `Name` locked. Edit any
field — for example a smaller `disk` — then optionally toggle **Preserve Claude
Code settings** and/or **Preserve project .env + checkout**, and submit. The VM
is deleted and re-cloned from the base with the edited settings, and those
settings are recorded so the next reset defaults to them.

### Running with lima-vm manually

If you prefer to drive Lima yourself instead of using the script:

```console
$ limactl create --name claude --cpus=8 --memory=32 template:debian-13
```

**It is highly recommended** to edit or disable the default mount of your home directory. Otherwise, nothing will stop Claude from making changes there.

## Other provisioning methods

These paths run the playbook against an existing host (no Lima). They require a
bit more setup than the quick start above.

### Prerequisites

- A fresh Debian 13 (trixie) minimal installation with SSH access as root
- Ansible installed on the control machine (`apt install ansible`)
- SSH key access to the target VM's root user

### Running the Playbook Directly on the Target Host

If you are running the playbook on the same machine you want to provision (i.e. no SSH hop), use Ansible's local connection mode. This is useful when bootstrapping the VM from within a post-install script or when SSH is not available.

1. Install Ansible on the target host:
   ```bash
   apt install ansible
   ```

2. Copy the example variables file and fill in your details:
   ```bash
   cp group_vars/all.yml.example group_vars/all.yml
   ```

3. Run the playbook with a local inventory and `--connection=local`:
   ```bash
   ansible-playbook -i localhost, --connection=local site.yml
   ```

   The trailing comma after `localhost` tells Ansible to treat the value as an inline inventory rather than a file path.

   When done, run `source ~/.bashrc` or create a new shell to get updated PATH settings.

### Running against a remote host

1. Copy the example variables file and fill in your details:
   ```bash
   cp group_vars/all.yml.example group_vars/all.yml
   ```
   Edit `group_vars/all.yml` with your Git identity and network settings. For
   SSH key access to the provisioned user, set `user_github_keys_url` (e.g.
   `https://github.com/your-username.keys`) — this is needed only on this
   non-Lima path; the Lima quick-start uses `limactl shell` instead.

2. Edit `inventory` and replace `CHANGE_ME` with the target VM's IP address:
   ```
   claude.example ansible_host=192.168.1.100 ansible_user=debian
   ```

3. Run the playbook:
   ```bash
   ansible-playbook -i inventory site.yml
   ```

## What It Does

- Sets hostname to `claude.lan` (configurable)
- Creates a user (default: the current host user) with passwordless sudo
- Installs development tools: Docker CE, ddev, Node.js, Go, Python 3, uv, mkcert, Java, and CLI utilities
- Installs the [GitHub CLI (`gh`)](https://cli.github.com/) and configures it as the git credential helper for HTTPS authentication
- Installs Claude Code CLI configured for autonomous operation, with remote
  control enabled at startup (`remoteControlAtStartup`) so you can drive and
  monitor sessions from the Claude app — this requires an interactive
  `claude` login (see below)
- Optionally configures a Docker registry proxy for caching pulls
- Deploys tmux, git, and bashrc configurations

## GitHub Authentication

The playbook installs the [GitHub CLI (`gh`)](https://cli.github.com/) and
configures it as the git credential helper, so `git push` / `git pull` over
HTTPS authenticate against whatever token is in the environment. The walkthrough
below threads a single fine-grained token through its whole life — from creating
it in GitHub, to supplying it at VM-create time, to rotating and revoking it.

1. **Create a fine-grained Personal Access Token.** Fine-grained PATs are
   recommended over classic PATs. They offer several advantages:

   - **Scoped to specific repositories** — a token can only access the repos you choose
   - **Granular permissions** — grant only the access each project needs
   - **Mandatory expiration dates** — tokens cannot be created without an expiry

   Create them at **Settings > Developer settings > Personal access tokens >
   Fine-grained tokens** with the recommended permissions (this is the set
   the TUI shows when prompting for a token):

   | Permission | Access | Purpose |
   |------------|--------|---------|
   | Contents | Read and write | Push and pull code |
   | Pull requests | **Read** | Read PRs without letting the agent self-merge to `main` without human review |
   | Issues | **Read** | Read issues without write access |
   | Actions | Read and write | Inspect and trigger CI |
   | Workflows | Read and write | Update workflow files |
   | Metadata | Read-only | Always required (automatically included) |

   Pull requests and Issues are deliberately **read-only** so an autonomous
   agent cannot merge its own PRs or close issues without a human in the loop.
   Bump them to write only if your workflow needs the agent to open/manage them
   directly.

2. **Supply it at VM-create time.** Provide the token through the TUI
   create-form `GitHub token` field, `sand create --clone-token`, or the
   `project_clone_token` playbook variable. It is used only to clone a private
   repo into the VM.

3. **Where it lands.** For `github.com` clone URLs the token is written into the
   per-org `.env` as `GH_TOKEN` (treat that file as a secret).

4. **How it loads.** direnv is installed and configured with `load_dotenv =
   true`, so the `GH_TOKEN=...` line is loaded when you `cd` into that directory
   and unloaded when you leave.

5. **Precedence.** `GH_TOKEN` takes precedence over any token stored by `gh auth
   login`, and because `gh` is the git credential helper, `git push` / `git
   pull` over HTTPS use whatever token is in the environment.

6. **Multiple organizations.** For multiple organizations or clients, use a
   **separate VM per org/context** rather than juggling several tokens on one
   machine. The VMs are disposable, and this keeps each context's credentials
   and code fully isolated — create a separate fine-grained token per
   organization or client for the best security posture.

7. **Rotate, expire, revoke.** Fine-grained PATs must have an expiry. When a
   token expires or you rotate it, update the `.env` `GH_TOKEN` line (or
   re-supply the new token on the next create), then revoke the old token in
   GitHub settings.

8. **Reset does not carry the token.** A reset/recreate does **not** carry the
   token — it is never stored in the managed-VM index — so a private-repo VM
   must have the token re-supplied on reset **unless** *Preserve project .env +
   checkout* is enabled, which keeps the existing `.env` (and its `GH_TOKEN`),
   so no re-supply is needed.

## Security Model

This playbook creates a **disposable, single-purpose development VM** intended to be run by Claude Code as an autonomous coding agent. The security posture reflects this:

- **Passwordless sudo** is enabled for the configured user (default: `claude`). The VM is not intended to host multiple users or untrusted workloads.
- **Claude Code runs with `--dangerously-skip-permissions`**, allowing it to operate without interactive approval prompts. This is appropriate because the VM is ephemeral and isolated — it can be torn down and reprovisioned at any time.
- **A random password** is generated for SSH and Samba access and is not stored persistently. With the Lima base-image flow it is generated once when the base is built, so clones of that base share it; this is immaterial in practice because Lima access is over `limactl shell` with an injected key, not the password. Direct (non-Lima) `full` provisioning still gets a fresh password per run.
- **The TUI reset preserve options are a deliberate, opt-in exception** to "nothing leaves the VM". When you enable them, the selected data — the Claude login under `~/.claude` plus `~/.claude.json`, and/or the per-org `.env` (which holds `GH_TOKEN`) together with the checkout — is copied to a private host temp dir, restored into the recreated VM, then deleted. They default off; **do not** use preserve if you suspect the VM is compromised.

**Do not use this playbook to provision machines that hold sensitive data or are exposed to the public internet.** It is designed for an isolated LAN or virtual network where the VM is treated as disposable.

## Configurable Variables

Copy `group_vars/all.yml.example` to `group_vars/all.yml` and edit, or override via `--extra-vars`:

| Variable | Default | Description |
|----------|---------|-------------|
| `user_name` | `claude` | Username for the primary system account |
| `base_hostname` | `claude` | VM hostname |
| `base_domain` | `lan` | Domain suffix (FQDN = hostname.domain) |
| `base_locale` | `en_CA.UTF-8` | System locale |
| `user_git_user_name` | `Your Name` | Git user.name |
| `user_git_user_email` | `you@example.com` | Git user.email (default) |
| `user_github_keys_url` | _(empty)_ | Optional SSH authorized_keys source (e.g. `https://github.com/<user>.keys`). Only needed for non-Lima / remote-host deployments; Lima uses `limactl shell` |
| `samba_enabled` | `true` | Run the Samba role. The Lima flow sets this to `false` (no host-home mount to share); set it for remote-host deployments that want file sharing |
| `devtools_docker_registry_proxy_enabled` | `false` | Enable Docker registry proxy |
| `devtools_docker_registry_proxy_host` | `docker-registry-proxy.example` | Docker registry proxy hostname |
| `devtools_docker_registry_proxy_port` | `3128` | Docker registry proxy port |
| `project_clone_url` | _(empty)_ | Optional HTTPS repo to clone on first provision, into `~/<host>/<org>/<repo>` |
| `project_clone_token` | _(empty)_ | Optional token for the clone. For `github.com` URLs it is written to the per-org `.env` as `GH_TOKEN` (loaded by direnv); treat as a secret |

### Authenticating Claude Code

The playbook does **not** provision a Claude Code credential. After provisioning,
shell into the VM and run `claude` once to complete the normal interactive login.

A full interactive login is required because remote control is enabled by
default (`remoteControlAtStartup`), and remote control sessions need a
full-scope OAuth login — the inference-only token from `claude setup-token`
cannot establish them, so headless token auth is intentionally not supported.

### Notifications

Notifications are delivered through Claude Code's remote control (enabled by
default), so you're alerted in the Claude app when a session needs input or
finishes — no webhook configuration required.

## Roles

- **base** — Hostname, locale, APT packages
- **user** — User creation, sudo, SSH, tmux, git, bashrc
- **samba** — Samba file sharing for the user's home directory (skipped by the Lima flow; `samba_enabled: false`)
- **dev-tools** — Docker, ddev, cloudflared, uv, mkcert, Docker registry proxy
- **claude-code** — Claude Code CLI installation and configuration
- **project** — Optional initial repo clone + per-org `.env`/direnv setup (only runs when `project_clone_url` is set)

## Releases and the Homebrew tap

`sand` ships as a prebuilt binary via Homebrew (`brew install
lullabot/sandbar/sand`), with this repository's playbook embedded at build
time — see [README-sand.md](README-sand.md#playbook-resolution-working-tree-first-embedded-fallback)
for how the embedded copy is resolved at runtime.

- **Cutting a release.** Releases are driven by
  [release-please](https://github.com/googleapis/release-please): it keeps a
  release PR open that accrues the changelog from Conventional Commit messages
  on `main`. Merging that PR creates the `vX.Y.Z` tag and GitHub Release, which
  in turn triggers the `release` workflow — [GoReleaser](https://goreleaser.com/)
  builds and uploads the `sand` binaries and pushes an updated formula to the
  tap. You never push a tag by hand; version bumps follow the commit types
  (`feat` → minor, `fix` → patch, with `bump-minor-pre-major` while pre-1.0).
- **Why a GitHub App token.** release-please authenticates with an org GitHub
  App (not the default `GITHUB_TOKEN`) precisely so the tag it creates triggers
  the `release` workflow — tags created with `GITHUB_TOKEN` do not trigger other
  workflows.
- **The tap.** Formulas live in a separate repository,
  `lullabot/homebrew-sandbar`, which must exist before the first release (it is
  not created by CI). GoReleaser's `brews:` config pushes the updated formula
  there on every release.
- **Human prerequisites** (one-time, before the first release):
  1. Create the `lullabot/homebrew-sandbar` repository.
  2. Create a dedicated org GitHub App (Contents: write) installed only on
     `lullabot/homebrew-sandbar`, and set its App ID as the `HOMEBREW_TAP_APP_ID`
     variable and its private key as the `HOMEBREW_TAP_APP_PRIVATE_KEY` secret in
     this repo's Actions settings. The `release` workflow mints a short-lived,
     tap-scoped token from them so GoReleaser can push the formula update
     (no long-lived PAT).
  3. Install the org release-please App on this repo (contents + pull-requests
     write) and set the `RELEASE_PLEASE_APP_ID` variable and
     `RELEASE_PLEASE_PRIVATE_KEY` secret used by `release-please.yml`.
