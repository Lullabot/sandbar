# sandbar

`sand` is a single Go binary that provisions disposable Claude Code
development VMs on [Lima](https://lima-vm.io). Spin up an isolated, fully
provisioned VM in seconds, point Claude Code at a repository, and throw the
VM away when you're done.

**For full documentation, visit
[https://lullabot.github.io/sandbar/latest/](https://lullabot.github.io/sandbar/latest/)**

## Install

```bash
brew install lullabot/sandbar/sand
```

That's it — no Ansible, no Go toolchain, and no clone of this repository
required. Homebrew pulls in [Lima](https://lima-vm.io) as a dependency.

## Quick start

Open the interactive TUI board:

```bash
sand
```

Press `n` to create a VM, then `S` on its tile for a shell. Or drive it
headlessly:

```bash
sand create
sand shell claude
```

See [Getting Started](https://lullabot.github.io/sandbar/latest/getting-started/)
for the full walkthrough, or the
[CLI Reference](https://lullabot.github.io/sandbar/latest/using-sand/cli-reference/)
for every command and flag.

## Development

Building from a checkout, running tests, and how `sand` embeds and runs
its Ansible provisioner are covered in [AGENTS.md](AGENTS.md) and the
[Contributing](https://lullabot.github.io/sandbar/latest/contributing/development/)
docs.
