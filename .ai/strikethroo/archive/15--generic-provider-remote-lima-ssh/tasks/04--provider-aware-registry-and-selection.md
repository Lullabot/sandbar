---
id: 4
group: "migration"
dependencies: [2]
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "high"
skills:
  - go
  - data-migration
complexity_score: 7
complexity_notes: "On-disk schema change with a one-time migration of user data; risk floor applies (data migration must not lose or corrupt managed-VM records)."
---
# Provider-aware managed-VM registry with a versioned migration, and provider selection config

## Objective
Make the managed-VM registry and the shared `internal/manage` bookkeeping aware
of which provider owns each VM, migrate existing on-disk records once to the
local Lima provider, and add the opt-in configuration that selects a provider
(and, for remote, its SSH target) — an unconfigured `sand` still resolves to
local Lima. This closes the correctness hole where a remote host's instances
could be reconciled against or collide with local names.

## Skills Required
`go`, `data-migration` (versioned schema bump with safe, tested migration of user data).

## Acceptance Criteria
- [ ] The registry schema records the owning provider (and, for remote, a stable remote-target identity) per entry; `currentVersion` is bumped from 1.
- [ ] Loading a pre-migration file (no provider field, version 1 or unversioned) rewrites every entry tagged as the local Lima provider and bumps the version, reusing the existing atomic-write and `.corrupt`-quarantine paths — no data loss.
- [ ] `internal/manage` reconcile/record (`Reconcile`, `RecreateBase`, `RecordSuccess`) is provider-scoped so a `List` from one provider never drops or matches another provider's entries.
- [ ] Provider selection config exists (flags and/or environment, plus the minimal remote-target fields: host, user, port, identity, remote `LIMA_HOME`), resolved centrally at the construction point from task 3; absent config = local Lima.
- [ ] **Verification**: a unit test writes a v1 `managed-vms.json` with two entries, loads it through the new code, and asserts the on-disk file is rewritten with both entries provider-tagged `lima` and the version bumped; `go test ./internal/registry ./internal/manage -race` passes; `go test ./... -race` stays green.

## Technical Requirements
- Preserve `registry`'s existing guarantees: atomic temp-file+rename write, `.corrupt` quarantine on unparseable input, secret-free (`CloneToken` stripped), version-too-new refusal.
- Migration must be copy-before-replace safe (never truncate the old data before the new file is durably written).
- Remote-target config must not persist secrets to the registry (identity paths are fine; no private keys or passwords in the index).

## Input Dependencies
- Task 2: the `Provider` interface (for the provider identity/type used in tagging and selection).

## Output Artifacts
- Provider-tagged registry + migration (consumed by task 5's remote flows and task 6's tests).
- Central provider-selection resolution used by task 5 to construct the remote provider.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

Current schema (`internal/registry/registry.go`): `fileSchema{Version int; VMs
map[string]entry}` where `entry{Base string; Config vm.CreateConfig}`, persisted
at `${XDG_DATA_HOME:-~/.local/share}/sandbar/managed-vms.json`. Add a provider
tag to `entry` (e.g. `Provider string` defaulting to `"lima"` local, plus an
optional remote-target id). Bump `currentVersion` to 2. In `LoadFrom`, when
`parsed.Version < 2`, fill the provider tag on every entry as local Lima and
re-`save()` — the save is already atomic, so this is safe; keep the
version-too-new refusal for `> currentVersion`.

Keying: today the map is keyed by VM name in one flat namespace. A remote host
can legitimately reuse a name a local VM has. Scope reconcile by provider (and
remote target) so `manage.Reconcile(reg, live)` only prunes entries belonging to
the provider whose `live` list was passed. Decide whether to key the map by
`(provider,name)` or keep name-keys with a provider field and filter on
reconcile — either works; the filter-on-reconcile approach is the smaller diff
and keeps the file human-readable. `manage.RecordSuccess`/`RecreateBase` become
provider-aware in step.

Selection config: the three entrypoints construct via task 3's central helper.
Add resolution there: default local; if a remote target is configured (flag/env),
construct the remote provider (task 5 supplies the implementation — for this task
the selection plumbing can construct local and leave a clear seam/constructor
signature for remote). Keep the flag/env surface minimal and documented in task
7. Do NOT write private keys or passwords into the registry.

Test against `AGENTS.md`'s `isolateHostState` conventions so migration tests
never touch the developer's real `~/.local/share/sandbar`.
</details>
