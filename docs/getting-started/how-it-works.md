# How Provisioning Works

`sand` splits provisioning into two passes so the expensive work happens
once, not on every VM.

```mermaid
graph LR
    A[sand create] --> B{claude-base exists?}
    B -- no --> C[Build base image: heavy identity-free install]
    C --> D[Stop base image]
    B -- yes --> D
    D --> E[limactl clone → grow disk]
    E --> F[Finalize: hostname, git identity, apt upgrade, optional repo clone]
    F --> G[Restart → ready]
```

## The base image

The first time you create a VM, `sand` runs a heavy, identity-free install
into a stopped Lima instance named `claude-base`. This installs the dev
tools, Claude Code, and everything else that every VM needs, none of which
depends on who you are. The base image is built at a 20 GiB virtual-disk
floor.

**What's installed:** Docker CE, ddev, cloudflared, the [GitHub CLI
(`gh`)](https://cli.github.com/) — configured as the git credential helper
for HTTPS authentication — Node.js, Go, Python 3, `uv`, `mkcert`, a headless
JDK, and the Claude Code CLI, plus common CLI utilities (`jq`, `htop`,
`tmux`, `direnv`, and more). tmux, git, and bashrc configuration are
deployed for the VM's user, and systemd linger is enabled so a detached
tmux session (and anything running inside it, including a Claude Code
session) keeps running after you disconnect.

Because the base image carries no identity or secrets, it's safe to keep
around and reuse indefinitely — `sand` rebuilds it automatically if the
underlying provisioning logic has changed since it was built, or you can
force a rebuild yourself with `sand create --rebuild` (or by deleting
`claude-base` and creating a new VM).

## Cloning

Every VM after the first is a `limactl clone` of the stopped base image,
grown to the disk size you asked for. Cloning a stopped VM is far cheaper
than reinstalling the whole stack, which is what makes each new VM fast.

## Finalize

A clone isn't ready to use yet — it's still an anonymous copy of the base
image. A light finalize pass sets the VM's hostname, writes your git
identity into it, runs `apt upgrade`, and optionally clones a project
repository into it. The VM restarts once at the end of finalize and is then
ready to use.

## Why the split

Doing the heavy install once and sharing it across every VM keeps VM
creation fast without compromising isolation: identity is applied per-VM at
finalize time, so the shared base image never contains anyone's secrets or
git configuration.
