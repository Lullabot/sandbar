# Install sandbar

Install sandbar on a macOS or Linux workstation, check that it works, and upgrade or remove it later.

## Before you start

- A Mac or a Linux machine, on an Intel/AMD (amd64) or ARM (arm64) processor. Sandbar has no Windows build.
- To run VMs on this machine, which is the default: [Lima](https://lima-vm.io) and a hypervisor. macOS has a hypervisor built in. On Linux, Lima uses QEMU with KVM, so your user must be able to open `/dev/kvm`. The Homebrew install below adds Lima for you.
- To run VMs only on another machine or a Proxmox VE host, this machine needs nothing but sandbar. See [Where VMs run](../advanced/connection-profiles.md) after you install.
- Optional: the [GitHub CLI](https://cli.github.com/) (`gh`), signed in on this machine, if you want sandbar to open pull requests for you from the *Landing* pane.

## Install with Homebrew

Install sandbar with [Homebrew](https://brew.sh), on macOS or Linux. It installs Lima for you, and upgrades sandbar with one command.

```bash
brew install lullabot/sandbar/sandbar
```

This adds the `lullabot/sandbar` tap and installs the `sandbar` formula and Lima.

## Check that it works

```bash
sandbar version
```

This prints the version you installed, for example `0.19.1`.

To run VMs on this machine, also check Lima:

```bash
limactl --version
```

Then run `sandbar` to open the board. If Lima is missing or too old, sandbar says so on the board; see [Troubleshooting](../reference/troubleshooting.md#lima-is-too-old-to-support-clone).

## Upgrade

```bash
brew upgrade lullabot/sandbar/sandbar
```

The board's header shows *Update available!* next to the version when a newer release is out. Upgrading doesn't change your VMs, profiles or secrets. A VM built with an older release may show an update notice on its tile; you apply it by resetting that VM (`R` on its tile, see [Resetting a VM](../reference/board.md#resetting-a-vm)).

### If you used the `sand` command

Sandbar was named `sand`. Releases still include a `sand` link that runs `sandbar` and prints a warning. It will be removed in a future release, so change your scripts to call `sandbar`. Your VMs, profiles, secrets and other state carry over.

If you installed the old `sand` Homebrew formula, switch it to `sandbar`. Homebrew 6 won't apply the rename until you trust the new formula, so `brew upgrade` alone stays on `sand`. Run this once:

```bash
brew update
brew trust --formula lullabot/sandbar/sandbar
brew migrate sand
```

## Uninstall

Uninstalling sandbar doesn't delete your VMs. Delete the ones you don't need first: press `d` on each tile on the board.

Then remove sandbar:

```bash
brew uninstall sandbar
```

Some files stay behind:

- The base image VM, `sandbar-base`, and any templates. Delete them with `limactl delete sandbar-base` and `sandbar template delete NAME` (run that one before you uninstall).
- The downloaded base image, in `~/.lima/_sand/images`.
- Sandbar's configuration, VM records and secrets. See [Files and state](../reference/files-and-state.md#host-paths) for every path.

## If something goes wrong

See [Troubleshooting](../reference/troubleshooting.md).

## Related

- [Quickstart: a Drupal site with DDEV](quickstart.md)
- [Where VMs run](../advanced/connection-profiles.md)
- [Release sandbar](../contributing/releases.md): how releases and the Homebrew tap are built
