# Available Tools

Every sandbar VM clones a shared base containing development tools and runtimes.
The selected coding agents are then installed at their current releases
inside that individual VM, including on reset.

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

## Coding agents

The create form offers four independent checkboxes: **Install Claude Code**,
**Install OpenAI Codex**, **Install OpenCode**, and **Install Pi**. Select any combination, including none.
All four start unselected; after creating a VM, sandbar remembers your choices
across connection profiles. See [Files and State](../reference/files-and-state.md#coding-agent-preferences-and-migration)
for persistence and migration details.

Headless creation offers `--with-claude`, `--with-codex`, `--with-opencode`,
and `--with-pi`; use `=false` to explicitly deselect an agent. Omitted agent
flags use the remembered selection. Changing agents does not rebuild the
shared base. Reset retains that VM's recorded selections, independently of
the defaults for new VMs.

### Claude Code terminal notifications

When creating or resetting a VM with Claude Code selected, sandbar detects the
workstation terminal and sets Claude's native notification channel for iTerm2,
Kitty, or Ghostty. Detection also checks terminal identifiers retained inside
host tmux and its saved `TERM_PROGRAM`. If sandbar cannot identify a supported
terminal, it leaves your existing notification preference unchanged.

The guest tmux configuration enables notification passthrough. If you also run
sandbar inside **host tmux**, enable `set -g allow-passthrough on` in the host's
`~/.tmux.conf` and reload it with `tmux source-file ~/.tmux.conf`.
iTerm2 additionally needs **Notification Center Alerts** and **Send escape
sequence-generated alerts** enabled under its profile's Terminal settings.
See [Claude's terminal notification guide](https://code.claude.com/docs/en/terminal-config#get-a-terminal-bell-or-notification).

The channel reflects the terminal used during provisioning. If you later
connect from a different terminal, change `preferredNotifChannel` in the guest's
`~/.claude/settings.json` to match it.

## Git

- The [GitHub CLI (`gh`)](https://cli.github.com/), configured as the git
  credential helper for HTTPS authentication
- The [GitLab CLI (`glab`)](https://gitlab.com/gitlab-org/cli)

The `--with-*` flags configure the **shared base image**. Changing a tool
selection makes the next `sandbar create` update the base before cloning a VM.
See the [CLI reference](../using-sand/cli-reference.md#sandbar-create).

## Browser reviews

Every base image includes [`@self-review/serve`](https://www.npmjs.com/package/@self-review/serve).
Run `sandbar land NAME PATH --review` to review a checkout's diff in your browser
and save comments where the agent can read them. The server runs inside the
VM and starts only when you open a review. See
[Reviewing changes in a browser](../using-sand/review.md).

An initial repository cloned during provisioning receives the matching agent
skills automatically. For repositories cloned later, run
`self-review-install-skills /path/to/repository` inside the VM.

## Shell & utilities

- `tmux`, `direnv`, `jq`, `htop`, and other common CLI tools
- Per-user tmux, git, and bashrc configuration, deployed automatically

!!! note "Sessions survive disconnecting"
    systemd linger is enabled for the VM's user, so a detached tmux session
    — and anything running inside it, including a Claude Code session —
    keeps running after you disconnect.
