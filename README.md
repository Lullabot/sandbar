# Claude Code Development VM Playbook

Ansible playbook to provision a Debian 13 (trixie) VM as a Claude Code development environment.

## Installing `sand`

`sand` is a small Go CLI/TUI with this playbook embedded in it. It ships as a
prebuilt binary from the [`lullabot/homebrew-sandbar`](https://github.com/Lullabot/homebrew-sandbar)
tap:

```bash
brew install lullabot/sandbar/sand
```

That taps the repository and installs the formula in one step; `brew tap
lullabot/sandbar && brew install sand` is the explicit equivalent. Homebrew
pulls in [Lima](https://lima-vm.io) as a dependency, so there is nothing else to
install — no Ansible, no Go toolchain, no clone of this repository.

Formulas (not casks) are published deliberately, so `brew install` works on
**macOS and Linux**, on both `amd64` and `arm64`. On Linux, Homebrew installs
Lima but not a hypervisor — Lima needs a working QEMU/KVM host, see the [Lima
installation docs](https://lima-vm.io/docs/installation/).

To upgrade or remove:

```bash
brew upgrade sand
brew uninstall sand   # does not delete your VMs; use `limactl delete <name>`
```

### From source

If you're hacking on the playbook or the Go code, build from a checkout instead
— a `sand` run from a working tree mounts *that* tree rather than its embedded
copy of the playbook, so uncommitted edits provision the VM:

```bash
go build ./cmd/sand && ./sand
```

You still need Lima on the host. See
[README-sand.md](README-sand.md#playbook-resolution-working-tree-first-embedded-fallback)
for the playbook resolution order.

## Quick start with Lima (recommended)

Once `sand` is installed, drive it headlessly. Every flag has a default, and
`--git-name`/`--git-email` fall back to the host's `git config`, so on a machine
with git configured this needs no flags at all:

```bash
sand create
sand create --git-name "Your Name" --git-email you@example.com   # or be explicit
```

or launch the interactive TUI:

```bash
sand
```

Either way, `sand` starts a [Lima](https://lima-vm.io/docs/installation/)
instance that installs Ansible and runs the embedded playbook against itself
with `--connection=local` — no manual cloning, inventory editing, or `ansible`
install required. The Homebrew-installed binary works from anywhere: it extracts
its embedded playbook to a private temp dir and mounts that. Run it from a
checkout of this repository instead and it mounts your working tree, so
uncommitted playbook edits provision the VM.

### Base image and clones

To avoid re-running the heavy install (packages, Docker, Node, Claude Code, …)
for every VM, the script provisions that identity-free work **once** into a
stopped base image (`claude-base` by default), then makes each VM a fast
[`limactl clone`](https://lima-vm.io/docs/) of it. Cloning copies the
provisioned disk (near-instant on a copy-on-write filesystem), so a new VM is
ready in seconds instead of minutes.

After cloning, a light **finalize** pass applies the per-VM bits: hostname,
your git identity, and the optional repo clone. The split is driven by the
`provision_phase` variable (`base` / `finalize` / `full`); heavy roles are
skipped on `finalize`, the `project` role is skipped on `base`. Package
installs — including group membership like `docker` — happen once, in the
**base** phase, so every clone already carries them before it ever boots;
finalize does not touch packages or groups.

- The base is built automatically the first time. After that, `sand` keeps it
  current on its own: editing the playbook and running `sand create` again
  converges the existing base **in place** (Ansible only applies what
  changed) instead of rebuilding it from scratch, and a base that has gone
  30+ days without a full package upgrade is refreshed the same way,
  automatically, the next time it's used. Either of these can make an
  occasional `sand create` noticeably slower than the rest — that's the base
  catching up, not something gone wrong.
- **The base-image tool-set.** `--with-claude` / `--with-ddev` / `--with-go` /
  `--with-java` choose which optional tools are installed into the shared base
  image every VM is cloned from. In the TUI, the same four choices are toggles
  on the create form. Claude Code is one of them rather than a fixture of the
  image: pass `--with-claude=false` to build a base without it and install your
  own agent instead.

  **The selection is remembered.** Each of these defaults to whatever the
  existing base image was actually *built* with — sand records the tool-set in
  the base's version stamp and reads it back — so you choose once, not on every
  VM. The create form's toggles open showing what the base contains, and a flag
  you don't pass adopts it. Only when there is no base yet do they all default
  **on**, so a first `sand create` installs everything. Passing a flag
  explicitly still wins, which is how you *add* a tool to an existing base
  (`--with-go` on a base built without it converges it in).
- **`--rebuild`** (headless flag) and the form's **"Rebuild base image"**
  toggle destroy the base and build it again from scratch, instead of
  converging it in place. You need this after **de-selecting** a tool
  (turning `--with-java` off, say): Ansible can converge an *addition* to the
  base, but it cannot *uninstall* a package whose task no longer applies —
  only a from-scratch rebuild actually removes it. It's also the manual
  escape hatch for a base left half-broken by an interrupted run.
- `--recreate` deletes and re-clones the **named** VM from the existing base (a
  fast reset of one VM, without rebuilding the base).
- With `sand create` (headless mode), `cpus` / `memory` / `disk` are set when
  the base is built and inherited by clones; pass them with `--rebuild` to
  change. (`disk` is baked into the base image, so growing it on a clone built
  this way needs `limactl disk resize`.)

After cloning, `sand` starts the VM and finalizes it; a restart happens only
when the guest itself reports a pending reboot (e.g. a kernel/libc update) —
most creates boot straight through with no bounce.

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
  board: each transfer is a discrete, user-initiated `limactl copy` under
  the hood, so there is still **no writable host mount or standing share** and
  `limactl delete` provably removes everything.
- Your answers are passed to Ansible as `--extra-vars`, so there is no
  `group_vars/all.yml` to maintain per VM; each instance is independent.

Prerequisites: [Lima](https://lima-vm.io/docs/installation/) (`limactl`).

### Interactive TUI

`sand` run with no arguments (instead of `sand create`) opens a Bubble Tea
terminal UI: a **board** of tiles, one per managed VM, offering create,
start/stop/restart, and delete/reset using the same base-image / clone /
finalize flow as headless `sand create`. Creating or resetting a VM no longer
takes over the screen — the build streams into a progress view you can leave
at any time while it keeps running in the background, so you can start
another VM (or act on a different one) while the first is still building. See
[README-sand.md](README-sand.md) for build, usage, and keybindings.

**Reset a VM.** On a managed VM, `R` opens the create form **pre-filled** with
the VM's last-used settings, with `Name` locked. Edit any field — for example a
smaller `disk` — then optionally toggle **Preserve Claude Code settings** and,
if the VM cloned a repo, **Preserve ~/&lt;host&gt;/&lt;org&gt;** (named for the
exact directory it protects, e.g. `Preserve ~/github.com/lullabot`). That second
toggle is hidden entirely when the VM cloned nothing, since there would be
nothing to preserve. Submit, and the VM is deleted and re-cloned from the base
with the edited settings; those settings are recorded so the next reset defaults
to them.

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
- Enables systemd linger for the user, so a detached tmux session (and the
  Claude Code session inside it) keeps running after you disconnect

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
   `project_clone_token` playbook variable. It is used to clone a private repo
   into the VM and is then stored for later use.

3. **Where it lands.** For `github.com` clone URLs the token is:
   - Written into the per-org `.env` as `GH_TOKEN` when the repo is cloned (treat that file as a secret)
   - Recorded in the host's `secrets.json` (unencrypted, see [Where Secrets Live](#where-secrets-live) below) as a `GH_TOKEN` pair in the VM's **global** scope
   - Applied to the guest's `~/.config/sandbar/secrets.env` every time the VM starts (so it is always available, even after a reset)

   The create form always seeds the token into the global scope, so it does
   **not** get the automatic git-credential wiring described in
   [Secrets Editor](#secrets-editor) — the per-org `.env` (written once, at
   clone time) is what makes `git`/`gh` authenticate under `~/<host>/<org>/`.
   If you want the same token to also authenticate other scopes, move it (or
   add a copy) into a `[github.com/acme]` section in the secrets editor.

4. **How it loads.** direnv is installed and configured with `load_dotenv =
   true`, so the `GH_TOKEN=...` line is loaded when you `cd` into that directory
   and unloaded when you leave. Additionally, `~/.config/sandbar/secrets.env` is
   sourced on every VM start (via `~/.profile` and `~/.bashrc`), so `GH_TOKEN`
   is available in all shells.

5. **Precedence.** `GH_TOKEN` takes precedence over any token stored by `gh auth
   login`, and because `gh` is the git credential helper, `git push` / `git
   pull` over HTTPS use whatever token is in the environment.

6. **Multiple organizations.** For multiple organizations or clients, use a
   **separate VM per org/context** rather than juggling several tokens on one
   machine. The VMs are disposable, and this keeps each context's credentials
   and code fully isolated — create a separate fine-grained token per
   organization or client for the best security posture.

7. **Rotate, expire, revoke.** Fine-grained PATs must have an expiry. When a
   token expires or you rotate it, update the secret via the secrets editor
   (press `e` on a VM's tile) or re-supply the new token on the next
   create, then revoke the old token in GitHub settings.

8. **Reset and the token.** The token lives in the host secrets store, never in
   the managed-VM index (which remains secret-free), so it survives a reset and
   is re-applied to the rebuilt VM's environment. But a reset **re-clones the
   repo during its finalize pass, which runs before the stored secrets are
   written into the guest** — so resetting a VM that cloned a *private* repo
   still requires re-supplying the clone token on the reset form, unless you
   enable the *Preserve ~/&lt;host&gt;/&lt;org&gt;* toggle, which skips the
   re-clone entirely. To remove a token, clear it in the secrets editor.

## Secrets Editor

`sand` stores an arbitrary set of `KEY=VALUE` pairs per VM, optionally grouped
into **scopes** — a scope is a safe home-relative directory path (e.g.
`github.com/acme`); the empty scope is **global**. Press `e` on a VM's detail
screen in the TUI to open the editor. It:

- Works whether the VM is running or stopped (secrets live on the host and are applied on the VM's next start)
- Renders the global scope first, headerless, then each directory scope under its own `[scope]` header, e.g.:

  ```
  EDITOR=vim

  [github.com/acme]
  GH_TOKEN=ghp_your_token_here
  ```

- Accepts one `KEY=VALUE` pair per line within a section; a line splits on its **first** `=`, so a value may contain `=`
- Ignores blank lines and lines starting with `#` (comments)
- Requires keys to match the shell variable name pattern `[A-Za-z_][A-Za-z0-9_]*` (letters, digits, underscore; not starting with a digit)
- Saves changes with `Ctrl+S`; press `Esc` to discard edits without saving. Saving a **running** VM's secrets applies them to the live guest immediately — no restart needed; a **stopped** VM gets them on its next start

**Convention, not a storage feature:** name a GitHub token `GH_TOKEN` and put it
under an org-scoped section like `[github.com/acme]`. `sand` recognizes
`GH_TOKEN` via a small delivery-layer table and, for any **non-empty** scope,
additionally wires `git`/`gh` credentials for that subtree (via a git
`includeIf "gitdir:~/<scope>/"` stanza) so authentication just works under
`~/<scope>/`. The secrets store itself has no idea what GitHub is — it only
ever holds `(scope, KEY, VALUE)` triples.

## Where Secrets Live

**Host side.** `sand` stores each VM's secrets in `${XDG_DATA_HOME:-~/.local/share}/sandbar/secrets.json`, **unencrypted**, at mode `0600` inside a `0700` directory. This is a deliberate trade: it is what lets you edit a VM's token or other secrets without booting the VM. It also means anyone who can read your user account's files (on this machine or via filesystem-level access) can read every sandbox's secrets. The managed-VM index (`managed-vms.json`) remains secret-free.

**Guest side.** When a VM starts, `sand` renders each scope into the guest:

- The **global** scope (`""`) renders into `~/.config/sandbar/secrets.env` (mode `0600`, parent dir `0700`), sourced from both `~/.profile` (login shells) and `~/.bashrc` (interactive non-login shells), so its variables are available in all shell types. If no global secrets are stored for a VM, the guest file is removed on start, so stale values do not linger.
- A directory **scope** `foo/bar` renders into `~/foo/bar/.env`, auto-loaded by direnv (`direnv allow` runs once per apply) when you `cd` into that directory, and unloaded when you leave.

## Security Model

This playbook creates a **disposable, single-purpose development VM** intended to be run by Claude Code as an autonomous coding agent. The security posture reflects this:

- **Passwordless sudo** is enabled for the configured user (default: `claude`). The VM is not intended to host multiple users or untrusted workloads.
- **Claude Code runs with `--dangerously-skip-permissions`**, allowing it to operate without interactive approval prompts. This is appropriate because the VM is ephemeral and isolated — it can be torn down and reprovisioned at any time.
- **A random password** is generated for SSH and Samba access and is not stored persistently. With the Lima base-image flow it is generated once when the base is built, so clones of that base share it; this doesn't matter in practice because Lima access is over `limactl shell` with an injected key, not the password. Direct (non-Lima) `full` provisioning still gets a fresh password per run.
- **The host secrets store** (`${XDG_DATA_HOME:-~/.local/share}/sandbar/secrets.json`, mode `0600`) is the single source of truth for every secret you give a VM, including GitHub tokens; it is rendered into the VM on each start, so it is a deliberate, host-side exception to "nothing leaves the VM" for that one file — treat it as sensitive. See [Secrets Editor](#secrets-editor).
- **The TUI reset preserve options are a separate, opt-in exception** to "nothing leaves the VM". When you enable them, the selected data — the Claude login under `~/.claude` plus `~/.claude.json`, and/or the per-org `.env` (which holds `GH_TOKEN`) together with the checkout — is copied to a private host temp dir, restored into the reset VM, then deleted. They default off; **do not** use preserve if you suspect the VM is compromised.

**Do not use this playbook to provision machines that hold sensitive data or are exposed to the public internet.** It is designed for an isolated LAN or virtual network where the VM is treated as disposable.

## TUI Keybindings

The interactive TUI's home surface is a **board**: a grid of tiles, one per
sand-managed VM, with a focus ring you move with the arrow keys. It is the
**only** roster — there is no table/list view to switch to. The board shows
managed clones only, always, with **no toggle** to widen it; base images and
unmanaged Lima instances are invisible here and are managed via `limactl`
directly. The header band reports the fleet count and a **live host readout** —
the CPU and memory the sandboxes are actually using (not what they were
allocated), and free disk. Per-VM keys below act on the **focused tile**
straight from the board — `enter` (open its own screen) is not required first.

The empty slot after the last tile is a real, selectable cell: arrow onto it and
press `enter` to create a VM (`n` still works from anywhere on the board).

The header's title row shows the build on the right — the release tag, or the git
revision (with `-dirty` for an uncommitted tree) for a build from source. The
footer wraps rather than truncating, so every verb that applies is visible, and
`?` opens a full reference.

**Board:**
| Key | Action |
|-----|--------|
| `↑` `↓` `←` `→` | Move the focus ring |
| `enter` | Create a VM, when the ring is on the empty slot (on a VM tile it does nothing) |
| `n` | Create a new VM |
| `/` | Search by VM name (type to filter, `esc` to clear/exit, `enter` to keep) |
| `X` | Stop every running **sand-managed** VM (unmanaged Lima instances and base images are never touched) |
| `?` | Show every key with a one-line description (scroll with `↑`/`↓`, `esc` closes) |
| `q` | Quit (confirms first if a build or transfer is in flight) |

**Per-VM verbs — pressed on the focused tile, straight from the board:**
| Key | Action |
|-----|--------|
| `s` | Start the VM |
| `x` | Stop the VM |
| `r` | Restart the VM |
| `R` | Reset the VM (re-clone from base; opens pre-filled form; managed VMs only) |
| `S` | Open an interactive shell in the VM |
| `d` | Delete the VM (opens confirmation) |
| `u` | Upload a file/directory from your host into the VM |
| `g` | Download a file/directory from the VM to your host |
| `e` | Edit the VM's secrets (works whether the VM is running or stopped; saving while it's running applies the change to the live guest immediately) |
| `l` | Reopen the VM's last build/transfer log |
| `esc` | Back to the board |

Creating or resetting a VM streams into a progress view, but leaving it
(`esc`/`enter`) no longer cancels the run — it keeps building in the
background and the tile shows live progress. See
[README-sand.md](README-sand.md#the-board) for the full key reference,
including which keys are offered only in certain VM states.

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
- **claude-code** — Claude Code CLI installation and configuration (only runs when `toolset_claude` is true, i.e. not `--with-claude=false`)
- **project** — Optional initial repo clone + per-org `.env`/direnv setup (only runs when `project_clone_url` is set)

## Releases and the Homebrew tap

`sand` ships as a prebuilt binary via Homebrew (`brew install
lullabot/sandbar/sand`), with this repository's playbook embedded at build
time — see [README-sand.md](README-sand.md#playbook-resolution-working-tree-first-embedded-fallback)
for how the embedded copy is resolved at runtime.

- **Cutting a release.** Releases are driven by
  [release-please](https://github.com/googleapis/release-please): it keeps a
  release PR open that collects the changelog from Conventional Commit messages
  on `main`. Merging that PR runs the `release-please` workflow, whose two jobs
  are the whole pipeline: release-please creates the `vX.Y.Z` tag and a *draft*
  GitHub Release, then [GoReleaser](https://goreleaser.com/) builds the `sand`
  binaries, uploads them into that draft, publishes it, and pushes an updated
  formula to the tap. You never push a tag by hand; version bumps follow the
  commit types (`feat` → minor, `fix` → patch, with `bump-minor-pre-major` while
  pre-1.0). If GoReleaser fails, re-run the failed job from the run page — the
  retry uploads into the same draft.
- **Why the release starts as a draft.** This repository has GitHub's
  [immutable releases](https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases)
  turned on, so a release is frozen the moment it is published and no asset can
  be added to it afterwards. Assets therefore have to go in while it is still a
  draft: release-please is configured with `draft` (leave it unpublished) plus
  `force-tag-creation` (create the tag anyway — GitHub does not create the tag
  for a draft release on its own, and GoReleaser builds from the tag), and
  GoReleaser is configured with `use_existing_draft` to adopt that draft instead
  of creating a release of its own. Publishing is GoReleaser's final step, once
  every archive is attached. A version whose release is published without
  binaries cannot be repaired — skip it and cut the next patch.
- **Why one workflow, not two.** GoReleaser is a `needs:` job in the same
  workflow rather than a separate tag-triggered one, so it cannot start before
  the draft release exists. A tag-triggered workflow would race release-please
  (the tag is created just before the release), and a GoReleaser run that finds
  no draft to adopt creates and publishes a release of its own — which under
  immutable releases burns the version number permanently.
- **Why a GitHub App token.** release-please authenticates with an org GitHub
  App (not the default `GITHUB_TOKEN`) so that the release PR it opens runs the
  test workflow — events made with `GITHUB_TOKEN` do not trigger other
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
     this repo's Actions settings. The GoReleaser job mints a short-lived,
     tap-scoped token from them so GoReleaser can push the formula update
     (no long-lived PAT).
  3. Install the org release-please App on this repo (contents + pull-requests
     write) and set the `RELEASE_PLEASE_APP_ID` variable and
     `RELEASE_PLEASE_PRIVATE_KEY` secret used by `release-please.yml`.
