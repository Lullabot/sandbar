# Claude Code Development VM Playbook

Ansible playbook to provision a Debian 13 (trixie) VM as a Claude Code development environment.

## Quick start with Lima (recommended)

The fastest and recommended way to get a VM is `scripts/new-vm.sh`. It prompts
for the required settings (with sensible autodetected defaults), then starts a
[Lima](https://lima-vm.io/docs/installation/) instance that installs Ansible and
runs this playbook against itself with `--connection=local` — no manual cloning,
inventory editing, or `ansible` install required.

It works two ways from the same script:

- **Just want a VM** — run it without checking the repo out:

  ```bash
  curl -fsSL https://raw.githubusercontent.com/deviantintegral/claude-code-ansible/main/install.sh | bash
  ```

  This clones the playbook into `~/.local/share/claude-code-ansible` (pinned
  to the latest release tag when one exists) and launches the VM from there.

  To pass flags on this path, put them **after `bash -s --`** — a pipe sends
  everything after `| bash` to bash, not to the script. For example, to rebuild
  an existing VM:

  ```bash
  curl -fsSL https://raw.githubusercontent.com/deviantintegral/claude-code-ansible/main/install.sh | bash -s -- --recreate
  ```

- **Hacking on the playbook** — from a checkout, run the script directly:

  ```bash
  ./scripts/new-vm.sh
  ```

  In this mode the script mounts your **working tree**, so uncommitted edits
  provision the VM. Provisioning runs **once** per VM (a marker at
  `/var/lib/claude-vm/provisioned` short-circuits Lima's per-boot re-runs, so
  restarts are fast). To re-apply the playbook after editing it, either re-run
  `ansible-playbook` inside the VM, or `sudo rm` the marker and restart.

  Lima bakes the instance config in at creation, so changing the VM's shape
  (cpus/memory/disk/mounts) or the generated provisioning means rebuilding the
  instance: `./scripts/new-vm.sh --recreate` (this destroys and recreates the
  VM). Re-running the script against an existing instance is refused otherwise.

After provisioning, the script restarts the VM once so your first shell lands
with the right group membership (e.g. `docker`) and any kernel/library updates
the provision installed.

Non-interactive use (CI, scripting) is supported via flags — see
`./scripts/new-vm.sh --help`. For example:

```bash
./scripts/new-vm.sh --yes --name claude \
  --git-name "Your Name" --git-email you@example.com
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
  files in or out with `limactl copy`.
- Your answers are passed to Ansible as `--extra-vars`, so there is no
  `group_vars/all.yml` to maintain per VM; each instance is independent.

Prerequisites: [Lima](https://lima-vm.io/docs/installation/) (`limactl`), and
`git` for the `curl | bash` path.

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
HTTPS authenticate against whatever token is in the environment.

Supply tokens per directory with `.env` files. direnv is installed and
configured with `load_dotenv = true`, so a `GH_TOKEN=...` line in a `.env`
file is loaded when you `cd` into that directory and unloaded when you leave.
`GH_TOKEN` takes precedence over any token stored by `gh auth login`.

For multiple organizations or clients, use a **separate VM per org/context**
rather than juggling several tokens on one machine. The VMs are disposable,
and this keeps each context's credentials and code fully isolated.

### Recommended: Fine-grained Personal Access Tokens

Fine-grained PATs are recommended over classic PATs. They offer several advantages:

- **Scoped to specific repositories** — a token can only access the repos you choose
- **Granular permissions** — grant only the access each project needs
- **Mandatory expiration dates** — tokens cannot be created without an expiry

Create them at: **Settings > Developer settings > Personal access tokens > Fine-grained tokens**.

Recommended permissions (this is the set `new-vm.sh` shows when prompting for a
token):

| Permission | Access | Purpose |
|------------|--------|---------|
| Contents | Read and write | Push and pull code |
| Pull requests | **Read** | Read PRs without letting the agent self-merge to `main` without human review |
| Issues | **Read** | Read issues without write access |
| Actions | Read and write | Inspect and trigger CI |
| Workflows | Read and write | Update workflow files |
| Metadata | Read-only | Always required (automatically included) |

Pull requests and Issues are deliberately **read-only** so an autonomous agent
cannot merge its own PRs or close issues without a human in the loop. Bump them
to write only if your workflow needs the agent to open/manage them directly.

For the best security posture, create a separate fine-grained token per organization or client.

## Security Model

This playbook creates a **disposable, single-purpose development VM** intended to be run by Claude Code as an autonomous coding agent. The security posture reflects this:

- **Passwordless sudo** is enabled for the configured user (default: `claude`). The VM is not intended to host multiple users or untrusted workloads.
- **Claude Code runs with `--dangerously-skip-permissions`**, allowing it to operate without interactive approval prompts. This is appropriate because the VM is ephemeral and isolated — it can be torn down and reprovisioned at any time.
- **A random password** is generated on each provision for SSH and Samba access. It is not stored persistently.

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
