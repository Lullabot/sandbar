# Connection Profiles

By default `sand` manages VMs with a local [Lima](https://lima-vm.io) — it runs
`limactl` on the machine you launched `sand` from. It can also drive `limactl`
on one or more **remote** hosts over SSH, so VMs live on a bigger box (a
workstation, a home server) while you work from a laptop.

Every location `sand` knows about — local or remote — is a **Connection
Profile**. Profiles are the only configuration surface for this: there is no
environment variable to set and no per-invocation flag needed to pick a
backend (older `sand` builds used `SAND_PROVIDER` / `SAND_REMOTE_*`
environment variables for this; those are **removed** — see
[Environment variables removed](#environment-variables-removed) below).

## What a profile is

A profile has:

- A **name** — a short, renameable label you pick (`work`, `home-server`,
  `local`). Names don't need to be unique, though the TUI won't stop you from
  picking a name that collides with another profile's — it's a display label,
  not an identifier.
- A **type** — `local` or `remote-ssh`.
- **Connection details** — for `remote-ssh` only: host, SSH user, port, an
  optional path to a private key file, and an optional remote `LIMA_HOME`.
- An **enabled** flag.

There is always exactly one **Local** profile — permanent, created
automatically the first time `sand` runs, and not deletable (though you can
rename it). Every other profile is `remote-ssh`, and you can create, edit,
enable, disable, or delete as many of those as you like.

## `profiles.yaml`

Profiles are persisted at:

```
${XDG_CONFIG_HOME:-~/.config}/sandbar/profiles.yaml
```

This file is **secret-free** — it holds a host/user/port/key-path, never a
password or key material — so it's safe to check into a dotfiles repo or copy
between machines: the same `profiles.yaml` on your laptop and your desktop
gives both clients the same set of named locations. It's also plain,
hand-editable YAML, shaped like this:

```yaml
version: 1
last_used: a1b2c3d4e5f6a7b8
profiles:
  - id: local
    name: local
    type: local
    enabled: true
  - id: a1b2c3d4e5f6a7b8
    name: work
    type: remote-ssh
    enabled: true
    host: 192.168.1.100
    user: debian
    port: 22
    identity_path: /home/me/.ssh/id_work
    lima_home: /home/debian/.lima
```

`id` is assigned once at creation and never changes, even across a rename —
it's how `sand` tracks "the last profile you used" and which managed VMs
belong to which profile without losing track when you rename one. `name` is
the label you see and address the profile by from the CLI (`--profile`) and
in the TUI.

Two enabled `remote-ssh` profiles may not point at the same
`user@host:port` — `sand` refuses to save that, so the same physical target
is never double-counted as two separate fleet members. There can also only
ever be one `local` profile.

## Managing profiles

### In the TUI

Press `p` from the board to open the profile management screen: a list of
every profile, its type, and its enabled/disabled state.

| Key | Action |
| --- | --- |
| `↑` `↓` | Move between profiles |
| `enter` | Edit the selected profile (or create a new one from the ghost row) |
| `t` | Toggle enabled/disabled |
| `d` | Delete (not offered for Local) |

Every change here is **live** — there is no restart, and no separate "apply"
step:

- **Enabling** a profile immediately builds its connection and starts
  listing its VMs; its tiles appear on the board as soon as that finishes.
- **Disabling** a profile tears its connection down and shows a banner in the
  header instead of its tiles, but leaves its managed-VM records and secrets
  exactly as they were — re-enable it later and everything comes back.
- **Deleting** a non-Local profile removes it from `profiles.yaml` and drops
  its tiles from the board. It **never touches the remote host** — no VM is
  stopped or deleted, no SSH command is run against it. Deleting a profile
  only forgets that `sand` should manage that connection; if you want to
  re-adopt the same host later, create a new profile pointing at it.
- **Renaming** a profile (a pure name edit, connection unchanged) doesn't
  rebuild anything — its VMs, jobs, and last-used pointer all follow the
  rename by ID.

Editing a profile's connection fields (host/user/port/key/Lima home), or
disabling/deleting it, is only offered while that profile is **idle** — no
build or other job currently running against it — the same guard that
protects a single VM from a destructive action mid-build, generalized to the
whole profile.

The VM **create form** also has a profile selector, so you choose which
enabled profile a new VM is created on right from the form, without leaving
the TUI.

### From the CLI

`sand create --profile <name>` picks which profile a headless create acts
on. `sand shell NAME --profile <name>` disambiguates when a VM named `NAME`
exists on more than one enabled profile. See the
[CLI Reference](cli-reference.md) for the full flag semantics and resolution
order.

## All enabled profiles are active at once

Unlike the old environment-variable selection, which chose exactly one
backend per process, **every enabled profile is live simultaneously** in the
TUI: the board shows tiles from your local Lima and every enabled remote
host side by side, each tile labeled with the profile it runs on (see
[The TUI](tui.md)). A disabled or errored profile shows a banner instead of
tiles, naming the profile and the reason.

Because VMs are tracked per-profile, the **same VM name can exist under two
different profiles** without conflict — `claude` on your `local` profile and
`claude` on your `work` profile are two independent VMs that happen to share
a display name. `sand shell claude` is ambiguous in that case and asks you to
disambiguate with `--profile`.

## Requirements on a remote host

- **Lima** (`limactl` on `PATH`) — the remote host runs the exact same
  `limactl` the local provider does; `sand` only changes *where* it runs.
- **A working hypervisor** (QEMU/KVM, etc.), just like any Linux host running
  Lima — see the [Lima installation docs](https://lima-vm.io/docs/installation/).
- **Passwordless SSH** to the target (key-based auth). `sand` runs `limactl`
  non-interactively over SSH, so the connection must not prompt.

## How a remote profile behaves

With a `remote-ssh` profile enabled, `sand create --profile`, the TUI, `sand
shell`, and file copy behave the same as locally — the base image is built on
that host once, each VM is a `limactl clone` of it, and finalize runs there
too:

- **Interactive shells** (`sand shell NAME` and `S` on a tile) wrap the guest
  tmux attach with `ssh -t` automatically; detaching still leaves the session
  running, exactly as with a local VM (see [Files and Shells](files-and-shells.md)).
- **File transfer** to and from a guest is staged through the remote host
  transparently — the remote `limactl copy` cannot see your local filesystem, so
  `sand` copies via the remote host and preserves where files land in the guest.
- **Isolation**: VMs created on a profile are tagged with that profile's
  connection in the [managed-VM index](../reference/files-and-state.md), so
  they never mix with VMs from another profile — a local `limactl list` and a
  remote profile's list never show each other's instances.

The managed-VM index records only a profile's `user@host:port`, never a
private key or password.

## Environment variables removed

Earlier (unreleased) builds of `sand` selected a single remote target for the
whole process via `SAND_PROVIDER`, `SAND_REMOTE_HOST`, `SAND_REMOTE_USER`,
`SAND_REMOTE_PORT`, `SAND_REMOTE_IDENTITY`, and `SAND_REMOTE_LIMA_HOME`
environment variables. That surface has been **removed entirely** and
replaced by Connection Profiles: set these variables and `sand` will simply
ignore them. If you were using them, create an equivalent `remote-ssh`
profile instead (in the TUI's `p` screen, or by hand-editing
`profiles.yaml`) — see [`profiles.yaml`](#profilesyaml) above for the field
mapping (`SAND_REMOTE_HOST` → `host`, `SAND_REMOTE_USER` → `user`,
`SAND_REMOTE_PORT` → `port`, `SAND_REMOTE_IDENTITY` → `identity_path`,
`SAND_REMOTE_LIMA_HOME` → `lima_home`).

## Future providers

The remote-Lima backend is one implementation of `sand`'s internal `Provider`
seam. That seam is what will let non-Lima backends (Proxmox, DigitalOcean,
Linode) be added later; none is available yet.
