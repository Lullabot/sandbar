# Installation

## Prerequisites

- A machine that can run [Lima](https://lima-vm.io): macOS or Linux, with a
  working hypervisor (macOS has one built in; on Linux, Lima needs
  QEMU/KVM — see the [Lima installation
  docs](https://lima-vm.io/docs/installation/) if `limactl` doesn't already
  work on your machine).
- Lima itself. You don't need to install it separately — Homebrew pulls it
  in as a dependency of the `sand` formula, below.

There is nothing else to install: no Ansible, no Go toolchain, and no clone
of this repository. `sand` embeds its provisioning logic in the binary and
runs it inside the guest VM.

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
brew uninstall sand   # does not delete your VMs; use `limactl delete <name>`
```
