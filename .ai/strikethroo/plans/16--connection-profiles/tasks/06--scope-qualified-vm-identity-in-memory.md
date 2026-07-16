---
id: 6
group: "vm-identity"
dependencies: [2]
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Correctness-sensitive: the HIGH data-loss risk (reconcile pruning the wrong VM's secrets) lives here. Touches several per-VM stores plus both prune sites."
skills:
  - go
  - bubbletea
---
# Scope-qualify the TUI's in-memory per-VM stores and both prune sites

## Objective
Make every in-memory per-VM key `(scope, name)` instead of bare `name`, so the
same VM name can exist under two profiles without sharing state — and, critically,
so reconcile and explicit-delete never prune the wrong VM's secrets/heartbeat/job.
This is the in-memory half of Component 6 (task 2 did the on-disk half).

## Skills Required
- `go` — refactor map keys / handles across the UI package.
- `bubbletea` — model state (heartbeat/jobs/focus) discipline.

## Acceptance Criteria
- [ ] The heartbeat registry (`internal/ui/heartbeat.go`, `heartbeatRegistry.beats map[string]*heartbeat` ~390-392), the jobs registry (`internal/ui/jobs.go`, `jobKey{vm, kind}` ~76-79), and the board's focus ring (`model.focusName` ~model.go:145) all key by a composite `(scope, name)` (or an equivalent stable per-VM handle), not a bare name.
- [ ] Both bare-name prune sites are scope-qualified: (a) the **reconcile** path in `internal/ui/model.go` (~767-778, "a dropped VM's HOST SECRETS ARE DELETED") prunes only its own scope's secrets/heartbeats/jobs; (b) the **explicit-delete** path (~806+) prunes only the targeted VM's `(scope, name)` state.
- [ ] Every secrets call updated in task 2 to pass `registry.LocalScope` (its `TODO(task 6)` markers) now passes the VM's **real** scope.
- [ ] Because the model still holds a single scope at this point, behavior is **identical** to before for the one-profile case — this is a preparatory refactor that compiles and passes the existing suite; task 7 introduces multiple scopes.
- [ ] A regression test proves the fix: with two entries under different scopes sharing a name, dropping one via reconcile/delete leaves the other's secrets, heartbeat, and job **intact**. (Can be written at the store/handler level with two scopes even before the full fleet model exists.)
- [ ] `go test ./internal/ui/... -race` passes and existing golden files are unchanged (identity keys are internal, not rendered). **No real backend.**

## Technical Requirements
- Files: `internal/ui/heartbeat.go`, `internal/ui/jobs.go`, `internal/ui/model.go` (focusName, both prune paths), plus any helper that looks up a VM by name.
- The scope type is `registry.Scope`; the secrets API now takes it (task 2).

## Input Dependencies
- Task 2: the secrets store now accepts a connection scope on every call.

## Output Artifacts
- All in-memory per-VM state keyed by `(scope, name)`; both prune sites scope-safe.
- Consumed by: task 7 (the fleet model relies on these keys being scope-safe before enabling multiple scopes).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- **Do it with the single scope first.** The model currently has one `m.scope`
  (model.go:111). Thread that scope into every per-VM key and every prune call.
  This changes the keys' *type* without changing runtime behavior (still one scope),
  so all existing tests/goldens must stay green. Task 7 then supplies different
  scopes per profile and the composite keys start doing real work.
- **Composite key options.** Either a small `struct{ Scope registry.Scope; Name string }`
  used as a map key (Scope is comparable) or a stable string handle
  `scope.String()+"\x00"+name`. Prefer the struct for type safety. `focusName`
  becomes a `focusVM` handle of the same composite (rename the field to avoid the
  "name is enough" assumption reappearing).
- **The HIGH-severity bug.** model.go:767-778 loops over `dropped` names from
  `manage.Reconcile(m.reg, live, m.scope)` and calls `m.sec.Remove(name)`. After
  this task it must be `m.sec.Remove(m.scope, name)` — and, once task 7 lands,
  each profile reconciles only its own scope. The explicit-delete path (~806+)
  similarly must remove `(scope, name)`, never a bare name that could match another
  profile's VM.
- **Heartbeat/jobs.** Update `heartbeatRegistry` to key on the composite; update
  `jobKey` to include the scope (rename to make it explicit, e.g.
  `jobKey{scope, vm, kind}`). Update all constructors/lookups. `shouldTick`
  (heartbeat.go:652) logic is unaffected.
- **Test.** Even without the full fleet, you can construct the registry + secrets +
  heartbeat/jobs maps with two scopes, insert same-named entries, run the
  reconcile/delete prune for one scope, and assert the other survives. This is the
  regression guarding the plan's HIGH data-loss risk — do not skip it.
</details>
