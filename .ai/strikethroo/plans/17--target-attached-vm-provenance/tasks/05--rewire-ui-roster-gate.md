---
id: 5
group: "consumer-rewire"
dependencies: [3]
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "medium"
complexity_score: 6
complexity_notes: "Bubble Tea async integration: fetch provenance inside the existing refresh closure and swap the roster gate without blocking the UI."
skills:
  - go
---
# Rewire the UI roster gate and refresh to provenance

## Objective
Change the board's managed-VM roster gate and base-image rendering to use
provider provenance (marker first, registry fallback for one release, plus active
provision jobs), fetching the provenance map once per refresh inside the existing
async `refreshCmd` closure so the UI thread never blocks.

## Skills Required
- `go` — modify `internal/ui`.

## Acceptance Criteria
- [ ] `boardVMs` roster gate (`board.go:142`) becomes: tile iff the instance
  carries a provenance marker OR (legacy fallback) `reg.IsManagedInScope` OR it
  has an active provision job.
- [ ] `stopAllTargets` (`board.go:494`) and `traitsOf` base rendering
  (`board.go:1027`) read from the same provenance map (base from the marker,
  registry fallback).
- [ ] The provenance map is fetched inside `refreshCmd`
  (`internal/ui/commands.go`) via the batched read, alongside the existing
  `List`/`HostResources` calls, and delivered through the existing
  `vmsLoadedMsg` path — NOT on the Bubble Tea Update goroutine.
- [ ] Per-refresh host round-trips for provenance are bounded (one batched call),
  not one per VM.
- [ ] `go build ./...` and `go test ./internal/ui/...` pass. Verification
  command: `go test ./internal/ui/... -run 'Board|Roster|Refresh' -v` exits 0.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Files: `internal/ui/board.go` (`boardVMs` @:142, `stopAllTargets` @:494,
  `traitsOf` @:1027), `internal/ui/commands.go` (`refreshCmd`),
  `internal/ui/model.go` (registry load @~:339; carry the provenance map on the
  model / in `vmsLoadedMsg`).
- The batched `Provenancer.Provenance(ctx)` from task 3.

## Input Dependencies
- Task 3: Lima `Provenancer` (batched read).

## Output Artifacts
- Board roster + base rendering sourced from provenance; consumed by task 7
  (tests) and task 8 (docs).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

`refreshCmd` (`internal/ui/commands.go`) already runs `prov.List()`, per-VM disk
sampling, and a blocking `prov.HostResources()` ssh round trip inside a spawned
`tea.Cmd` closure (off the Update goroutine) and returns a `vmsLoadedMsg`. Add
`prov.Provenance(ctx)` there and carry the resulting `map[string]Provenance` on
`vmsLoadedMsg`. This keeps the extra round trip off the UI thread and bounded to
one call.

`boardVMs` (`board.go:142`) currently: `if m.reg.IsManagedInScope(v.Name, mem.scope)
{ on[v.Name] = true }` (plus active provision jobs). New gate:
```
managed := prov[v.Name].present   // marker exists
if !managed { managed = m.reg.IsManagedInScope(v.Name, mem.scope) } // legacy fallback (remove after one release)
if managed || hasActiveJob(v.Name) { on[v.Name] = true }
```
Thread the provenance map (from the model, populated by `vmsLoadedMsg`) into
`boardVMs`/`traitsOf`/`stopAllTargets`. For base rendering in `traitsOf`
(`board.go:1027`), prefer `prov[name].Base`, fall back to `reg.BaseInScope`.

Keep `isBaseImage` filtering and active-provision-job handling unchanged. Do not
change the 5s refresh cadence. Preserve `Scope` for grouping — it is no longer
the ownership discriminator but still keys grouping/known-targets.

If the model is value-passed (it is a Bubble Tea `Model`), store the provenance
map as a field set when handling `vmsLoadedMsg`, the same way `m.reg` and the VM
list are held. Add/adjust a board test that asserts a VM with a marker (fake
provenance map) gets a tile while an unmarked, non-job VM does not, and that the
legacy registry fallback still lights a tile.

Do NOT modify `manage`/CLI here (task 4). Do NOT implement the batched read here
(task 3).
</details>
