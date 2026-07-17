---
id: 4
group: "consumer-rewire"
dependencies: [3]
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Rewires correctness gates (recreate, stop-all, shell routing) from the registry to provenance; recreate mis-gating corrupts VMs."
skills:
  - go
---
# Rewire manage + CLI ownership to provenance (with one-release registry fallback)

## Objective
Switch the correctness-critical ownership decisions in the `manage` layer and the
CLI from the controller registry to provider provenance, writing the marker at
create time and keeping the registry as a one-release fallback. This covers
`RecreateBase` (reset gate + base name), `RecordSuccess` (now writes the marker),
`Reconcile`, and multi-profile `sand shell` owner routing.

## Skills Required
- `go` — modify `internal/manage` and `cmd/sand`.

## Acceptance Criteria
- [ ] `manage.RecordSuccess` writes the provenance marker via
  `Provenancer.MarkManaged` (authoritative) and updates the registry only as a
  cache. The marker carries the base name and `CreateConfig`.
- [ ] `manage.RecreateBase` resolves managed-status AND the base to clone from
  provenance first (marker), falling back to `reg.BaseInScope` for one release;
  an instance with no marker and no registry entry is refused (returns not-ok),
  preserving today's "unmanaged ⇒ not recreatable" gate.
- [ ] Multi-profile `sand shell` owner routing (`cmd/sand/shell.go`) resolves the
  owning profile from provenance first, registry fallback second.
- [ ] `manage.Reconcile` semantics preserved: entries/markers for VMs no longer
  listed are not treated as managed (instance-dir markers vanish on delete, so
  reconcile against the live `List` still holds).
- [ ] `go build ./...` and `go test ./internal/manage/... ./cmd/sand/...` pass.
  Verification command: `go test ./internal/manage/... -v` exits 0 with the
  updated `RecreateBase`/`RecordSuccess` tests green.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Files: `internal/manage/manage.go` (`RecreateBase` @~:42, `RecordSuccess`
  @~:57, `Reconcile` @~:29), `cmd/sand/shell.go` (`IsManagedInScope` resolver
  @~:49,:310), `cmd/sand/create.go` (create path that calls `RecordSuccess`).
- The `Provenancer` from task 3, reachable from the selected provider.
- Preserve existing function signatures where callers outside this task depend
  on them; thread a `Provenancer` (or the provider) into `manage` as needed.

## Input Dependencies
- Task 3: Lima `Provenancer` implementation (mark/read).

## Output Artifacts
- `manage`/CLI ownership resolved from provenance with registry fallback;
  markers written on successful create. Consumed by task 7 (tests) and task 8
  (docs).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

The full scoped call-site inventory to touch here (UI sites are task 5):

| Call | Site | Change |
| --- | --- | --- |
| `AddScoped` | `manage.go:57` `RecordSuccess` | also `MarkManaged` (authoritative); registry write becomes cache |
| `BaseInScope` | `manage.go:42` `RecreateBase` | prefer marker's base + managed check; registry fallback |
| `ReconcileScoped` | `manage.go:29` `Reconcile` | keep; ensure it doesn't prune based on a stale registry when markers are truth |
| `IsManagedInScope` | `shell.go:49,310` | resolve owner from provenance, registry fallback |

**RecordSuccess.** After a successful create, call
`prov.MarkManaged(ctx, name, Provenance{Base: cfg.BaseName, Config: cfg, …})`.
Keep `reg.AddScoped(...)` so the cache stays warm and the one-release fallback
works, but the marker is the source of truth. If `MarkManaged` fails, surface it
(a VM with no marker is invisible cross-controller) — do not silently swallow.

**RecreateBase.** New order: read `prov.ProvenanceOf(name)`; if found, managed =
true and base = `p.Base`; if not found, fall back to `reg.BaseInScope(name,
scope)`; if neither, return `"", false` (refuse). This keeps the correctness gate
intact — recreate must never run against an unmanaged/unknown VM, and must clone
from the correct recorded base.

**shell routing.** Where multiple profiles are enabled and the resolver picks
which profile owns NAME, consult each candidate provider's provenance first
(single `ProvenanceOf` per candidate), registry second. Preserve the existing
single-profile fast path.

**Reconcile.** Because instance-dir markers disappear with the instance,
"managed" already reflects live existence; ensure `Reconcile` (which prunes
registry entries for VMs gone from `List`) still runs for the cache but does not
now incorrectly resurrect or drop provenance. Do not make reconcile delete
markers (deletion is limactl's job).

**Fallback window.** Gate the registry fallback behind a clearly-commented
"legacy, remove after one release" path so task 8's docs can reference it and a
follow-up can delete it.

Keep changes surgical; the UI rewire (board/refresh) is task 5 and must not be
done here. Update or add `manage` unit tests for the new gate behavior (managed
via marker, refused when neither marker nor registry).
</details>
