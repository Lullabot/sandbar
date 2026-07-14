# About sand

`sand` is a small Go CLI and terminal UI that manages disposable [Claude
Code](https://www.anthropic.com/claude-code) development VMs on top of
[Lima](https://lima-vm.io). Run it with no arguments for an interactive board
of your VMs, or drive it headlessly (`sand create`, `sand shell`) from
scripts and CI.

Each VM is a fresh, isolated Debian environment with a specific, opinionated
stack baked in: Claude Code, common dev tools, and your git identity. You
get a disposable place to point an agent at a repository without touching
your host machine, and you throw the VM away — or recreate it — when you're
done.

## What it is not

`sand` is not a general-purpose VM manager. It doesn't manage arbitrary
guest OSes, arbitrary provisioning recipes, or long-lived infrastructure. It
manages one kind of thing — a Claude Code development VM — well, and leaves
everything else to Lima and `limactl` directly.

`sand` is the Go successor to what used to be a shell script plus a
standalone Ansible playbook.

## Where to go next

- [Installation](installation.md) — install `sand` and its one prerequisite.
- [Your First VM](first-vm.md) — the 30-second path to a running VM.
- [How Provisioning Works](how-it-works.md) — the base-image/clone/finalize
  model that makes each VM fast to create.
