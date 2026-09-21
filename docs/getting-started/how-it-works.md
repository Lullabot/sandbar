# How Provisioning Works

`sand` splits provisioning into two passes so the expensive work happens
once, not on every VM. This is the same everywhere a VM can run — on your
own machine, on a remote host, or on Proxmox.

```mermaid
graph TD
    A[sand create] --> B{Base image exists?}
    B -- no --> C[Build the base image: heavy install]
    C --> D[Stop the base image]
    B -- yes --> D
    D --> E[Clone the base → grow disk]
    E --> F[Finalize: selected agents, hostname, git identity, optional repo clone]
    F --> G[Ready: restart only if required]
```

## The base image

The first time you create a VM, `sand` runs a heavy install into a stopped
VM named `sandbar-base`. This installs development tools and shared
runtimes. Coding agents are excluded from the base. See [Available
Tools](available-tools.md) for the full toolchain.

Because the base image carries no identity or secrets, it's safe to keep
around and reuse indefinitely — `sand` rebuilds it automatically if the
underlying provisioning logic has changed since it was built, or you can
force a rebuild yourself with `sand create --rebuild` (or by deleting
`sandbar-base` and creating a new VM).

Each place you run VMs gets its own base image, built the first time you
create a VM there. A base image on your laptop and one on a server are
separate, and neither is copied to the other.

## Cloning

Every VM after the first is a copy of the stopped base image, grown to the
disk size you asked for. Copying a stopped VM is far cheaper than
reinstalling the whole stack, which is what makes each new VM fast.

## Finalize

A clone isn't ready to use yet — it's still an anonymous copy of the base
image. Finalize installs current releases of the selected agents, sets the
VM's hostname, writes your git identity, and optionally clones a project
repository. OS upgrades happen during base maintenance; the VM restarts
only when the guest reports a reboot is required.

Reset repeats the per-VM installs using that VM's recorded selections.
The optional [agent preservation checkbox](../using-sand/tui.md#resetting-a-vm)
keeps settings and files across reset; executable releases are installed fresh.
When upgrading from an older sand, base maintenance removes legacy agent
installs before cloning. Changing agent choices alone does not invalidate
the base's dependency stamp.

## Why the split

Doing the heavy install once and sharing it across every VM keeps VM
creation fast without compromising isolation: identity is applied per-VM at
finalize time, so the shared base image never contains anyone's secrets or
git configuration.

## How each backend does it

The shape above is identical everywhere; only the mechanics differ.

| | On your machine or a remote host | On Proxmox |
| --- | --- | --- |
| Runs VMs with | [Lima](https://lima-vm.io), via `limactl` | The Proxmox REST API |
| The base image is | a stopped Lima instance | a PVE template, built from a cloud image |
| A new VM is | `limactl clone` of it | a PVE clone of that template |
| `sand` reaches the guest over | `limactl shell`, or SSH for a remote host | SSH, to the address the guest agent reports |

For a remote host, `sand` runs the same `limactl` commands it would run
locally — it only changes *where* they run. See [Where VMs
Run](../using-sand/connection-profiles.md).
