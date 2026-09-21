# Available Tools

Every sand VM is provisioned from the same base image, so a freshly cloned
VM comes with the tools you normally use.

## Container & local dev

- [Docker CE](https://docs.docker.com/engine/) (with Buildx and Compose)
- [ddev](https://ddev.com/)
- [The Drupal.org CLI (`drupalorg`)](https://github.com/mglaman/drupalorg-cli)
- [cloudflared](https://github.com/cloudflare/cloudflared)
- [mkcert](https://github.com/FiloSottile/mkcert)

Running a web server with these? [Web Servers and Ports](../using-sand/web-servers.md)
covers how to reach it from your browser — locally and on a remote profile.

## Language runtimes

- Node.js
- Go
- Python 3 (with [`uv`](https://github.com/astral-sh/uv))
- A headless JDK

## Claude Code & git

- The Claude Code CLI
- The [GitHub CLI (`gh`)](https://cli.github.com/), configured as the git
  credential helper for HTTPS authentication
- The [GitLab CLI (`glab`)](https://gitlab.com/gitlab-org/cli)
- The [OpenAI Codex CLI](https://chatgpt.com/codex) — **opt-in**: pass
  `--with-codex` to `sand create` (or enable the toggle in the TUI create
  form). Codex is not provisioned by default; only include it if you want to
  use it alongside Claude Code.

The `--with-*` flags configure the **shared base image**. Changing a tool
selection makes the next `sand create` update the base before cloning a VM.
See the [CLI reference](../using-sand/cli-reference.md#sand-create).

## Browser reviews

Every base image includes [`@self-review/serve`](https://www.npmjs.com/package/@self-review/serve).
Run `sand land NAME PATH --review` to review a checkout's diff in your browser
and save comments where the agent can read them. The server runs inside the
VM and starts only when you open a review. See
[Reviewing changes in a browser](../using-sand/review.md).

## Shell & utilities

- `tmux`, `direnv`, `jq`, `htop`, and other common CLI tools
- Per-user tmux, git, and bashrc configuration, deployed automatically

!!! note "Sessions survive disconnecting"
    systemd linger is enabled for the VM's user, so a detached tmux session
    — and anything running inside it, including a Claude Code session —
    keeps running after you disconnect.
