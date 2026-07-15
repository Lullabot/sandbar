---
id: 2
group: "vm-identity"
dependencies: []
status: "pending"
created: 2026-07-15
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "On-disk schema migration of an existing store — data-loss risk floor. Must not conflate the pre-existing directory scope with the new connection scope."
skills:
  - go
  - data-migration
---
# Add a connection-scope dimension to the secrets store (schema migration)

## Objective
Migrate `internal/secrets` so a VM's secrets are keyed by **connection scope +
name** instead of bare name, so a `web` on local and a `web` on a remote keep
independent secrets. This is the storage half of Component 6 and the mitigation
for the plan's HIGH-severity data-loss risk (reconcile deleting the wrong VM's
secrets).

## Skills Required
- `go` — struct/schema change, backward-compatible load path.
- `data-migration` — on-disk version bump with a safe read-old-files path.

## Acceptance Criteria
- [ ] The in-memory `Store` and the on-disk `fileSchema` key VMs by a **connection scope** (`registry.Scope{Provider, RemoteTarget}`) **plus** the VM name, not by bare name alone.
- [ ] The on-disk `schemaVersion` is bumped from `2` to `3`.
- [ ] Loading a **pre-migration (v2) file** reads every existing VM as **local-scoped** (`registry.LocalScope`) with its secrets intact, mirroring how the registry's v2 migration stamped old entries local. The new version is written on the next save.
- [ ] The **existing home-relative directory scope** (`ValidScope`, secrets.go ~87-103) is left completely intact and is NOT the dimension being added — the new connection scope is orthogonal and sits **above** the per-VM keying. Code and naming keep the two distinct.
- [ ] All public methods that took a bare VM name (`Set`, `Get`, `Remove`, list, etc.) gain a connection-scope parameter (or an equivalent `(scope, name)` handle). All in-tree callers are updated to compile (they can pass `registry.LocalScope` for now; task 6 threads the real scope).
- [ ] A round-trip test loads a captured **v2 fixture file** and asserts the secrets read back intact as local-scoped; another test writes v3 and reads it back with two same-named VMs under different scopes holding distinct secrets.
- [ ] `go test ./internal/secrets/... -race` passes and `go build ./...` succeeds. **No real limactl/ssh target used.**

## Technical Requirements
- Files: `internal/secrets/secrets.go` (Store `vms` map ~line 113; `fileSchema.VMs` ~40-44; `schemaVersion` ~line 37; `ValidScope` ~87-103).
- Import `internal/registry` for `Scope` and `LocalScope` (confirm no import cycle; registry does not import secrets).
- Keep the on-disk representation of the scope stable and human-readable (Provider + RemoteTarget), consistent with how the registry serializes a scope.

## Input Dependencies
None (Phase 1). Uses `registry.Scope`/`registry.LocalScope`, already on `main`.

## Output Artifacts
- A connection-scope-aware secrets store (schema v3) with a safe v2→v3 read path.
- Consumed by: task 6 (scope-qualified in-memory identity threads the real scope into every call and into the reconcile/delete prune sites).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- **Two different "scopes" — do not conflate.** The store already has a
  *directory scope* (`ValidScope`) that namespaces secret **paths within a single
  VM** (e.g. `$HOME`-relative locations). This task adds a *connection scope* that
  namespaces **which host the VM lives on**. The connection scope wraps the whole
  per-VM entry; the directory scope stays exactly where it is inside each entry.
  Name the new things `connScope`/`ConnectionScope` or pass a `registry.Scope`
  directly — never reuse the word "scope" bare in a way that collides with
  `ValidScope`.
- **Keying.** Today `vms map[string]map[string]map[string]string` is
  `name → dirScope → key → value` (confirm exact nesting in secrets.go:113). Add
  the connection scope as the outermost dimension, e.g.
  `map[registry.Scope]map[string]<existing inner>` keyed by `(connScope, name)`.
  A `registry.Scope` is a comparable struct (Provider + RemoteTarget strings), so
  it is a valid map key.
- **Migration.** In `Load`, branch on the file's `Version`. For v2, unmarshal into
  the old shape and lift every VM under `registry.LocalScope`. For v3, unmarshal
  directly. Write `Version: 3` on every save. Capture a real v2 file as a testdata
  fixture (you can generate one by checking out the current serializer behavior or
  hand-writing the small YAML/JSON).
- **Caller updates.** Grep for every call into the secrets package
  (`grep -rn "\.sec\.\|secrets\." internal cmd`). The main consumers are in
  `internal/ui/model.go` (the reconcile prune path ~767-778 and the explicit
  delete path ~806+) and wherever secrets are set/read. For THIS task, make them
  compile by passing `registry.LocalScope`; task 6 replaces that with the VM's
  real scope. Add a `// TODO(task 6): real scope` marker at each call site so
  task 6 can find them.
- **Risk floor:** this is a data migration — a botched read path silently loses
  host secrets. Test the v2→v3 path explicitly with a fixture before considering
  the task done.
</details>
