---
id: 12
group: "documentation"
dependencies: [9, 11]
status: "pending"
created: 2026-07-20
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Not mechanical writing: the privilege list must be exactly correct, and the three non-pool-scopable grants must be explained honestly, or the isolation guarantee the docs promise is false."
skills:
  - technical-writing
  - proxmox-api
---
# docs: Proxmox setup guide — dedicated pool, minimum-privilege role, and API token

## Objective

Write the step-by-step Proxmox setup guide: creating a dedicated resource pool,
a custom role holding the minimum privileges, an API user and
privilege-separated token, and the ACLs that scope it — plus a separate isolated
pool for automated tests. Update the surrounding documentation that currently
says no non-Lima backend exists.

## Skills Required

`technical-writing` (clear, verifiable, copy-pasteable instructions) and
`proxmox-api` (the privilege model, so the commands are correct rather than
plausible).

## Acceptance Criteria

- [ ] `mkdocs build --strict` succeeds with the new page in the nav and no broken
      links — this is the runnable verification for this task.
- [ ] Every `pveum` command in the guide is copy-pasteable and uses the **plural**
      flags (`--roles`, `--users`, `--tokens`) and `--output-format json`.
- [ ] The privilege list contains **no** `VM.Monitor` (removed in PVE 9 —
      including it makes `pveum role add` reject the entire command) and no
      `VM.Console` (only needed for VNC/SPICE, which sandbar does not use).
- [ ] The guide explicitly identifies the three privileges that **cannot** be
      pool-scoped, states which path each must be granted at, and explains what
      each one allows — rather than hiding them inside a command block.
- [ ] The guide includes a verification step
      (`pveum user permissions ... --output-format json`) and states what a
      correct result looks like, so the operator can confirm the scoping rather
      than assume it.
- [ ] A second, isolated pool and token for automated tests is documented, with
      the `PROXMOX_E2E_*` variables matching task 11's test file exactly —
      cross-check them, do not transcribe from memory.

## Technical Requirements

- **New**: `docs/using-sand/proxmox.md`, added to `mkdocs.yml` nav under
  "Using sand".
- **Update** `docs/using-sand/connection-profiles.md`: replace the "Future
  providers" section (which currently states Proxmox "is not available yet") and
  document the `proxmox` profile fields.
- **Update** `docs/reference/security-model.md`: the pool-scoping isolation
  rationale and token-file handling.
- **Update** `docs/reference/files-and-state.md`: the per-endpoint Proxmox state
  directory and the token file location/permissions.
- **Update** `AGENTS.md`: note the third provider and `internal/pve`, so future
  agents do not assume an SSH-only transport model.
- Match the existing docs' voice and Material-for-MkDocs admonition style — read
  two or three existing pages first.

## Input Dependencies

Task 9 (final profile field names), task 11 (the exact `PROXMOX_E2E_*` names).

## Output Artifacts

`docs/using-sand/proxmox.md`, updated `mkdocs.yml`, `connection-profiles.md`,
`security-model.md`, `files-and-state.md`, `AGENTS.md`.

## Implementation Notes

<details>

**The exact minimum role.** Use this privilege list verbatim:

```
VM.Allocate VM.Clone VM.Audit VM.PowerMgmt VM.Snapshot
VM.Config.Disk VM.Config.CPU VM.Config.Memory VM.Config.Network
VM.Config.Options VM.Config.Cloudinit VM.Config.HWType VM.Config.CDROM
VM.GuestAgent.Audit VM.GuestAgent.Unrestricted
Datastore.AllocateSpace Datastore.Audit Pool.Audit
```

Explain *why* several non-obvious ones are needed, because an operator
minimizing further will otherwise remove them and get confusing failures:

- `VM.Config.HWType` — required for `scsihw`, `vga`, and `machine`.
- `VM.Config.Options` — required for `agent`, `name`, and `ostype`.
- `VM.Config.Disk` — also covers `boot`.
- `Pool.Audit` — only so `/cluster/resources` populates the `pool` field.
- `VM.GuestAgent.Unrestricted` — needed **only** if guest-agent `exec` is used;
  note it can be dropped otherwise, since it is the broadest privilege in the set.

**The setup sequence:**

```bash
# 1. A dedicated pool. Every VM sandbar creates lands here automatically.
pveum pool add sandbar --comment "sandbar-managed VMs"

# 2. The minimum role.
pveum role add SandbarProv --privs "VM.Allocate VM.Clone VM.Audit ..."

# 3. A dedicated user and a privilege-separated token.
#    The token value is displayed ONCE and cannot be retrieved later.
pveum user add sandbar@pve --comment "sandbar automation"
pveum user token add sandbar@pve prov --privsep 1 --output-format json

# 4. Scope the token to the pool.
pveum acl modify /pool/sandbar --roles SandbarProv --tokens 'sandbar@pve!prov'

# 5. Storage — add it to the pool to keep it pool-scoped.
pveum pool modify sandbar --storage local-lvm
```

Then the section that carries the real weight — write it as prose, not a
footnote:

> ### Three privileges that cannot be pool-scoped
>
> Proxmox implements pool permissions by projecting roles onto the pool's
> members, and a pool can only contain VMs and storage. Three things sandbar
> needs are therefore not grantable at the pool, and must be granted at the
> narrowest path that does work:

| Privilege | Path | What it allows |
| --- | --- | --- |
| `SDN.Use` | `/sdn/zones/localnetwork/vmbr0` | Attaching a VM to that one bridge |
| `Sys.AccessNetwork` | `/nodes/pve1` | Downloading the cloud image to storage |
| `Sys.Audit` | `/nodes/pve1` | Reading node CPU/memory stats for the board header |

Note that a plain Linux bridge lives under the synthetic zone `localnetwork`, and
that with a VLAN tag the path gains the tag as a further segment. State plainly
that none of these three grants any access to another VM — that is the point of
naming them individually instead of granting something broader.

Include the verification step and say what good looks like:

```bash
pveum user permissions 'sandbar@pve!prov' --output-format json
```

> The result should list **only** `/pool/sandbar`, your storage, the bridge, and
> the node. If any permission appears at `/`, the isolation guarantee does not
> hold.

**Test pool section.** A second pool (`sandbar-test`), its own role assignment,
and its own token, plus the `PROXMOX_E2E_*` block. Make the point that the test
pool is what lets automated runs create and destroy VMs freely without any
possibility of touching day-to-day work.

**Profile example:**

```yaml
- id: pve1
  name: proxmox
  type: proxmox
  enabled: true
  host: pve1.example.com
  node: pve1
  pool: sandbar
  storage: local-lvm
  bridge: vmbr0
  token_file: ~/.config/sandbar/pve1.token
```

Add an admonition that `token_file` must be mode `0600` — sandbar refuses to
read it otherwise — and that the token value is never stored in
`profiles.yaml`.

Also document the prerequisites honestly: the machine running `sand` must be able
to reach the VM subnet over SSH, the storage must support `images` content (the
built-in `local` storage does not, by default), and PVE 8.1+ is required.

</details>
