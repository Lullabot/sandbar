---
id: 2
group: "store-conversion"
dependencies: [1]
status: "pending"
created: 2026-07-17
model: "opus"
effort: "xhigh"
complexity_score: 9
complexity_notes: "Concurrency + data-sensitive; establishes the non-saving reload + delta-merge + reconcile-pruning pattern the other stores copy; subtle re-entrancy and pruning-basis correctness."
skills:
  - go
  - concurrency
---
# Registry store: locked reload-merge + reconcile correctness

## Objective

Convert every `internal/registry/registry.go` mutation from a blind whole-file
overwrite into a lock-protected read-modify-write against the *current on-disk*
index, and fix `ReconcileScoped` (and its `manage.Reconcile` caller) so it
prunes only entries the caller actually observed absent — never entries a
concurrent process added after the caller's snapshot. This task also establishes
the **non-saving reload parse** pattern that Tasks 3 and 4 reuse.

## Skills Required

- `go` — refactoring the store write path, factoring a pure parse helper.
- `concurrency` — cross-process lock correctness, re-entrancy avoidance,
  merge semantics.

## Acceptance Criteria

- [ ] A **non-saving, side-effect-free parse** helper is factored from
      `LoadFrom` (registry.go): it decodes + migrates *in memory* only. It must
      NOT call `save()` (today `LoadFrom` persists a migrated v3 index at
      ~registry.go:297-305), must NOT re-acquire the file lock, and must NOT
      rename to `.corrupt`. Migration-persistence and corrupt-quarantine remain
      on the process-start `Load()` path.
- [ ] `AddScoped`, `RemoveScoped`, and `ReconcileScoped` each: acquire the
      registry lock (`<datadir>/managed-vms.json.lock`) via `internal/filelock`,
      reload the on-disk index via the non-saving parse, apply only their own
      delta, write via the existing atomic temp+rename `save()` body, release the
      lock, then refresh the in-memory map from the merged result.
- [ ] Delta semantics: `AddScoped` inserts/overwrites exactly its one
      `(scope, name)` key; `RemoveScoped` deletes exactly its one key.
- [ ] `ReconcileScoped` pruning basis is corrected: it prunes only
      `known ∩ absent-from-present`, where `known` is the set of `(scope,name)`
      keys the caller last observed. Its signature gains the caller's known-set
      (e.g. `ReconcileScoped(scope Scope, present map[string]bool, known map[string]bool)`
      or equivalent). Entries present in the reloaded on-disk set but NOT in
      `known` are never pruned.
- [ ] `manage.Reconcile` (internal/manage/manage.go:24-30) is updated to capture
      the caller's known key set (the scope's keys as it last observed them) and
      pass it to `ReconcileScoped`. All call sites (`internal/ui/model.go:959`,
      `cmd/sand/create.go:218`) compile and behave correctly.
- [ ] Best-effort posture: when `filelock.Acquire` reports it could not lock, the
      mutation still completes (reload+merge+write, unserialized) and emits a
      visible note through the registry's existing warning channel.
- [ ] The empty-path in-memory no-op (`r.path == ""`), stable-sort /
      byte-identical output, and `.corrupt` quarantine on the `Load()` path are
      all preserved.
- [ ] The `save()` doc comment at ~registry.go:496-502 (currently about unique
      temp-file names / "two TUI processes sharing a data dir don't race on a
      shared name") is corrected to state writes are now lock-protected
      read-modify-writes.
- [ ] Tests (`go test ./internal/registry/... ./internal/manage/...`):
      - two `Registry` instances on one temp file: `AddScoped(A)` on one and
        `AddScoped(B)`/`RemoveScoped(C)` on the other — final file contains both
        effects regardless of order;
      - `ReconcileScoped` does NOT prune a key that exists on disk but was absent
        from the caller's `known` set (simulated concurrent add);
      - re-entrancy / no-reload-write: a mutation against a legacy-schema file
        that would migrate completes without deadlock and the reload performs no
        `save()` (only the final intended write hits disk);
      - format-stability: a single-process mutation produces byte-identical
        output to the pre-change code for the same logical state.
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` pass.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Reference points (verify current line numbers before editing): `save()` ~503,
  `AddScoped` ~416, `RemoveScoped` ~437, `ReconcileScoped` ~466 (prune+save at
  477-480), `LoadFrom` ~212, migration save ~297-305.
- Reuse Task 1's `internal/filelock.Acquire`. Acquire once at the mutation
  boundary; the inner `save()` body and the reload parse must never re-acquire.
- Keep schema/version handling defined once (shared between `Load()` and the
  new non-saving parse).

## Input Dependencies

- Task 1: `internal/filelock.Acquire`.

## Output Artifacts

- Converted `internal/registry/registry.go` (locked reload-merge; corrected
  `ReconcileScoped`), updated `internal/manage/manage.go`, and registry/manage
  tests. The non-saving-parse pattern documented here is the template for Tasks
  3 and 4.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

1. **Factor the non-saving parse.** Split `LoadFrom` into: (a) a pure
   `parseIndex(bytes) (indexMap, error)` that decodes + migrates in memory and
   does no I/O side effects; and (b) the existing `Load()`/`LoadFrom` wrapper
   that, on the process-start path, additionally persists a migrated index and
   quarantines a corrupt file. The locked reload calls only (a) after reading the
   file bytes fresh.

2. **Wrap each mutation.** Pattern (pseudo-Go):
   ```go
   func (r *Registry) AddScoped(cfg vm.CreateConfig, scope Scope) error {
       if r.path == "" { /* in-memory: existing behavior */ }
       release, err := filelock.Acquire(r.path + ".lock")
       if err != nil { r.warnf("proceeding without lock: %v", err) }
       defer release()
       cur, err := r.reloadUnlocked() // read file + parseIndex; empty set if ENOENT
       if err != nil { return err }
       cur.put(key(scope, cfg.Name), entryFrom(cfg)) // the one delta
       if err := r.saveMap(cur); err != nil { return err } // existing atomic body
       r.vms = cur // refresh in-memory
       return nil
   }
   ```
   `RemoveScoped` deletes one key; both always write (matching today).

3. **Reconcile.** Change to prune `known ∩ absent`:
   ```go
   func (r *Registry) ReconcileScoped(scope Scope, present, known map[string]bool) ([]string, error) {
       // ... acquire lock, reload cur ...
       var dropped []string
       for name := range known {          // only keys the caller knew about
           if present[name] { continue }  // still live -> keep
           k := key(scope, name)
           if _, ok := cur[k]; ok { delete(cur, k); dropped = append(dropped, name) }
       }
       if len(dropped) == 0 { return nil, nil } // preserve save-only-when-pruning
       return dropped, r.saveMap(cur)
   }
   ```
   In `manage.Reconcile`, build `known` from the registry's current view of the
   scope (the names it would have listed) alongside the `present` map derived
   from `p.List()`. This keeps the headline scenario safe: a VM another process
   just added is not in `known`, so it is never pruned.

4. **Best-effort note.** Mirror `baselock`'s posture: on lock failure, warn once
   and continue. Do not fail the mutation.

5. **Tests.** Put concurrency tests in `registry_test.go`. Drive two `*Registry`
   values at the same temp path. For the re-entrancy test, seed a legacy (v1/v2)
   file and assert the mutation succeeds and that a spy on the save path shows a
   single write.

Test philosophy — write a few tests, mostly integration: cover the merge logic,
the reconcile pruning basis, and the re-entrancy path (this store's real custom
logic). Do not add per-getter unit tests or re-test the YAML/JSON libraries.
</details>
