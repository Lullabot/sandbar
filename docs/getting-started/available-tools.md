# Available Tools

Every sand VM is provisioned from the same base image, so a freshly cloned
VM comes with the tools you normally use.

Four of these tools — Claude Code, ddev, Go, and a headless JDK — are
optional, and are each implemented as a **shipped provisioning profile**: the
same declarative `.sandbar/profile.yml` format a repo can check in for its
own needs (see [Provisioning Profiles](../using-sand/provisioning-profiles.md)),
bundled with `sand` itself. Toggle them with the `--with-claude`,
`--with-ddev`, `--with-go`, and `--with-java` flags on `sand create` (all
default on), or their matching toggles on the TUI's create form — the flags
and toggles are unchanged shorthands for enabling or disabling the
corresponding shipped profile in the shared base image. See the
[CLI Reference](../using-sand/cli-reference.md#-with-flags-are-provisioning-profile-shorthands)
for the exact flag semantics.

## Container & local dev

- [Docker CE](https://docs.docker.com/engine/) (with Buildx and Compose)
- [ddev](https://ddev.com/) — optional, `--with-ddev`
- [The Drupal.org CLI (`drupalorg`)](https://github.com/mglaman/drupalorg-cli)
- [cloudflared](https://github.com/cloudflare/cloudflared)
- [mkcert](https://github.com/FiloSottile/mkcert)

Running a web server with these? [Web Servers and Ports](../using-sand/web-servers.md)
covers how to reach it from your browser — locally and on a remote profile.

## Language runtimes

- Node.js
- Go — optional, `--with-go`
- Python 3 (with [`uv`](https://github.com/astral-sh/uv))
- A headless JDK — optional, `--with-java`

## Claude Code & git

- The Claude Code CLI — optional, `--with-claude`
- The [GitHub CLI (`gh`)](https://cli.github.com/), configured as the git
  credential helper for HTTPS authentication
- The [GitLab CLI (`glab`)](https://gitlab.com/gitlab-org/cli)
- The [OpenAI Codex CLI](https://chatgpt.com/codex) — **opt-in**: pass
  `--with-codex` to `sand create` (or enable the toggle in the TUI create
  form). Codex is not provisioned by default; only include it if you want to
  use it alongside Claude Code.

## Shell & utilities

- `tmux`, `direnv`, `jq`, `htop`, and other common CLI tools
- Per-user tmux, git, and bashrc configuration, deployed automatically

!!! note "Sessions survive disconnecting"
    systemd linger is enabled for the VM's user, so a detached tmux session
    — and anything running inside it, including a Claude Code session —
    keeps running after you disconnect.
