---
id: 13
group: "vm-identity"
dependencies: [7]
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "high"
complexity_score: 8
complexity_notes: "Re-keys a core persisted data structure by (scope,name) with an on-disk migration and rescopes callers across ui/cmd/manage — data-loss risk floor. Added mid-execution when the registry was found to be name-keyed, contradicting the plan's 'no registry schema change' assumption."
skills:
  - go
  - data-migration
---
# Re-key the registry by (scope, name) with an on-disk migration

## Objective
Make the managed-VM registry hold entries keyed by **(connection scope, name)**
instead of bare name, so a VM named `web` on a local profile and a `web` on a
remote profile can coexist — delivering the plan's same-name-across-profiles
promise (Component 6, success criterion 9) that the current `map[string]entry`
index silently prevents by overwriting. This task was added during execution
after the registry was found to be name-keyed (scope stored *in* each entry but
only one entry per name), contradicting the plan's "no registry schema change"
assumption.

## Skills Required
- `go` — refactor a core data structure + its persistence and callers.
- `data-migration` — on-disk v2→v3 bump with a safe read-old-files path.

## Acceptance Criteria
- [ ] `Registry.vms` is re-keyed so two entries with the **same name but different `registry.Scope`** coexist (e.g. a composite key `struct{ scope Scope; name string }`, or `map[Scope]map[string]entry`). `AddScoped` with the same name under two scopes no longer overwrites.
- [ ] The on-disk format changes to represent per-(scope,name) entries (a JSON **array** of entries each carrying name+provider+remoteTarget+config+base, or a scope-nested object — NOT a flat `{name: entry}` object, whose keys can't repeat). `currentVersion` is bumped `2`→`3`.
- [ ] Loading a **pre-migration (v2) file** reads every existing entry as **LocalScope** with its config/base intact, then writes v3 on next save — mirroring task 2's secrets migration and the registry's own prior v2 migration.
- [ ] Every scoped method (`AddScoped`, `IsManagedInScope`, `ConfigInScope`, `BaseInScope`, `ReconcileScoped`, and a new `RemoveScoped`) operates on exactly the `(scope, name)` entry. The unscoped convenience methods (`Add`, `Remove`, `IsManaged`, `Config`, `Base`, `Reconcile`, `IsBase`) remain as **LocalScope** defaults so local-only callers/tests are unchanged.
- [ ] Production callers that act on a **specific fleet VM** are rescoped: the TUI delete path (`internal/ui/model.go` ~887 `reg.Remove(msg.name)` → `RemoveScoped(vmScope, name)` using the deleted VM's scope) and any per-VM `IsManaged`/`Config`/`Base` lookup a **remote** VM flows through must pass that VM's scope (the board's `boardVM` already carries it). Local-only paths may keep the unscoped convenience.
- [ ] Tests: a captured **v2 fixture** loads back intact as LocalScope; a v3 round-trip stores two same-named entries under different scopes and reads each independently; a delete under one scope leaves the same-named entry under the other scope intact. `go test ./internal/registry/... -race` passes.
- [ ] `go build ./...`, `go vet ./...`, and `go test ./... -race` all pass. **No real limactl/ssh target.**

## Technical Requirements
- Files: `internal/registry/registry.go` (`vms map[string]entry` ~93/100, `entry` ~32, `Scope` ~52, `currentVersion` ~83, the migration in `Load`, and every method ~240-369), plus callers in `internal/ui/model.go`, `cmd/sand/*.go`, and `internal/manage/*.go` where a specific VM's scope must be threaded.
- `Scope` is already a comparable struct (Provider + RemoteTarget) → valid map key.

## Input Dependencies
- Task 7: the fleet model + `boardVM` scope tagging (so callers have each VM's scope to pass).

## Output Artifacts
- A (scope,name)-keyed registry (schema v3) that supports same-name coexistence.
- Consumed by: task 12 (same-name coexistence integration test) and task 9 (create records under the selected scope — already uses AddScoped).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- **Keying.** Simplest: `vms map[scopedKey]entry` with `type scopedKey struct{ scope Scope; name string }`. Keep `entry` carrying Provider+RemoteTarget too (redundant with the key but preserves the on-disk shape's self-description) OR derive scope from the key and drop the entry fields — your call, but keep the on-disk JSON self-describing.
- **On-disk shape.** A flat `"vms": {"web": {...}}` object cannot hold two `web`s. Switch to `"vms": [ {"name":"web","provider":"lima","remote_target":"","config":{...},"base":"..."}, {"name":"web","provider":"lima-ssh","remote_target":"user@host:22",...} ]` (a list). Bump `version` to 3. On load: v2 (object) → lift each under LocalScope; v3 (list) → load directly. Write v3 (list) on save.
- **Methods.** Add `RemoveScoped(scope, name)`. Re-express `IsManaged`/`Config`/`Base`/`Reconcile`/`Remove` as `…InScope(LocalScope, …)` conveniences (or keep thin wrappers). `IsBase` scans all entries (any scope) — keep that semantics unless a caller needs scoping. `matches` on entry stays the scope predicate.
- **Callers — be surgical.** Most `reg.Add` in production already flow through a create path that knows its scope (use `AddScoped`). The must-fix production site is the TUI **delete** (`model.go` ~887): a remote VM deleted via unscoped `Remove(name)` would try LocalScope and leave the remote entry dangling — pass the VM's real scope. Audit `reg.IsManaged(`/`reg.Config(`/`reg.Base(` in `internal/ui` (non-test): if the value is a board VM that could be remote, pass its scope; if it's inherently local (e.g. base-image bookkeeping), the LocalScope convenience is fine. Do NOT blindly rewrite test callers that intentionally use the local convenience.
- **Risk floor:** data migration — test the v2→v3 path with a real fixture before done. Do not lose or mis-scope existing entries.
- **Coordinate:** this runs AFTER task 10 and BEFORE tasks 8/9 in the sequential UI order, so tasks 8/9 write against the new scoped registry API.
</details>
