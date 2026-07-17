---
id: 6
group: "migration"
dependencies: [3]
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "One-time idempotent data migration stamping markers from the legacy registry; wrong adoption claims a VM it shouldn't."
skills:
  - go
---
# Demote the registry to cache and add idempotent adoption/migration

## Objective
Reduce the local registry from ownership truth to a cache + known-targets list +
one-release legacy fallback, and add a one-time idempotent adoption pass that
stamps provenance markers onto already-managed VMs recorded in the existing
`managed-vms.json`, so upgrading controllers don't make managed VMs lose their
tiles.

## Skills Required
- `go` — modify `internal/registry` and the create/refresh entry points that
  trigger adoption.

## Acceptance Criteria
- [ ] An adoption function: for each instance the registry records as managed
  (any scope) that (a) exists in the live `List` and (b) has NO marker, it writes
  the marker from the registry entry via `Provenancer.MarkManaged`. It NEVER
  overwrites an existing marker and is safe to run repeatedly (idempotent).
- [ ] Adoption runs once per target on first contact after upgrade (from the
  create path and/or first board refresh), is cheap on the no-op path, and logs
  adopted instance names.
- [ ] The registry continues to load and is still consulted as the roster/gate
  fallback (implemented in tasks 4/5); registry writes are treated as a cache.
- [ ] A unit test proves idempotency: running adoption twice against a seeded
  legacy registry writes the marker once and leaves it unchanged the second time;
  and proves it skips instances that already have a marker or are not in `List`.
  Verification command: `go test ./internal/registry/... -run Adopt -v` exits 0.
- [ ] `go build ./...` passes.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Files: `internal/registry/registry.go` (or a new `adopt.go`), and the caller
  that invokes adoption (`cmd/sand/create.go` and/or `internal/ui/model.go`).
- `Provenancer` (task 3) for `MarkManaged`/`ProvenanceOf`, and the provider's
  `List` for existence checks.

## Input Dependencies
- Task 3: Lima `Provenancer` (mark + read).
- Soft: task 4 (manage rewire) — adoption complements the fallback path; if task
  4 lands first, reuse its provider plumbing.

## Output Artifacts
- Idempotent adoption + registry-as-cache; consumed by task 7 (adoption test)
  and task 8 (docs of the one-release window).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

**Adoption algorithm.**
```
live := set(provider.List() names)
for name, entry := range reg.managed(anyScope):
    if !live.contains(name): continue           // gone; don't resurrect
    if _, ok, _ := prov.ProvenanceOf(name); ok: continue  // already marked
    prov.MarkManaged(name, Provenance{Base: entry.Base, Config: entry.Config, …})
    log("adopted %s", name)
```
Idempotency comes from the `ProvenanceOf ⇒ skip` guard and never overwriting.
Run at most once per process per target (guard with a sync.Once / a per-target
flag) so refreshes don't re-scan every 5s; the create path is a natural trigger,
and the first board load is the other.

**Registry role.** Keep `registry.Load()` and the scoped lookups intact — tasks
4/5 use them as fallback. Do not delete `IsManagedInScope`/`BaseInScope` yet;
they remain the one-release fallback. Add a clear comment marking the fallback +
adoption as removable after one release, and mirror that in task 8's docs.

**Safety.** Adoption must be conservative: only stamp when the VM is live AND
unmarked AND the registry already claimed it. Never infer management for a VM the
registry did not record. Log every adoption so the migration is auditable.

**Test.** Seed a fake registry with a managed entry, back it with a fake
`Provenancer` whose store starts empty and a `List` that includes the name; run
adopt twice; assert exactly one `MarkManaged` for that name and that a
pre-existing marker is left untouched; assert an entry absent from `List` is
skipped. Name it so `-run Adopt` selects it.

This task is data-migration-sensitive — keep it conservative and well-logged.
</details>
