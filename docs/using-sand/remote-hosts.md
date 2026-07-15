# Remote Lima hosts over SSH

By default `sand` manages VMs with a local [Lima](https://lima-vm.io) — it runs
`limactl` on the machine you launched `sand` from. It can instead drive `limactl`
on a **remote** host over SSH, so the VMs live on a bigger box (a workstation, a
home server) while you work from a laptop. The local workflow is unchanged; the
remote target is opt-in.

## Selecting a remote host

Set the selection environment variables before running `sand` (headless or the
TUI). Only `SAND_PROVIDER` and `SAND_REMOTE_HOST` are required; the rest have
sensible defaults:

```bash
export SAND_PROVIDER="lima-remote"
export SAND_REMOTE_HOST="192.168.1.100"          # hostname or IP
export SAND_REMOTE_USER="debian"                 # optional; the SSH login user
export SAND_REMOTE_PORT="22"                      # optional; defaults to 22
export SAND_REMOTE_IDENTITY="/path/to/ssh/key"    # optional; else SSH agent/config
export SAND_REMOTE_LIMA_HOME="/home/debian/.lima" # optional; the remote LIMA_HOME

sand create      # or 'sand' for the TUI
```

To return to local Lima, unset them:

```bash
unset SAND_PROVIDER SAND_REMOTE_HOST SAND_REMOTE_USER \
      SAND_REMOTE_PORT SAND_REMOTE_IDENTITY SAND_REMOTE_LIMA_HOME
```

## Requirements on the remote host

- **Lima** (`limactl` on `PATH`) — the remote host runs the exact same `limactl`
  the local provider does; `sand` only changes *where* it runs.
- **A working hypervisor** (QEMU/KVM, etc.), just like any Linux host running
  Lima — see the [Lima installation docs](https://lima-vm.io/docs/installation/).
- **Passwordless SSH** to the target (key-based auth). `sand` runs `limactl`
  non-interactively over SSH, so the connection must not prompt.

## How it behaves

With a remote target configured, `sand create`, the TUI, `sand shell`, and file
copy behave the same as locally — the base image is built on the remote host
once, each VM is a `limactl clone` of it, and finalize runs there too:

- **Interactive shells** (`sand shell NAME` and `S` on a tile) wrap the guest
  tmux attach with `ssh -t` automatically; detaching still leaves the session
  running, exactly as with a local VM (see [Shells and Files](files-and-shells.md)).
- **File transfer** to and from a guest is staged through the remote host
  transparently — the remote `limactl copy` cannot see your local filesystem, so
  `sand` copies via the remote host and preserves where files land in the guest.
- **Isolation**: VMs created on a remote host are tagged with that target in the
  [managed-VM index](../reference/files-and-state.md), so they never mix with
  your local VMs — a local `limactl list` and the remote list never show each
  other's instances.

The managed-VM index records only the target's `user@host:port`, never a private
key or password.

## Future providers

The remote-Lima backend is one implementation of `sand`'s internal `Provider`
seam. That seam is what will let non-Lima backends (Proxmox, DigitalOcean,
Linode) be added later; none is available yet.
