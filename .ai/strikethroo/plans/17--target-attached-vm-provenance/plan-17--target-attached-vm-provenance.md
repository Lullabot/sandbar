---
id: 17
summary: "Move VM managed-ness/provenance from the per-controller registry to per-instance, target-attached metadata so multiple controllers see one consistent managed fleet"
created: 2026-07-17
---

# Plan: Target-Attached VM Provenance

## Original Work Order

> Move VM managed-ness / provenance from the per-controller registry
> (~/.local/share/sandbar/managed-vms.json) to per-instance, target-attached
> metadata, so multiple controllers (e.g. two laptops) managing the same
> host/fleet see a consistent set of managed VMs. Design: write a marker file
> inside the Lima instance dir on the owning host (~/.lima/<name>/sandbar.json)
> containing what registry.Entry holds today (base, CreateConfig, sandbar
> version, created-at). Introduce a provider-level Provenance/MarkManaged
> abstraction that Lima implements as the instance-dir file, and that
> Proxmox/cloud providers can later implement as VM tags/labels. Local registry
> becomes a cache + known-targets list, not the ownership truth. Include
> batched/mux'd reads to avoid per-VM SSH round-trips, an idempotent
> adoption/migration path from the existing controller-side registry, and also
> fix the latent bug where RemoteLimaHome is never exported as LIMA_HOME to the
> remote limactl invocation. Key code: internal/registry/registry.go (Entry,
> Scope, IsManagedInScope, defaultPath), internal/ui/board.go:142 (boardVMs
> roster gate), internal/provider/{local,remote,select}.go,
> internal/lima/{client.go List, sshhost.go ReadFile/WriteFile, hostfiles.go},
> internal/provision/provision.go (existing host-side version stamps as prior
> art).

## Plan Clarifications

| Question | Answer |
| --- | --- |
| How should the existing per-controller registry be handled during cutover? | **Adopt + cache, keep fallback.** A one-time idempotent adoption writes a marker for each existing managed entry; the local registry remains as a cache/known-targets list and is still consulted as a fallback for the roster gate for one release, so no VM loses its tile on upgrade. |
| Should local (non-SSH) Lima also switch to instance-dir markers, or keep the local registry as its ownership truth? | **Unify — local uses markers too.** Local mode reads/writes `~/.lima/<name>/sandbar.json` exactly as remote does. This is what makes "run sandbar on the mini" and "connect to the mini from a laptop" converge on the same managed set. |
| What is the provider-abstraction scope for this plan? | **Seam + Lima only.** Define the provenance provider interface and implement it for Lima. No Proxmox/cloud implementation is built now; the interface must merely be shaped so those providers can implement it later (YAGNI). |
| Is backwards compatibility required? | Yes, for one release, via the adopt-and-fallback path above. There is no requirement to preserve the on-disk `managed-vms.json` schema beyond keeping it readable as a legacy fallback; new writes may extend it as a cache. |
| Where should the marker live — inside the Lima instance dir, or in the `_sand/` sibling namespace sand already uses for host metadata? | **Instance dir (`~/.lima/<name>/sandbar.json`).** Chosen for the "deleted for free" lifecycle (`limactl delete` removes the whole instance dir, so no orphan and no stale-marker aliasing on name reuse) even though it deliberately deviates from sand's existing `_sand/`-only convention. This deviation is accepted knowingly — see the corrected Background note and the new tolerance risk/mitigation. *(Refinement 2026-07-17.)* |

## Executive Summary

Sandbar today decides whether a VM is "managed" (and therefore gets a tile in
the board and is safe to reset/recreate against its clone base) by consulting a
JSON registry stored on the **controlling machine** at
`~/.local/share/sandbar/managed-vms.json`, keyed per `(scope, name)`. Because
that file lives with the controller rather than with the VM, two controllers —
e.g. a user's two laptops both connecting to the same Mac mini as the same SSH
user — hold two independent, divergent sources of truth for one fleet. A VM
created from laptop A appears in `limactl list` from laptop B but gets no tile,
and vice versa; running sandbar directly on the mini uses yet another scope key
and shows a third view. The raw discovery is identical in all modes; only the
provenance record differs.

This plan relocates provenance from the controller to the **target**: the
machine (or, later, the hypervisor/cloud account) that actually owns the VM. For
Lima that means a marker file written inside the instance directory,
`~/.lima/<name>/sandbar.json`, carrying exactly the fields
`registry.Entry` holds today. Managed-ness becomes a question answered *through
the provider* ("does this instance carry a sandbar marker?") rather than by a
controller-local lookup. Because the marker lives in the instance directory it
inherits the VM's lifecycle for free — `limactl delete` removes it, so there are
no orphaned or aliased claims — and every controller that can reach the host
converges on the same answer with no sync protocol.

The change is introduced behind a small provider seam (`Provenancer`) so the
Lima implementation is an instance-dir file while Proxmox/cloud can later be VM
tags/labels — the only metadata model that works for providers with no shared
controller filesystem. The local registry is demoted to a cache plus the list of
known targets/profiles; for one release it also serves as a legacy fallback and
as the source for a one-time, idempotent adoption pass that stamps markers onto
already-managed VMs. Finally, the plan fixes a latent correctness bug where the
profile's `RemoteLimaHome` is honored for host file reads but never exported as
`LIMA_HOME` to the remote `limactl` invocation, which can make discovery and
file reads (now including marker reads) disagree on hosts with a non-default
Lima home.

## Context

### Current State vs Target State

| Current State | Target State | Why? |
| --- | --- | --- |
| Managed-ness is recorded in `~/.local/share/sandbar/managed-vms.json` on the controlling machine (`internal/registry/registry.go` `defaultPath`). | Managed-ness is recorded on the target as per-instance metadata (`~/.lima/<name>/sandbar.json` for Lima), read/written through a provider seam. | The controller registry is per-machine; two controllers of one fleet diverge. Provenance belongs with the VM. |
| The board roster gate is `reg.IsManagedInScope(name, scope)` (`internal/ui/board.go:142`) against the local file. | The roster gate asks the provider whether the instance carries a marker (with the legacy registry as a one-release fallback). | Makes local-on-host and remote-into-host, and multiple controllers, show the same set. |
| Entries are keyed per `(scope, name)`; the same VM under local scope vs. a remote scope are two different keys, deliberately invisible to each other. | The marker is intrinsic to the instance, so scope no longer gates ownership; `Scope` is retained only for UI grouping and known-target/profile bookkeeping. | The per-scope key was the mechanism that made the same VM look unmanaged from a different controller/mode. |
| Local Lima uses the controller registry as ownership truth; remote uses the same registry under a remote scope. | Both local and remote Lima read/write the same instance-dir marker on the owning host. | Convergence between "run on the mini" and "connect to the mini" requires one shared record. |
| `RemoteLimaHome` (profile `lima_home`) is used for host file reads but is never exported as `LIMA_HOME` to the remote `limactl` (`internal/lima/sshhost.go` ssh argv construction). | The remote `limactl` invocation is run with `LIMA_HOME` set to the profile's resolved remote Lima home. | On hosts with a non-default Lima home, discovery and file reads (including marker reads) otherwise resolve different instance directories. |
| No adoption/migration path; ownership is wherever each controller's file happens to record it. | A one-time idempotent adoption stamps markers onto existing managed VMs; the legacy registry is a fallback for one release. | Upgrading controllers must not make already-managed VMs lose their tiles. |

### Background

Both providers delegate discovery to the same Lima core: `limaProvider.List`
(`internal/provider/local.go`) calls `Client.List` (`internal/lima/client.go`),
which runs `limactl list --format json` with no filtering; the remote provider
(`internal/provider/remote.go`) embeds `*limaProvider` and inherits `List`
unchanged, running it over the SSH hop (`internal/lima/sshhost.go`). Filtering
to "managed" happens only at the UI/registry layer.

The registry already stores everything needed for a marker: an `Entry` holds
`Base`, `Config` (`vm.CreateConfig`), `Provider`, and `RemoteTarget`
(`internal/registry/registry.go`). The provisioner already demonstrates the
host-side-metadata *mechanism* this plan reuses: it writes and reads version
**stamps** and base overlays through the `HostFiles` seam
(`internal/provision/baseversion.go`, `internal/provision/provision.go`), and
`SSHHost` already implements `ReadFile`/`WriteFile`/`Stat` and compound `sh -c`
reads over SSH (`internal/lima/sshhost.go`). So the target-side marker reuses an
existing I/O mechanism and transport; this plan introduces a new record and a
seam to address it, not a new way to touch the host.

**Correction to a prior assumption (validated against the code):** an earlier
draft claimed the version stamp is "prior art for writing *into the instance
dir*." That is not accurate. Every existing sand host-file write deliberately
targets a sibling namespace, `~/.lima/_sand/` — the base version stamp lives at
`filepath.Join(hf.LimaHome(), "_sand", baseName+".playbook-version")`
(`internal/provision/baseversion.go`), and the overlay and base lock are
likewise under `_sand/`, "namespaced to avoid colliding with Lima's own state."
Sand has never written inside `~/.lima/<name>/`. This plan therefore makes a
*conscious, tested* deviation from that convention (see Plan Clarifications): the
marker goes inside the instance dir specifically to inherit the VM's lifecycle
(deleted for free, no name-reuse aliasing). The deviation was de-risked
empirically — with `limactl` 2.1.3, dropping a stray `sandbar.json` into an
instance directory left `limactl list` fully functional (all instances still
enumerated and parsed, targeted inspect unaffected). That tolerance is not a
documented Lima guarantee, so the plan carries an explicit tolerance risk and a
guard (see Risks and Self Validation) rather than assuming it holds for future
Lima versions.

The reported symptom — different VMs shown when running sandbar on a host vs.
connecting to it remotely — is, in its intentional part, a direct consequence of
the per-controller, per-scope registry. This plan is the structural fix, and it
is explicitly forward-looking: instance-attached metadata is the only ownership
model that survives the move to Proxmox and cloud providers, where controllers
share no filesystem.

## Architectural Approach

The work divides into a provider seam, a Lima implementation of that seam, a
roster/ownership rewiring in the UI and CLI to consult the provider instead of
the controller registry, a demotion of the registry to cache + adoption source,
and the standalone `LIMA_HOME` transport fix. The seam is deliberately minimal
(Lima-only implementation) but shaped for future providers.

```mermaid
flowchart TD
    subgraph Controller[Controller machine - laptop A or B, or the mini itself]
        UI[board roster gate / CLI ownership] --> PSeam[Provenancer seam]
        Cache[(local registry\ncache + known targets\n+ legacy fallback)] -.one-time adopt.-> PSeam
    end
    PSeam -->|Lima impl| Marker
    subgraph Host[Owning host / target]
        Marker[~/.lima/&lt;name&gt;/sandbar.json\nbase, CreateConfig, version, created-at]
        LList[limactl list --format json]
    end
    UI --> LList
    LList -->|batched read of all markers\nover one mux'd ssh| PSeam
```

### Component 1 — The `Provenancer` provider seam

**Objective**: Give the rest of the codebase a provider-agnostic way to ask
"is this instance sandbar-managed, and with what provenance?" and to mark/unmark
it, so ownership no longer routes through the controller registry.

Define a small interface (working name `Provenancer`) exposing, at minimum: a
batched read that returns provenance for all currently-listed instances in one
call (to avoid per-VM round-trips), a single-instance read, a write/mark
operation invoked at create time, and an unmark/clear operation for teardown
paths that do not delete the instance. The provenance payload mirrors the
current `registry.Entry` fields (base, `CreateConfig`, sandbar version,
created-at) plus a schema version for the marker itself. The seam lives alongside
the existing provider interfaces (`internal/provider`) and is implemented by the
Lima provider; the remote provider inherits it through the same embedding that
already gives it `List`. Providers that cannot support provenance would return a
well-defined "unsupported" signal, but Lima (local and remote) fully supports it.

**The marker must be authoritative for correctness, not just display.** The
`base` field is load-bearing: `manage.RecreateBase`
(`internal/manage/manage.go`) uses `reg.BaseInScope(name, scope)` both to *gate*
whether a VM may be reset/recreated (an unmanaged VM is refused) and to supply
the base image to clone from. So the marker payload must carry the base name, and
provenance reads must be able to answer "is this managed, and from what base?"
The `ReconcileScoped` semantic — a VM deleted outside sand stops being
recreatable — is preserved for free by the instance-dir location: when the
instance dir is gone, so is the marker, so the answer becomes "unmanaged"
automatically.

Design the batched read as the primary entry point so the board's per-refresh
cost is one host round-trip, not N. The single-instance read exists for CLI
paths that already target one VM (e.g. `sand shell` routing, recreate).

**Note — a layer shift.** Today provenance is recorded one layer above the
provider, in the caller: `manage.RecordSuccess` → `reg.AddScoped`, invoked from
`cmd/sand/create.go` and the TUI `provisionDoneMsg` handler. Writing the marker
through the provider seam moves the *authoritative* provenance write down into
the provider/provisioner layer (which already holds `HostFiles` write access and
the resolved base name). The caller's registry write is retained but demoted to
a cache update. The plan should make this ownership shift explicit when wiring
`RecordSuccess`.

### Component 2 — Lima instance-dir marker implementation

**Objective**: Implement `Provenancer` for Lima as a file at
`<LimaHome>/<name>/sandbar.json`, written/read through the existing `HostFiles`
seam so it works identically for local (`LocalFiles`) and remote (`SSHHost`)
hosts.

The marker path is resolved against the same Lima home the provider uses for
discovery — `hf.LimaHome()` — so that after the `LIMA_HOME` fix (Component 5)
discovery and marker I/O always agree. Note `LimaHome()` may be **relative** on
the remote host (`SSHHost` returns the profile's `RemoteLimaHome`, default
`".lima"`, resolved against the remote `$HOME` at command time); this is fine for
the `cat`/`stat`/`mkdir` operations the marker uses and does not require an
absolute path, but the implementation must not assume `LimaHome()` is absolute.

Writing happens at create time, after the instance directory exists (inside
`Provisioner.createVM`, which already holds the `HostFiles` seam and the resolved
base name), using `WriteFile` with restrictive perms consistent with the existing
stamp writes. The single read is `ReadFile` with a not-exist result meaning
"unmanaged."

**The batched read requires extending the `HostFiles` seam.** The interface
currently exposes only per-path `ReadFile`/`Stat` — there is no batch primitive,
so a one-round-trip read of all markers is *not* free through the existing seam.
The plan adds a batch-read method implemented for both `localFiles` (a
single-pass directory walk) and `SSHHost` (one compound `sh -c 'for d in
<LimaHome>/*/sandbar.json; do …; done'` over the already-multiplexed connection),
returning a `name -> provenance` map. This mirrors the existing `HostResources`
precedent (a multi-line `sh -c` script executed in one round trip). Falling back
to N per-path `ReadFile` calls is possible (mux/ControlMaster makes each cheap)
but is explicitly the non-goal; the batch method is the primary path.
Missing/parse-failed markers are treated as unmanaged, never as errors that abort
a listing.

Because the marker lives inside the instance directory, `limactl delete` removes
it with the VM (verified: `limactl delete` tears down the whole instance dir), so
no explicit cleanup is required on delete and no stale marker can be inherited by
a later VM that reuses the name. Explicit unmark is only for flows that
intentionally relinquish management without deleting the VM. This is the concrete
payoff of choosing the instance dir over `_sand/`: the `_sand/` alternative would
have required a reconcile pass to prune orphaned markers and to prevent name
reuse from inheriting a stale claim.

### Component 3 — Rewire the roster gate and CLI ownership to the provider

**Objective**: Make the board and CLI decide managed-ness from provider
provenance (with a one-release legacy fallback) instead of
`IsManagedInScope`.

The board's `boardVMs` roster gate (`internal/ui/board.go:142`) changes from
"tile iff `reg.IsManagedInScope(name, scope)`" to "tile iff the instance carries
a provenance marker, OR (legacy fallback) the local registry records it as
managed, OR it has an active provision job." The provenance map is fetched once
per refresh (Component 2's batched read) inside the existing async `refreshCmd`
closure (`internal/ui/commands.go`), which already runs `List` and a blocking
remote `HostResources` round trip off the Update goroutine — so the extra fetch
fits the existing async model and does not block the UI thread.

**Full blast radius (every non-test scoped-provenance call site to rewire):**

| Call | Site | Role |
| --- | --- | --- |
| `IsManagedInScope` | `internal/ui/board.go:142` | roster gate (display) |
| `IsManagedInScope` | `internal/ui/board.go:494` | `stopAllTargets` gate (correctness) |
| `IsManagedInScope` | `cmd/sand/shell.go:310` (+ iface decl `:49`) | multi-profile `sand shell` owner routing (correctness) |
| `BaseInScope` | `internal/ui/board.go:1027` | `traitsOf` base for tile render (display) |
| `BaseInScope` | `internal/manage/manage.go:42` | `RecreateBase` — reset gate + base to clone (correctness) |
| `AddScoped` | `internal/manage/manage.go:57` | `RecordSuccess` — record provenance after create |
| `ReconcileScoped` | `internal/manage/manage.go:29` | `Reconcile` — prune entries for VMs deleted outside sand |

`manage.*` reaches further through its callers: `RecreateBase` ←
`cmd/sand/create.go` and the TUI reset gate; `RecordSuccess` ←
`cmd/sand/create.go` + TUI `provisionDoneMsg`; `Reconcile` ← `cmd/sand/create.go`
+ `internal/ui/model.go`. Each site resolves ownership/base from provenance
(marker first, registry fallback for one release) and, at create, writes the
marker. `Scope` is retained for UI grouping and for keying the known-targets
list, but it stops being the ownership discriminator. Because several of these
sites are correctness gates (stop-all, shell routing, recreate), the marker — not
a display hint — must be the source they trust.

### Component 4 — Demote the registry to cache + adoption source

**Objective**: Keep the local registry useful (fast cache, list of known
targets/profiles, one-release legacy fallback) while removing its role as the
ownership source of truth, and drive a one-time idempotent adoption that stamps
markers onto already-managed VMs.

On first contact with a target after upgrade, run an adoption pass: for each
instance the controller's registry records as managed under any scope, if the
live instance exists and carries no marker, write the marker from the registry
entry. Adoption is idempotent (a present marker is left untouched) and safe to
run repeatedly. The registry file continues to be read as a fallback in the
roster gate for one release so that a controller that has not yet run adoption
against a given host still shows the right tiles. New writes to the registry are
treated as a cache of last-known provenance, not the authority. Document the
one-release fallback window so the fallback and legacy read path can be removed
in a follow-up.

### Component 5 — Export `LIMA_HOME` to the remote `limactl` (latent-bug fix)

**Objective**: Ensure the remote `limactl` invocation resolves the same Lima
home that sandbar uses for host file reads, so discovery and marker I/O agree on
hosts with a non-default Lima home.

The profile's `RemoteLimaHome` (`internal/lima/sshhost.go`, from profile
`lima_home`) is currently consumed only by the `LimaHome()` file-reading seam
(and `HostResources`/`StagePlaybook` path building) and never reaches the remote
command. Verified: the argv builders `sshBase` and `sshCommand`
(`internal/lima/sshhost.go`) emit only `ssh` flags (`-t`, `-p`, `-i`, mux `-o`
flags), the target, and the shell-quoted remote argv (`limactl …` for `Output`) —
there is **no** `LIMA_HOME=` token anywhere, and no `cmd.Env`/`Setenv` threading.
So the remote `limactl` runs with whatever `LIMA_HOME` the remote login shell
happens to have, while sand's own reads use `h.cfg.RemoteLimaHome`; if the two
differ, discovery and marker/stamp reads silently diverge.

The fix prepends `LIMA_HOME=<resolved remote Lima home>` to the remote `limactl`
invocation in `sshCommand` (as a remote-process env assignment in the quoted
remote command, not the local ssh client env). This is independently correct
regardless of the provenance change, but it is prerequisite to markers being read
from the same directory `limactl list` enumerates. Note the env is *entirely
unmanaged across the hop today* — the ssh command passes no env at all, which is
why the laptop's local `LIMA_HOME`/`XDG_*` correctly does not leak to the host,
but also why the intended remote home is never asserted. The change must keep
that non-leak property (assign only the one intended var on the remote side) and
be verified against the existing loopback/second-user e2e.

## Risk Considerations and Mitigation Strategies

<details>
<summary>Technical Risks</summary>
- **`limactl` intolerance of an unknown file in the instance dir**: The marker
  lives inside `~/.lima/<name>/`, a directory Lima owns; a stricter future
  `limactl` could reject or mishandle a stray `sandbar.json`, and `limactl list`
  is already known to be fragile about instance-dir contents (it fails fatally
  when it "cannot load instance"). Sand's own convention has always avoided this
  by writing to `_sand/`.
    - **Mitigation**: Empirically validated on `limactl` 2.1.3 — a stray
      `sandbar.json` in an instance dir left `list` fully functional (all
      instances enumerated/parsed, targeted inspect unaffected). Add a guard test
      in the Lima e2e that creates a marker and asserts `limactl list`/inspect
      still succeed, so a Lima upgrade that breaks this tolerance fails CI rather
      than users. Keep the marker payload minimal and never named like a Lima
      artifact. If a future Lima version breaks tolerance, the `_sand/<name>`
      fallback (with a reconcile pass) remains the escape hatch.
- **`HostFiles` seam has no batch primitive**: A one-round-trip read is not
  available through the existing per-path `ReadFile`/`Stat` interface, so a naive
  implementation would do N SSH invocations per refresh.
    - **Mitigation**: Extend the seam with a batch-read method implemented for
      both `localFiles` (directory walk) and `SSHHost` (single compound `sh -c`,
      per the `HostResources` precedent). Reuse the existing SSH multiplexing.
- **Host can forge or corrupt a marker**: A compromised or buggy host could
  present false provenance.
    - **Mitigation**: The controller already trusts the host completely (it runs
      `limactl` there), so no new trust is ceded. Treat malformed/missing markers
      as "unmanaged" rather than erroring, so a bad marker degrades gracefully to
      no-tile instead of crashing a listing.
- **`LIMA_HOME` env handling regressing prior ssh-env fixes**: Injecting env
  into the remote command risks re-introducing env leakage previously fixed.
    - **Mitigation**: Apply `LIMA_HOME` narrowly to the remote process argv,
      verify against the existing loopback/second-user e2e, and add coverage that
      asserts the remote `limactl` sees the intended home.
</details>

<details>
<summary>Implementation Risks</summary>
- **Marker/instance lifecycle edge cases** (rename, reset, migrate): The base
  rename/migrate flows already special-case stamps; markers must behave sanely
  across the same operations.
    - **Mitigation**: Define marker behavior for create, delete (free via
      instance-dir removal), reset (marker persists — same VM), and rename
      (marker moves with the instance dir or is re-stamped), mirroring how the
      provisioner already carries stamps across a rename.
- **Adoption running against the wrong/partial fleet**: A controller might adopt
  based on a stale registry, stamping a marker onto a VM it should not claim.
    - **Mitigation**: Adopt only when the live instance exists AND has no marker;
      never overwrite an existing marker; scope adoption to entries the registry
      already recorded as managed. Log adopted names.
</details>

<details>
<summary>Compatibility Risks</summary>
- **Mixed-version controllers during the transition**: An old controller (no
  marker awareness) and a new one manage the same fleet simultaneously.
    - **Mitigation**: New controllers keep writing the registry as a cache and
      keep the registry fallback for one release, so an old controller continues
      to work from its own registry while new controllers converge on markers.
      The old controller simply does not benefit from cross-controller
      convergence until upgraded — no regression relative to today.
</details>

## Success Criteria

### Primary Success Criteria

1. A VM created by sandbar on host X (local mode) shows a tile when a second
   controller connects to host X over remote SSH as the same user, and vice
   versa — without any manual registry copying.
2. Two controllers (simulating two laptops) connecting to the same host as the
   same SSH user display the identical managed-VM set on the board.
3. Deleting a managed VM via `limactl delete` (or sandbar's delete path) leaves
   no marker behind, and a subsequently-created VM reusing the same name is not
   shown as managed until it is itself marked.
4. Upgrading a controller that already has managed VMs recorded in
   `managed-vms.json` results in those VMs remaining visible (adoption stamps
   markers; fallback covers pre-adoption reads) — no managed VM loses its tile.
5. On a host configured with a non-default Lima home, remote `limactl list` and
   sandbar's marker reads resolve the same instance directory (the `LIMA_HOME`
   fix), and the managed set is correct.
6. Board refresh performs a bounded number of host round-trips for provenance
   (batched), not one per VM.
7. `RecreateBase` (reset/recreate) is gated and sourced from the marker: a marked
   VM can be recreated against its recorded base; a VM whose marker is absent
   (e.g. deleted outside sand) is refused — exercising the correctness paths, not
   only display.
8. A CI guard asserts `limactl list`/inspect still succeed with a marker present
   in an instance dir, so a Lima upgrade that breaks that tolerance fails CI.

## Self Validation

After all tasks are complete, an executing LLM should verify the implementation
by exercising the real system, not only running unit tests:

1. **Cross-mode convergence (local ↔ remote)**: Using the project's Lima e2e
   harness (see `molecule/` and the existing remote-Lima-over-SSH loopback e2e),
   create a managed VM in local mode, then list via the remote provider pointed
   at the same host/user, and assert (programmatically or via captured board
   output) the VM appears as managed in both. Confirm the marker file exists at
   `<LimaHome>/<name>/sandbar.json` on the host and contains the expected base,
   CreateConfig, version, and created-at fields.
2. **Two-controller convergence**: Simulate a second controller by invoking the
   remote provider from a clean `XDG_DATA_HOME` (empty registry) against the same
   host; confirm the managed set matches the first controller's without copying
   `managed-vms.json`.
3. **Lifecycle**: Delete the VM and assert the marker is gone (no orphan);
   recreate a VM with the same name without marking it and assert it is listed by
   `limactl` but shows no tile.
4. **Adoption/fallback**: Seed a legacy `managed-vms.json` with an entry for an
   existing unmarked VM, run the adoption path, and assert a marker is now
   present and the VM shows managed; run adoption again and assert the marker is
   unchanged (idempotent). Before adoption runs, assert the fallback still shows
   the tile.
5. **`LIMA_HOME` fix**: Configure a profile with a non-default `lima_home` on a
   test host, and assert the remote `limactl list` enumerates instances from that
   directory and that marker reads resolve the same path (e.g. by capturing the
   remote command and confirming `LIMA_HOME` is present, plus an end-to-end list
   returning the expected instance).
6. **Round-trip bound**: Instrument or trace a board refresh over SSH and confirm
   provenance is fetched in a single batched call rather than one per VM.
7. **`limactl` tolerance guard**: With a marker present in a real instance dir,
   run `limactl list --format json` and a targeted `limactl list <name>` and
   assert both succeed and enumerate/parse the instance (the check performed
   during refinement on 2.1.3), wired as a CI assertion.
8. **Recreate correctness**: Drive `sand` reset/recreate against a marked VM and
   confirm it clones from the marker's recorded base; remove the marker (or
   delete + recreate the instance unmarked) and confirm recreate is refused —
   verifying the marker drives the correctness gate, not just tiles.

## Documentation

- Update `AGENTS.md` and/or `internal/registry` and `internal/provider` package
  docs to describe the new ownership model: markers are the source of truth, the
  registry is a cache + known-targets + one-release fallback, and `Scope` is UI
  grouping rather than ownership.
- Document the marker file location, schema, and lifecycle (created on create,
  removed with the instance, adopted from the legacy registry once) for future
  provider implementers (Proxmox/cloud) so they know what the seam expects.
- Note the profile `lima_home` → remote `LIMA_HOME` behavior in the relevant
  profile/remote docs, since it now affects discovery, not just file reads.
- Record the one-release fallback window and the follow-up needed to remove the
  legacy registry read path.

## Resource Requirements

### Development Skills

Go; familiarity with the sandbar provider/lima/registry architecture; SSH
command construction and multiplexing; Lima instance layout; the project's
Molecule/Lima e2e harness.

### Technical Infrastructure

Lima on the dev/CI host; the existing remote-Lima-over-SSH loopback e2e
(including the second-user variant) for cross-controller and non-default-home
testing; the project's Go test and CI tooling.

## Integration Strategy

The seam is introduced additively: the Lima provider gains a `Provenancer`
implementation while the registry remains present as cache/fallback, so the
system is functional at every step. The roster gate switches to "marker OR legacy
registry OR active job," which is a superset of today's behavior during the
transition and therefore cannot hide a currently-visible VM. The `LIMA_HOME` fix
is self-contained and can land first as an independent correctness improvement.
Removal of the legacy fallback and the registry's ownership role is deferred to a
follow-up plan after the one-release window.

## Notes

- Scope is intentionally limited to the provider seam plus a Lima implementation;
  Proxmox and cloud implementations are explicitly out of scope for this plan but
  the interface must be shaped so a tag/label-based implementation fits without
  redesign.
- The change deliberately does not add a controller-to-controller sync protocol;
  convergence is achieved purely by relocating the record to the shared target,
  which is why it also generalizes to providers with no shared controller
  filesystem.
- The retained `Scope` type still matters for grouping and for keying the list of
  known targets/profiles; only its role as the ownership discriminator is
  removed.

### Decision Log

- **Marker location = instance dir, not `_sand/`.** Deliberately deviates from
  sand's established "write host metadata only under `~/.lima/_sand/`" convention
  (base stamps/overlay/lock all live there). Chosen for deleted-for-free
  lifecycle and no name-reuse aliasing. De-risked empirically on `limactl` 2.1.3
  and guarded by a CI tolerance test; `_sand/<name>` + reconcile is the documented
  fallback if a future Lima version breaks tolerance.
- **Batched marker read requires a new `HostFiles` seam method** (both `localFiles`
  and `SSHHost`); it is not available through the existing per-path `ReadFile`.
- **Provenance is load-bearing for correctness** (recreate gate + base name via
  `RecreateBase`, stop-all, shell routing), so the marker — not the registry — is
  the trusted source; the marker payload must carry the base name.
- **Authoritative provenance write moves down** into the provisioner/provider
  layer (where `HostFiles` + resolved base already are); the caller's registry
  write is demoted to a cache update.
- **`LIMA_HOME` fix** = prepend `LIMA_HOME=<remote home>` to the remote `limactl`
  argv in `sshCommand`; today no env crosses the hop, so the intended remote home
  is never asserted. Keep the non-leak property (assign only that one var).

### Change Log

- 2026-07-17: Refinement pass. Corrected the false "prior art for writing into
  the instance dir" claim (sand actually uses `_sand/`); recorded the marker
  location decision (instance dir) with empirical `limactl` 2.1.3 tolerance
  validation and a new tolerance risk + CI guard; expanded Component 3 with the
  full scoped-provenance call-site inventory and flagged the correctness (not
  display-only) gates; made the marker payload carry the base name for
  `RecreateBase`; noted the batched read needs a new `HostFiles` seam method and
  fits the existing async `refreshCmd`; added the provenance-write layer shift;
  strengthened Component 5 with the exact argv finding and the env-non-leak
  constraint; added the relative-`LimaHome()` caveat; added success criteria 7–8
  and self-validation steps 7–8; added this Decision Log.

## Execution Blueprint

**Validation Gates:**
- Reference: `.ai/strikethroo/config/hooks/POST_PHASE.md`

### Dependency Diagram

```mermaid
graph TD
    T1[Task 1: LIMA_HOME fix]
    T2[Task 2: Provenancer seam + payload]
    T3[Task 3: Lima marker I/O + batched read]
    T4[Task 4: Rewire manage + CLI]
    T5[Task 5: Rewire UI roster]
    T6[Task 6: Registry demotion + adoption]
    T7[Task 7: Integration/e2e + tolerance guard]
    T8[Task 8: Documentation]

    T2 --> T3
    T3 --> T4
    T3 --> T5
    T3 --> T6
    T1 --> T7
    T4 --> T7
    T5 --> T7
    T6 --> T7
    T4 --> T8
    T5 --> T8
    T6 --> T8
```

### ✅ Phase 1: Foundations (no dependencies)
**Parallel Tasks:**
- ✔️ Task 001: Export LIMA_HOME to the remote limactl invocation
- ✔️ Task 002: Define the Provenancer seam and marker payload type

### ✅ Phase 2: Lima provenance implementation
**Parallel Tasks:**
- ✔️ Task 003: Implement Lima marker I/O + batched read + provider exposure (depends on: 002)

### ✅ Phase 3: Consumer rewire + migration
**Parallel Tasks:**
- ✔️ Task 004: Rewire manage + CLI ownership to provenance (depends on: 003)
- ✔️ Task 005: Rewire the UI roster gate and refresh to provenance (depends on: 003)
- ✔️ Task 006: Registry demotion + idempotent adoption (depends on: 003)

### Phase 4: Verification + documentation
**Parallel Tasks:**
- Task 007: Integration/e2e tests + limactl tolerance guard (depends on: 001, 004, 005, 006)
- Task 008: Document the new ownership model and marker contract (depends on: 004, 005, 006)

### Post-phase Actions
- Run `POST_PHASE.md` validation after each phase; do not advance on unverified claims.

### Execution Summary
- Total Phases: 4
- Total Tasks: 8
