# Installation

## Prerequisites

`sand` itself runs on macOS or Linux. What else you need depends on where
you want the VMs to run.

**To run VMs on this machine** (the default), you need
[Lima](https://lima-vm.io) and a working hypervisor. macOS has a hypervisor
built in; on Linux, Lima needs QEMU/KVM — see the [Lima installation
docs](https://lima-vm.io/docs/installation/) if `limactl` doesn't already
work on your machine. You don't need to install Lima separately: Homebrew
pulls it in as a dependency of the `sand` formula below.

**To run VMs somewhere else**, this machine needs nothing but `sand`:

- On **another machine over SSH**, Lima and the hypervisor live on that
  machine instead, and you need passwordless SSH to it.
- On a **Proxmox VE host**, no Lima is involved at all — `sand` talks to the
  Proxmox API over HTTPS. The host needs a one-time
  [token and pool setup](../using-sand/proxmox.md).

Either way, add the machine as a [Connection
Profile](../using-sand/connection-profiles.md) once and `sand` uses it from
then on.

## Install `sand`

`sand` ships as a prebuilt Homebrew **formula** (not a cask — deliberately,
so the same install works on both macOS and Linux) from the
[`lullabot/homebrew-sandbar`](https://github.com/Lullabot/homebrew-sandbar)
tap:

```bash
brew install lullabot/sandbar/sand
```

That taps the repository and installs the formula in one step.

## Verify it worked

```bash
sand version
```

## Upgrading and removing

```bash
brew upgrade sand
brew uninstall sand   # does not delete your VMs
```

Uninstalling leaves every VM where it is. Delete the ones you don't want
first — press `d` on a tile in the board, or, for a local VM, run
`limactl delete <name>`.
