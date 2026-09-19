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
- A **browser review UI** ([`@self-review/serve`](https://www.npmjs.com/package/@self-review/serve))
  — installed by default. `sand land NAME PATH --review` opens a
  browser-based review of that checkout's diff, served from inside the VM,
  and writes your comments back into the checkout where the agent can read
  them. Opt out with `--with-review=false`. See
  [Reviewing changes in a browser](../using-sand/review.md).

Like every `--with-*` flag, these configure the **shared base image**:
toggling one from what the base was last built with invalidates it, so the
next `sand create` reprovisions the base before cloning — see
[`--with-*` flags](../using-sand/cli-reference.md#sand-create) in the CLI
reference.

## Shell & utilities

- `tmux`, `direnv`, `jq`, `htop`, and other common CLI tools
- Per-user tmux, git, and bashrc configuration, deployed automatically

!!! note "Sessions survive disconnecting"
    systemd linger is enabled for the VM's user, so a detached tmux session
    — and anything running inside it, including a Claude Code session —
    keeps running after you disconnect.
