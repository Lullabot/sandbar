# Provisioning Profiles

A **provisioning profile** is a `.sandbar/` directory committed to a project
repository that declares the extra packages, services, Ansible roles, and
setup steps that repo's development VM needs beyond the stock `sand` base.
Commit it, and every teammate who runs `sand create --clone-url` against that
repository gets the same configuration applied automatically.

!!! note "Not to be confused with Connection Profiles"
    "Provisioning profile" and [Connection Profile](connection-profiles.md)
    are unrelated features that happen to share the word "profile". A
    Connection Profile is *where* a VM runs (local Lima, or a remote host over
    SSH). A provisioning profile is *what gets installed* into a VM once it
    exists. This page is about the latter. Where this could be ambiguous,
    these docs always spell out "provisioning profile" or "connection
    profile" in full — never a bare "profile".

## What it guarantees — and what it doesn't

A committed provisioning profile makes a VM **configuration-identical and
reproducible**: the same committed `.sandbar/` always yields the same
declared packages, services, roles, and seed steps, for every teammate and
every re-provision. It does **not** make VMs byte-identical — `sand` does not
pin apt package versions, so two VMs built from the same profile at different
times can still differ at the byte level if upstream packages moved in
between. If you need that guarantee, it is out of scope for this feature.

## The `.sandbar/` layout

```
.sandbar/
├── profile.yml       # the manifest — required
├── roles/            # optional: Ansible roles referenced by `roles:` below
│   └── my-role/
│       ├── tasks/main.yml
│       └── ...
└── seed.yml           # optional: the seed tasks file referenced by `seed:` below
```

Only `profile.yml` is special-cased by name; `roles/` and the seed tasks
file's path are both declared inside the manifest (see below), so you may
name the seed file whatever you like as long as the manifest points at it.

## The manifest: `.sandbar/profile.yml`

The manifest is a YAML file declaring up to five top-level groups. Every
group is optional — omit a group entirely to declare nothing for it, or
declare an empty list. No other top-level key is permitted: an unrecognized
key fails validation by name, and there is no per-OS conditional logic and no
profile inheritance.

| Key | Shape | Meaning |
|---|---|---|
| `packages` | list of strings | apt package names to install into the clone. |
| `services` | list of strings | systemd unit names to enable and start in the clone. |
| `roles` | list of strings | names of Ansible roles under `.sandbar/roles/<name>/` to include. |
| `seed` | single string | a relative path to a repo-supplied Ansible tasks file, run last. |
| `toolset` | list of strings | names of [shipped provisioning profiles](#shipped-provisioning-profiles) (`claude`, `ddev`, `go`, `java`) this repo needs. |

```yaml
# .sandbar/profile.yml
packages:
  - cowsay
services:
  - my-app.service
roles:
  - my-role
seed: .sandbar/seed.yml
toolset:
  - go
```

Package names must look like valid Debian package names, service names must
look like valid systemd unit names (any of the standard suffixes:
`.service`, `.socket`, `.target`, and so on), role names must be bare
identifiers with no `/` or `.` — so a declared role can never resolve outside
`.sandbar/roles/`  — and `seed` must be a relative path with no `..` segment,
ending in `.yml` or `.yaml`. A malformed manifest fails finalize outright,
with a message naming the specific offending key or item, rather than being
silently ignored.

### Not declared here: VM resources

CPUs, memory, and disk size are **not** part of the manifest. Sizing is a
host-specific choice — how much RAM you can spare varies per developer
machine — and stays exactly where it is today: the `--cpus`/`--memory`/`--disk`
flags and the TUI create form. See the [CLI Reference](cli-reference.md).

## Guest-only discovery — nothing reaches the host

`sand` never fetches, parses, or reads `.sandbar/` on the host. The
repository's only path into the system is the clone that already happens
inside the guest: after the `project` role clones your repository during
finalize, a new stage checks whether the clone contains `.sandbar/profile.yml`
and, if so, validates and applies it — all inside the VM. A repository
without `.sandbar/` behaves exactly as it does today: no new variables, no
prompts, no behavior change.

Because everything happens guest-side and after the fact, there is **no
consent gate**. Cloning a repository into the VM already implies running its
code inside the guest — the sandbox is the VM itself, not a review step in
front of it. Repo-declared packages, roles, services, and seed tasks are
applied automatically, with no prompt to approve them. See
[Security Model](../reference/security-model.md) for the trust rationale.

## The two-tier execution model

Provisioning in `sand` happens at two points, and provisioning profiles use
both, for different purposes:

- **Base tier.** The four tools `sand` ships out of the box —
  [shipped provisioning profiles](#shipped-provisioning-profiles) — install
  into the shared `sandbar-base` image, selected via the `--with-*` flags or
  the TUI's create-form toggles, exactly as today. This is the expensive,
  once-per-base install, amortized across every clone. See
  [How Provisioning Works](../getting-started/how-it-works.md).
- **Finalize tier (per-clone).** A **repo-checked-in** provisioning profile —
  your `.sandbar/`, discovered after the clone — applies entirely during
  finalize, against that one clone only. It never touches the shared base
  image or its version stamp: a repo profile's packages, roles, services, and
  seed steps are installed into the individual VM being created, not baked
  into anything shared with other repos or other teammates' unrelated VMs.

This split exists because a repo profile is only discoverable *after* its
repository has been cloned — which, by construction, happens after the base
image already exists — so repo-specific content can never ride the shared
base and force every other repo's VM to carry it too.

### Per-clone toolset reconciliation

A repo profile's `toolset` group requests one or more of the shipped tools
(`claude`, `ddev`, `go`, `java`) for that repo, independent of whichever
`--with-*` flags were used to build the shared base. During finalize, each
declared tool is **reconciled into the clone**:

- If the shared base already has that tool (e.g. the base was built with
  `--with-go` on), the reconciliation step is a no-op — the tool is already
  there.
- If the base does not have it (e.g. the base was built with `--with-go=false`,
  or `go` was never selected), the tool's shipped profile is applied directly
  into **this clone only**, using the exact same shipped-profile content that
  the base tier uses.

Either way, the shared base's `v2:<playbook-hash>:<toolset>` version stamp is
never touched by a repo profile — reconciliation is entirely local to the one
clone finalize is provisioning.

## Idempotency and the root-execution contract

A repo profile's tasks — its declared roles and, especially, its `seed`
tasks file — run with **root privileges** (the finalize play's `become: true`),
and re-run every time finalize runs against that clone: on the initial
`sand create --clone-url`, and again on every subsequent `Reset` or
`Recreate` of that VM. There is no host-side record of "already applied" to
skip a second run.

This means:

- **Seed tasks and repo roles must be idempotent.** Write them the way you'd
  write any Ansible task: check state before changing it, or use modules
  that are naturally idempotent (`apt`, `systemd_service`, `file`, `copy`,
  and so on all are). A task that blindly appends to a file or unconditionally
  runs a one-time migration script will misbehave on the second run.
- **Seed tasks run as root by default.** If a step needs to run as the VM's
  primary user instead (e.g. writing into that user's home directory with the
  right ownership, or running a per-user tool), use Ansible's `become_user`
  on that specific task rather than assuming the play's ambient privilege
  level is what you want.
- **There is no undo.** Deleting the VM is the only way to remove what a repo
  profile installed; there's no "uninstall" path symmetrical with `roles`,
  `packages`, or `services`.

## Shipped provisioning profiles

The four optional dev tools `sand` has always offered —
[`claude`, `ddev`, `go`, and `java`](../getting-started/available-tools.md) —
are themselves provisioning profiles, shipped with `sand` and applicable at
either tier: at the base tier via `--with-*` flags/TUI toggles (their
long-standing behavior), or at the finalize tier when a repo's `toolset`
group requests one that the base doesn't already have. Because they use the
exact same manifest format documented above, they double as real, tested
examples for writing your own:

- `claude` — installs the Claude Code CLI and its configuration (a `roles`
  entry only: nothing to add to the apt transaction).
- `ddev` — registers ddev's apt repository/keyring and installs the `ddev`
  package (`roles` + `packages`).
- `go` — installs the Go toolchain (`packages` only).
- `java` — installs a headless JDK (`packages` only).

Read their manifests directly for a worked, minimal example of each
declaration group in practice — `shipped-profiles/claude/profile.yml`,
`shipped-profiles/ddev/profile.yml`, `shipped-profiles/go/profile.yml`, and
`shipped-profiles/java/profile.yml` in this repository.

## See also

- [How Provisioning Works](../getting-started/how-it-works.md) — the
  base/clone/finalize pipeline this feature's finalize stage extends.
- [Available Tools](../getting-started/available-tools.md) — the shipped
  provisioning profiles and how to toggle them.
- [CLI Reference](cli-reference.md) — the `--with-*` flags and `--clone-url`.
- [Security Model](../reference/security-model.md) — the no-consent-gate
  trust decision and what it means for repo-supplied Ansible.
- [The Embedded Playbook](../contributing/ansible-playbook.md) — for
  contributors: how the finalize stage is wired and why repo content never
  enters the embedded fileset.
