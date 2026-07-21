# Proxmox VE

`sand` can run VMs on a [Proxmox VE](https://www.proxmox.com/) host through its
REST API, alongside the local and remote-Lima backends. A Proxmox profile is
selected the same way any other is — it's just another
[Connection Profile](connection-profiles.md) — but it needs some one-time setup
on the Proxmox side, and that setup is what this page is about.

The design goal is a **least-privilege, pool-scoped** token: `sand` gets exactly
the permissions it needs to run its create/clone/delete workflow, confined to a
dedicated [resource pool](https://pve.proxmox.com/wiki/User_Management#pveum_pools),
so it **cannot see or touch any VM outside that pool** — not by accident, not by
a bug, not at all. That isolation is structural (Proxmox enforces it), not a
matter of `sand` behaving well.

!!! info "What you'll set up"
    1. A dedicated **pool** for `sand`'s VMs.
    2. A custom **role** holding the minimum privileges.
    3. A dedicated **user** and an **API token** that inherits the user's rights.
    4. **ACLs** binding the role to the **user** at the pool — plus three
       privileges that Proxmox does not allow to be pool-scoped, granted at the
       narrowest path that works.
    5. Optionally, a **second isolated pool** for automated tests.

Everything below is run on the Proxmox host (or any machine with `pveum` and
cluster access) as a full admin — usually `root@pam`.

## Prerequisites

- **Proxmox VE 9.0 or newer.** `sand` checks the version at preflight and
  refuses an older host. The minimum-privilege role below is expressed in PVE 9's
  privilege vocabulary — it relies on the `VM.GuestAgent.*` privileges introduced
  in PVE 9, and PVE 9 removed the old `VM.Monitor` privilege — so it cannot be
  created on an 8.x host.
- **A storage that supports `images` content** for VM disks (e.g. `local-lvm`, a
  ZFS pool, or a directory storage with *Disk image* enabled). The built-in
  `local` storage does **not** hold disk images by default, so `sand` cannot put
  a VM disk or a cloud-init drive there.
- **A file-based storage that supports `import` content** for the one-time
  cloud-image download — a **directory**, **NFS**, or **CIFS** storage. `sand`
  downloads the base image with PVE's `download-url` (content type `import`),
  which **block** storages (`zfspool`, `lvm-thin`, RBD) reject with *"not a file
  based storage"*. This is a **separate** storage from the disk storage above and
  defaults to `local`; set the profile's `image_storage` to override it. If your
  disk storage is a directory that already enables `import`, point `image_storage`
  at it and the same storage serves both.

    !!! tip "Enabling `import` on `local`"
        The default `local` is a directory storage but often ships without the
        `import` content type. Enable it once with
        `pvesm set local --content iso,vztmpl,backup,import` (keep whatever it
        already lists), or point `image_storage` at another file-based storage.
- **A Linux bridge** for VM networking (usually `vmbr0`).
- **Network reachability from the machine running `sand` to the VM subnet.**
  `sand` talks to guests over SSH once they boot (it discovers each VM's IP from
  the guest agent), so your workstation must be able to reach the addresses the
  VMs get on that bridge.
- **The `qemu-guest-agent`** is installed by `sand`'s provisioning into the base
  image, so you don't need to prepare an image yourself — `sand` builds its base
  template from a cloud image the first time you create a VM.

## Step 1 — Create a dedicated pool

Every VM `sand` creates is placed in this pool automatically, and the token is
scoped to it. That membership is the whole isolation boundary.

```bash
pveum pool add sandbar --comment "sandbar-managed VMs"
```

## Step 2 — Create the minimum-privilege role

This is the exact set of privileges `sand`'s workflow needs — create a base VM
from a cloud image, clone it, resize, configure cloud-init, power on and off,
snapshot, read node stats, and run a guest-agent command. Nothing more.

```bash
pveum role add SandbarProv --privs "\
VM.Allocate VM.Clone VM.Audit VM.PowerMgmt VM.Snapshot \
VM.Config.Disk VM.Config.CPU VM.Config.Memory VM.Config.Network \
VM.Config.Options VM.Config.Cloudinit VM.Config.HWType VM.Config.CDROM \
VM.GuestAgent.Audit VM.GuestAgent.Unrestricted \
Datastore.AllocateSpace Datastore.AllocateTemplate Datastore.Audit Pool.Audit"
```

Some of these are non-obvious, and if you trim them further you'll get confusing
failures — so, for the record, why each of the less-obvious ones is here:

| Privilege | Why `sand` needs it |
| --- | --- |
| `VM.Config.HWType` | Setting `scsihw`, `vga`, and `machine` on the base VM. Cloud images need `virtio-scsi-pci`, not the PVE default. |
| `VM.Config.Options` | Setting `agent`, `name`, and `ostype`. |
| `VM.Config.Disk` | Covers disk devices **and** the `boot` order. |
| `VM.Config.Cloudinit` | Injecting the SSH key, user, and network config. |
| `Datastore.AllocateTemplate` | Downloading the cloud image into storage via the `download-url` endpoint (content type `import`). PVE gates that endpoint on this privilege specifically — `Datastore.AllocateSpace` alone is not enough, and its absence fails the very first base-build step with a 403. |
| `Pool.Audit` | So the pool name appears in listings — without it `sand` can't tell which VMs are its own. |
| `VM.GuestAgent.Unrestricted` | Only needed for guest-agent `exec`. It's the broadest privilege in the set; drop it if you never need `sand` to run a command via the agent (it uses SSH for shells regardless). |

!!! warning "Do not add `VM.Monitor` or `VM.Console`"
    `VM.Monitor` was **removed in PVE 9** — including it makes `pveum role add`
    reject the whole command. `VM.Console` is only for the VNC/SPICE console,
    which `sand` never uses. Leaving both out is deliberate.

## Step 3 — Create a user and an API token

```bash
pveum user add sandbar@pve --comment "sandbar automation"
pveum user token add sandbar@pve prov --privsep 0 --output-format json
```

The second command prints the token **value exactly once**:

```json
{
  "full-tokenid": "sandbar@pve!prov",
  "value": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
  "info": { "privsep": "0" }
}
```

`--privsep 0` turns privilege separation **off**, so the token inherits its
user's permissions. That's deliberate: Proxmox computes a privilege-separated
(`--privsep 1`) token's rights as the **intersection** of the user's permissions
and the token's own. So if you grant the ACLs only to the token and its user has
none — exactly the case here, where `sandbar@pve` exists solely to own this
token — the token ends up with **no permissions at all**, and
`pveum user permissions 'sandbar@pve!prov'` prints `{}`. With separation off you
grant the least-privilege ACLs below to the **user** `sandbar@pve` instead, and
the token carries exactly those. Because this user has no password and no other
roles, its permissions *are* the confined set — there is nothing broader for the
token to inherit. **Save the value now**; it cannot be retrieved again.

`sand` authenticates with the token's **full identity**, which is the two fields
above joined by an `=`:

```
<full-tokenid>=<value>
sandbar@pve!prov=xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
```

Write exactly that one line into a file `sand` will read (referenced as
`token_file` in [Step 6](#step-6-point-sand-at-the-host)):

```bash
mkdir -p ~/.config/sandbar
umask 077
printf 'sandbar@pve!prov=%s\n' 'xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx' \
  > ~/.config/sandbar/pve1.token
chmod 600 ~/.config/sandbar/pve1.token
```

The file holds **one line** and nothing else — the identity, an `=`, and the
value. `sand` refuses to read it unless it is mode `600` (owner-only).

## Step 4 — Bind the role to the user at the pool

```bash
# Scope the user (and so its token) to the pool: this confines it to sandbar's own VMs.
pveum acl modify /pool/sandbar --roles SandbarProv --users sandbar@pve

# Add the VM storage to the pool so the role's Datastore privileges apply to it.
# This grants them on the WHOLE storage object (see the note below), not a
# pool-private slice — Proxmox has no notion of "the pool's part" of a storage.
pveum pool modify sandbar --storage local-lvm
```

!!! note "A shared storage is trusted as a whole"
    A pool can only contain VMIDs and *entire* storages — there is no "pool's
    slice" of a datastore. So if `local-lvm` also backs VMs outside the pool, the
    token's `Datastore.AllocateSpace` and `Datastore.Audit` reach the whole of it:
    it can **enumerate** every volume on that storage (volume names, sizes, and
    the owning VMID) and **allocate space** anywhere on it. It still **cannot read
    or modify** another VM's disk — and it never sees those VMs' configuration or
    state — because reading, attaching, or reassigning a volume owned by another
    guest requires access to that guest or the `Datastore.Allocate` privilege,
    neither of which this token has. If you want the storage boundary as tight as
    the VM one, give `sand` a **dedicated storage** (its own LVM-thin pool or
    dataset) rather than one shared with other VMs.

## Step 5 — Grant the three privileges that can't be pool-scoped

Proxmox implements pool permissions by *projecting* a role onto the pool's
members, and a pool can only contain **VMs and storage**. Three things `sand`
needs are therefore not grantable at the pool, and have to be granted at the
narrowest path that does work. None of them grants any access to another VM —
that's exactly why they're named individually here instead of reaching for a
broader role.

```bash
# 1. Attach a VM to the bridge. A plain Linux bridge lives under the synthetic
#    SDN zone "localnetwork"; with a VLAN tag the path gains the tag as a further
#    segment (…/vmbr0/<tag>).
pveum role add SandbarNet --privs "SDN.Use"
pveum acl modify /sdn/zones/localnetwork/vmbr0 --roles SandbarNet --users sandbar@pve

# 2. Download the cloud image to storage, and read node CPU/memory for the board
#    header. Both are node-scoped, not pool-scoped.
pveum role add SandbarNode --privs "Sys.AccessNetwork Sys.Audit"
pveum acl modify /nodes/pve1 --roles SandbarNode --users sandbar@pve

# 3. Storage privileges on BOTH storages sand uses:
#    - the DISK storage, so it can allocate VM disks (Datastore.AllocateSpace);
#    - the IMAGE storage, so it can download the cloud image with content=import
#      (Datastore.AllocateTemplate). These are the same command when a single
#      file-based storage serves both — run it once in that case.
pveum acl modify /storage/local-lvm --roles SandbarProv --users sandbar@pve
pveum acl modify /storage/local     --roles SandbarProv --users sandbar@pve
```

| Privilege | Path | What it allows |
| --- | --- | --- |
| `SDN.Use` | `/sdn/zones/localnetwork/vmbr0` | Attaching a VM to **that one bridge** — nothing else on the network |
| `Sys.AccessNetwork` | `/nodes/pve1` | Downloading the cloud image to storage |
| `Sys.Audit` | `/nodes/pve1` | Reading node CPU/memory/disk stats for the board header |

!!! note "You won't see `localnetwork` under SDN → Zones"
    `localnetwork` is a built-in *synthetic* zone that Proxmox uses only as the
    permission path for plain Linux bridges — it is **not** a configured SDN
    zone, so it never appears under **Datacenter → SDN → Zones**, and that's
    expected. To confirm the bridge grant landed, look under **Datacenter →
    Permissions** (or run the verification command below), not the SDN panel.

### Verify the scope

Confirm the token ended up with **only** the paths you intended — this is the
check that proves the isolation guarantee actually holds:

```bash
pveum user permissions 'sandbar@pve!prov' --output-format json
```

The result should list **only** `/pool/sandbar`, your disk **and** image
storages, the bridge, and the node `/nodes/pve1`. **If any permission appears at
`/`, the isolation guarantee does not hold** — go back and remove the over-broad
grant.

!!! warning "If this prints `{}`"
    An empty result means the token has **no** effective permissions — almost
    always because it was created with `--privsep 1` and the ACLs were granted to
    the **token** instead of the **user**. A privilege-separated token's rights
    are the *intersection* of the user's and the token's, so a token whose user
    holds no ACLs gets nothing. Recreate it with `--privsep 0` (Step 3) and grant
    the ACLs to `--users sandbar@pve` (Steps 4–5).

## Step 6 — Point `sand` at the host

!!! note "Proxmox profiles are added by editing `profiles.yaml`"
    The TUI's profile screen (press `p`) can create and edit `local` and
    `remote-ssh` profiles, but **not** `proxmox` ones yet — so add a Proxmox
    profile by hand-editing `profiles.yaml` as shown here. Once it's in the file
    and `enabled`, it appears in the TUI's profile list and board like any other,
    and `sand --profile <name>` targets it from the CLI; only the *creation* form
    is CLI/YAML-only for now.

Add a `proxmox` profile to your
[`profiles.yaml`](connection-profiles.md#profilesyaml):

```yaml
profiles:
  - id: pve1
    name: proxmox
    type: proxmox
    enabled: true
    host: pve1.example.com        # the API host; add :8006 only if non-default
    node: pve1                    # the PVE node name
    pool: sandbar                 # the dedicated pool from Step 1
    storage: local-lvm            # images-capable storage for VM disks
    # image_storage: local        # file-based (dir/NFS/CIFS) storage for the
    #                             # cloud-image download; defaults to "local"
    bridge: vmbr0                 # the Linux bridge
    token_file: ~/.config/sandbar/pve1.token
    # insecure: true              # only if the PVE cert is self-signed
    # ca_file: /etc/pve/pve-root-ca.pem   # or pin the CA instead
```

The profile fields:

| Field | Meaning |
| --- | --- |
| `host` | Hostname or IP the API answers on. A bare host uses port `8006`; append `:port` only if you've changed it. |
| `node` | The PVE **node name** (the identifier in `/nodes/<node>/…` paths) — often the same string as the host, but not always. |
| `pool` | The dedicated pool. Every VM `sand` creates lands here, and the token is scoped to it. |
| `storage` | The images-capable storage backing VM disks and the cloud-init drive. May be block (zfspool, lvm-thin) or file-based. |
| `image_storage` | Optional. The **file-based** storage (dir/NFS/CIFS) the cloud image is downloaded to with content `import` — block storages reject it. Defaults to `local`. The disk is then imported onto `storage` from here. |
| `bridge` | The Linux bridge `net0` attaches to. |
| `token_file` | Path to a file holding `user@realm!tokenid=value`. |
| `insecure` | Optional. Skip TLS verification (PVE ships a self-signed cert by default). |
| `ca_file` | Optional. Pin a CA certificate instead of disabling verification. |

!!! danger "The token file must be `chmod 600`"
    Like `identity_path` for a remote profile, `token_file` is a **path**, never
    the credential itself — `profiles.yaml` stays
    [secret-free](connection-profiles.md#profilesyaml) and safe to check into
    dotfiles. `sand` **refuses to read** a token file that is readable by group
    or other; a leaked API token is not a recoverable mistake. Create it with
    `umask 077` (as in Step 3) or run `chmod 600` on it.

That's it. `sand` builds its base template from a cloud image the first time you
create a VM (this takes a few minutes — it downloads the image, runs the same
Ansible provisioning the other backends use, and converts the result to a PVE
template), then clones each new VM from it. The board header shows the node's
real CPU, memory, and storage usage, sampled from the API.

## A separate pool for automated tests

If you run `sand`'s opt-in end-to-end test suite (or otherwise want a throwaway
pool that can never touch your day-to-day VMs), set up a **second** pool and
token exactly as above but with different names — `sandbar-test`, a
`SandbarTestProv` role assignment, and its own token. The two pools are fully
isolated from each other, so automated runs create and destroy VMs freely in
`sandbar-test` with no possibility of affecting the VMs in `sandbar`.

The test suite is documented in the repository's development guide; it's gated
behind a build tag and skips unless you configure it, and it reads its target
from these environment variables (which mirror the profile fields above):

```bash
export PROXMOX_E2E=1
export PROXMOX_E2E_HOST=pve1.example.com
export PROXMOX_E2E_NODE=pve1
export PROXMOX_E2E_POOL=sandbar-test          # the ISOLATED test pool
export PROXMOX_E2E_STORAGE=local-lvm
# export PROXMOX_E2E_IMAGE_STORAGE=local       # file-based; defaults to "local"
export PROXMOX_E2E_BRIDGE=vmbr0
export PROXMOX_E2E_TOKEN_FILE=~/.config/sandbar/pve-test.token
export PROXMOX_E2E_SSH_USER=debian            # the cloud-init guest login user
export PROXMOX_E2E_SSH_IDENTITY=~/.ssh/id_ed25519
export PROXMOX_E2E_IMAGE=https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2
# export PROXMOX_E2E_INSECURE=1               # if the PVE cert is self-signed

# Optional: a VMID OUTSIDE the test pool, to prove the token cannot touch it.
# The isolation test never creates or deletes this VM — you own it.
export PROXMOX_E2E_FOREIGN_VMID=100
```

Setting `PROXMOX_E2E_FOREIGN_VMID` enables the **pool-isolation test**, which
takes a VMID *outside* the test pool and asserts that the pool-scoped token is
refused (with a permission error) when it tries to read, stop, or delete it —
the live proof of the guarantee this whole page is built around. To confirm the
other half by hand — that the foreign VM is *unchanged* afterward — check its
status as an admin before and after; the test deliberately can't, because its
token can't see the VM at all, which is the point.
