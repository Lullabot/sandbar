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
    3. A dedicated **user** and a **privilege-separated API token**.
    4. **ACLs** binding the role to the token at the pool — plus three
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
- **A storage that supports `images` content** (e.g. `local-lvm`, a ZFS pool, or
  a directory storage with *Disk image* enabled). The built-in `local` storage
  does **not** hold disk images by default, so `sand` cannot put a VM disk or a
  cloud-init drive there.
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
Datastore.AllocateSpace Datastore.Audit Pool.Audit"
```

Some of these are non-obvious, and if you trim them further you'll get confusing
failures — so, for the record, why each of the less-obvious ones is here:

| Privilege | Why `sand` needs it |
| --- | --- |
| `VM.Config.HWType` | Setting `scsihw`, `vga`, and `machine` on the base VM. Cloud images need `virtio-scsi-pci`, not the PVE default. |
| `VM.Config.Options` | Setting `agent`, `name`, and `ostype`. |
| `VM.Config.Disk` | Covers disk devices **and** the `boot` order. |
| `VM.Config.Cloudinit` | Injecting the SSH key, user, and network config. |
| `Pool.Audit` | So the pool name appears in listings — without it `sand` can't tell which VMs are its own. |
| `VM.GuestAgent.Unrestricted` | Only needed for guest-agent `exec`. It's the broadest privilege in the set; drop it if you never need `sand` to run a command via the agent (it uses SSH for shells regardless). |

!!! warning "Do not add `VM.Monitor` or `VM.Console`"
    `VM.Monitor` was **removed in PVE 9** — including it makes `pveum role add`
    reject the whole command. `VM.Console` is only for the VNC/SPICE console,
    which `sand` never uses. Leaving both out is deliberate.

## Step 3 — Create a user and a privilege-separated token

```bash
pveum user add sandbar@pve --comment "sandbar automation"
pveum user token add sandbar@pve prov --privsep 1 --output-format json
```

The second command prints the token **value exactly once**:

```json
{
  "full-tokenid": "sandbar@pve!prov",
  "value": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
  "info": { "privsep": "1" }
}
```

`--privsep 1` means the token carries **only** the permissions you grant it
explicitly below — even though its user could later be given more, the token
stays confined. **Save the value now**; it cannot be retrieved again. Write it to
a file `sand` will read (see [Step 6](#step-6-point-sand-at-the-host)), in the
form `sandbar@pve!prov=<value>`:

```bash
umask 077
printf 'sandbar@pve!prov=%s\n' 'xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx' \
  > ~/.config/sandbar/pve1.token
```

## Step 4 — Bind the role to the token at the pool

```bash
# Scope the token to the pool: this is what confines it to sandbar's own VMs.
pveum acl modify /pool/sandbar --roles SandbarProv --tokens 'sandbar@pve!prov'

# Add the VM storage to the pool so the storage privileges above are pool-scoped too.
pveum pool modify sandbar --storage local-lvm
```

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
pveum acl modify /sdn/zones/localnetwork/vmbr0 --roles SandbarNet --tokens 'sandbar@pve!prov'

# 2. Download the cloud image to storage, and read node CPU/memory for the board
#    header. Both are node-scoped, not pool-scoped.
pveum role add SandbarNode --privs "Sys.AccessNetwork Sys.Audit"
pveum acl modify /nodes/pve1 --roles SandbarNode --tokens 'sandbar@pve!prov'

# 3. Allocate the imported template on the storage (the download-url import step).
pveum acl modify /storage/local-lvm --roles SandbarProv --tokens 'sandbar@pve!prov'
```

| Privilege | Path | What it allows |
| --- | --- | --- |
| `SDN.Use` | `/sdn/zones/localnetwork/vmbr0` | Attaching a VM to **that one bridge** — nothing else on the network |
| `Sys.AccessNetwork` | `/nodes/pve1` | Downloading the cloud image to storage |
| `Sys.Audit` | `/nodes/pve1` | Reading node CPU/memory/disk stats for the board header |

### Verify the scope

Confirm the token ended up with **only** the paths you intended — this is the
check that proves the isolation guarantee actually holds:

```bash
pveum user permissions 'sandbar@pve!prov' --output-format json
```

The result should list **only** `/pool/sandbar`, your storage, the bridge, and
the node `/nodes/pve1`. **If any permission appears at `/`, the isolation
guarantee does not hold** — go back and remove the over-broad grant.

## Step 6 — Point `sand` at the host

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
    storage: local-lvm            # images-capable storage
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
| `storage` | The images-capable storage backing VM disks and the cloud-init drive. |
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
