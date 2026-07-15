# Available Tools

Every sand VM is provisioned from the same base image, so a freshly cloned
VM comes with the full toolchain below already installed and ready to work
with.

## Container & local dev

- [Docker CE](https://docs.docker.com/engine/) (with Buildx and Compose)
- [ddev](https://ddev.com/)
- [cloudflared](https://github.com/cloudflare/cloudflared)
- [mkcert](https://github.com/FiloSottile/mkcert)

## Language runtimes

- Node.js
- Go
- Python 3 (with [`uv`](https://github.com/astral-sh/uv))
- A headless JDK

## Claude Code & git

- The Claude Code CLI
- The [GitHub CLI (`gh`)](https://cli.github.com/), configured as the git
  credential helper for HTTPS authentication

## Shell & utilities

- `tmux`, `direnv`, `jq`, `htop`, and other common CLI tools
- Per-user tmux, git, and bashrc configuration, deployed automatically

!!! note "Sessions survive disconnecting"
    systemd linger is enabled for the VM's user, so a detached tmux session
    — and anything running inside it, including a Claude Code session —
    keeps running after you disconnect.
