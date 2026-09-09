# About sand

`sand` is a small Go CLI and terminal UI that manages disposable [Claude
Code](https://www.anthropic.com/claude-code) development VMs. Run it with no
arguments for an interactive board of your VMs, or drive it headlessly
(`sand create`, `sand shell`) from scripts and CI.

Each VM is a fresh, isolated Debian environment with a specific, opinionated
stack baked in: Claude Code, common dev tools, and your git identity. You
get a disposable place to point an agent at a repository without touching
your host machine, and you throw the VM away — or recreate it — when you're
done.

## Locally or remotely

A VM doesn't have to run on the machine in front of you. `sand` runs VMs in
three places, and the commands and keybindings are the same in all three:

| Where | What it uses | What you need |
| --- | --- | --- |
| Your own machine | [Lima](https://lima-vm.io) | Nothing beyond installing `sand` |
| Another machine, over SSH | Lima on that machine | Passwordless SSH and Lima on the far end |
| A [Proxmox VE](https://www.proxmox.com/) host | The Proxmox REST API | A pool-scoped API token ([setup](../using-sand/proxmox.md)) |

Each place you add is a named **Connection Profile**, and every enabled
profile is live at once — the board shows a laptop's VMs and a server's VMs
side by side. See [Where VMs Run](../using-sand/connection-profiles.md).

## What it is not

`sand` is not a general-purpose VM manager. It doesn't manage arbitrary
guest OSes, arbitrary provisioning recipes, or long-lived infrastructure. It
manages one kind of thing — a Claude Code development VM — well, and leaves
everything else to the tools underneath it.

`sand` is the Go successor to what used to be a shell script plus a
standalone Ansible playbook.

## Where to go next

- [Installation](installation.md) — install `sand` and its one prerequisite.
- [Your First VM](first-vm.md) — the 30-second path to a running VM.
- [How Provisioning Works](how-it-works.md) — the base-image/clone/finalize
  model that makes each VM fast to create.
- [Where VMs Run](../using-sand/connection-profiles.md) — putting VMs on
  another machine.
