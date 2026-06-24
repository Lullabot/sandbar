# Claude Code Development VM Playbook

Ansible playbook to provision a Debian 13 (trixie) VM as a Claude Code development environment.

## Prerequisites

- A fresh Debian 13 (trixie) minimal installation with SSH access as root
- Ansible installed on the control machine (`apt install ansible`)
- SSH key access to the target VM's root user

## Running the Playbook Directly on the Target Host

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

## Running against a remote host

1. Copy the example variables file and fill in your details:
   ```bash
   cp group_vars/all.yml.example group_vars/all.yml
   ```
   Edit `group_vars/all.yml` with your Git identity, GitHub username, and network settings.

2. Edit `inventory` and replace `CHANGE_ME` with the target VM's IP address:
   ```
   claude.example ansible_host=192.168.1.100 ansible_user=debian
   ```

3. Run the playbook:
   ```bash
   ansible-playbook -i inventory site.yml
   ```
   
## Quick start with Lima

The fastest way to get a VM is `scripts/new-vm.sh`. It prompts for the
required settings (with sensible autodetected defaults), then starts a Lima
instance that installs Ansible and runs this playbook against itself with
`--connection=local` — no manual cloning, inventory editing, or `ansible`
install required.

It works two ways from the same script:

- **Just want a VM** — run it without checking the repo out:

  ```bash
  curl -fsSL https://raw.githubusercontent.com/deviantintegral/claude-code-ansible/main/install.sh | bash
  ```

  This clones the playbook into `~/.local/share/claude-code-ansible` (pinned
  to the latest release tag when one exists) and launches the VM from there.

- **Hacking on the playbook** — from a checkout, run the script directly:

  ```bash
  ./scripts/new-vm.sh
  ```

  In this mode the script mounts your **working tree**, so uncommitted edits
  provision the VM. Re-run it (or `limactl start <name>`) to re-apply the
  playbook; Ansible is idempotent, so it just re-converges.

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

Commonly needed permissions:

| Permission | Access | Purpose |
|------------|--------|---------|
| Contents | Read and write | Push and pull code |
| Pull requests | Read and write | Create and manage PRs |
| Issues | Read and write | Create and manage issues (if needed) |
| Metadata | Read-only | Always required (automatically included) |

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
| `devtools_docker_registry_proxy_enabled` | `false` | Enable Docker registry proxy |
| `devtools_docker_registry_proxy_host` | `docker-registry-proxy.example` | Docker registry proxy hostname |
| `devtools_docker_registry_proxy_port` | `3128` | Docker registry proxy port |
| `claude_code_notifications_enabled` | `false` | Install Claude Code Stop / UserPromptSubmit / SessionEnd hooks that POST to a webhook |
| `claude_code_notifications_webhook_url` | _(empty)_ | Webhook URL the hooks POST to. Treat as a secret — supply via `--extra-vars` or an Ansible vault, not source control |

### Authenticating Claude Code

The playbook does **not** provision a Claude Code credential. After provisioning,
shell into the VM and run `claude` once to complete the normal interactive login.

A full interactive login is required because remote control is enabled by
default (`remoteControlAtStartup`), and remote control sessions need a
full-scope OAuth login — the inference-only token from `claude setup-token`
cannot establish them, so headless token auth is intentionally not supported.

### Webhook notifications (optional)

When `claude_code_notifications_enabled` is true, the playbook installs three hooks under `~/.claude/hooks/`:

- `notify-stop.sh` — fires on Claude Code's `Stop` event and POSTs a notification with the host, project, and last assistant message.
- `notify-clear.sh` — fires on `UserPromptSubmit` and `SessionEnd` and POSTs a `clear_notification` with the same `tag` so the prior notification is dismissed (Home Assistant Companion convention).

Each notification is tagged `claude-<host>-<session8>` so multiple concurrent sessions don't clobber each other. The webhook URL is read at runtime from `~/.claude/hooks/notify.env` (rendered from `claude_code_notifications_webhook_url`, mode `0600`); if the env file is missing or empty, the hooks exit silently.

To enable for a single run without committing the secret:

```bash
ansible-playbook -i inventory site.yml \
  --extra-vars "claude_code_notifications_enabled=true claude_code_notifications_webhook_url=https://example.ui.nabu.casa/api/webhook/your-webhook-id"
```

## Roles

- **base** — Hostname, locale, APT packages
- **user** — User creation, sudo, SSH, tmux, git, bashrc
- **samba** — Samba file sharing for the user's home directory
- **dev-tools** — Docker, ddev, cloudflared, uv, mkcert, Docker registry proxy
- **claude-code** — Claude Code CLI installation and configuration
