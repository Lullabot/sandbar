---
id: 7
group: "fleet"
dependencies: [4, 6]
status: "pending"
created: 2026-07-15
model: "opus"
effort: "xhigh"
complexity_score: 9
complexity_notes: "Architecture-defining, concurrency-heavy restructure of the TUI model from one provider to a fleet of async per-profile sub-states, plus the per-tile UI host-access seam and errored self-heal with backoff. The correctness core of the plan."
skills:
  - go
  - bubbletea
---
# Promote the TUI model to an async per-profile fleet

## Objective
Restructure the TUI model from a single `p provider.Provider` + `scope` to a
**fleet of per-profile sub-states** whose VM lists aggregate into one board, each
refreshing independently and asynchronously so a slow/unreachable remote never
blocks the UI. Retire the `ui.hostFiles` global into a per-tile seam and add
errored-profile self-heal with backoff. This is Component 3 and the heart of the
plan.

## Skills Required
- `go` — concurrency via `tea.Cmd`, state modeling.
- `bubbletea` — Charm v2 model-by-value discipline, message routing, commands.

## Acceptance Criteria
- [ ] The model holds a **fleet** of per-profile sub-states, each with: its provider, scope, last-known VM list, host-capacity sample, connection status (`connecting`/`connected`/`error`), last error, per-profile host-access seam, and per-profile backoff state. The single `m.p`/`m.scope` fields are **removed** (so any missed single-provider path fails to compile).
- [ ] The board roster is the **union** of every connected profile's managed VMs. Each profile lists/refreshes on its **own** `tea.Cmd`; the local list renders immediately while remotes merge in as they land.
- [ ] Async result messages carry the **profile identity** so the model routes each result to the right sub-state; the existing `vmsLoadedMsg` shape is **extended**, not replaced by a parallel mechanism.
- [ ] Startup launches **without** waiting on any remote preflight: each profile's preflight runs inside its per-profile connect/refresh cmd; a failure/timeout marks that profile an **error binding** (surfaced by task 10's status bar), never a blocked or aborted startup. Verified by a fake provider whose preflight blocks — the board still launches and stays interactive.
- [ ] `ui.hostFiles` (`internal/ui/diskusage.go` ~14, `SetHostFiles` ~22) is retired: per-tile disk/up-since sampling resolves the host-access **by the VM's owning profile** (each sub-state carries its own seam), so a remote VM samples on its remote host and a local VM on the local FS — in the **same** render, no global.
- [ ] Reconcile, heartbeats, and disk sampling run **per profile**: each sub-state reconciles only its own scope (`ReconcileScoped`), heartbeats against its own provider, samples on its own host. A disabled/errored profile is **not** reconciled (its entries stay dormant, not pruned).
- [ ] An **errored profile self-heals with backoff**: its refresh keeps retrying, backing off (5s → 30s → 60s, capped) instead of every `refreshInterval`; a successful list resets to normal cadence. Backoff is per-sub-state, so a healthy profile is unaffected.
- [ ] When **every** enabled profile is still connecting or errored and there are no tiles, the board shows a "connecting to N profiles…" state, not the bare "no VMs — press enter to create" invitation.
- [ ] `go test ./internal/ui/... -race` passes, including a new test where one profile blocks indefinitely while the board stays interactive and another profile fails-then-succeeds and its tiles appear automatically. **No real backend** (use `providerfake`).

## Technical Requirements
- Files: `internal/ui/model.go` (fields `p` ~101, `scope` ~111, `connecting`/`connectErr` ~119-120, `New` ~321, reconcile/refresh handlers), `internal/ui/commands.go` (`listCmd` ~83), `internal/ui/refresh.go` (`refreshInterval`, `tickRefresh`), `internal/ui/heartbeat.go`, `internal/ui/connecting.go`, `internal/ui/diskusage.go`.
- Uses `provider.BuildFleet`/bindings (task 4) and the scope-qualified keys (task 6).
- Charm v2 stack: `charm.land/bubbletea/v2`, model-by-value.

## Input Dependencies
- Task 4: fleet builder / bindings (the model is constructed from a `Fleet`).
- Task 6: `(scope, name)` keys and scope-safe prune (required before multiple scopes are live).

## Output Artifacts
- A fleet TUI model with per-profile async aggregation, per-tile host-access, and self-heal backoff.
- Consumed by: task 8 (management screen mutates the live fleet), task 9 (create targets a sub-state), task 10 (tile labels + per-profile status bands), task 12 (integration tests).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- **Sub-state shape.** Introduce e.g. `type fleetMember struct { profile profiles.Profile; prov provider.Provider; scope registry.Scope; vms []vm.VM; host hostSample; status connStatus; lastErr error; hostFiles lima.HostFiles; backoff backoffState }` and hold `members []fleetMember` (or keyed by profile ID) on the model. Remove `m.p`/`m.scope` — let the compiler find every reader.
- **Message routing.** Add the profile ID (or scope) to `vmsLoadedMsg` and to the host-sample/heartbeat messages, so `Update` dispatches each to the right member. `listCmd(p)` becomes `listCmd(member)` capturing the member's provider AND host-access, returning a message tagged with the member's identity.
- **Per-tile host-access.** Today `ui.hostFiles` is a global read during tile disk/up-since sampling. Move the sampling into `listCmd` per member (it already samples off the Bubble Tea goroutine — see tile.go:440, header.go:159) using that member's `hostFiles`. The tile then displays the already-sampled value; no global. Delete `diskusage.go`'s global + setter.
- **Async preflight.** In each member's connect cmd, run `prov.Preflight()`; on error set `status=error, lastErr`. Never call preflight on the Update goroutine. The local member's preflight is effectively instant.
- **Backoff.** Give each member a `backoffState` (current delay, capped). The refresh scheduler arms the next tick per member at `normal` when connected, or at the member's backoff when errored, doubling (5→30→60) on repeated failure and resetting on success. Reuse the `shouldTick` idle gate (heartbeat.go:652) so a backgrounded board still does not busy-poll. Watch the existing `m.refreshing` single-loop guard (refresh.go) — you now need per-member arming without spawning duplicate ticks.
- **connecting.go generalization.** The old full-screen `connecting`/`connectErr`
  interstitial (model.go:119-120, connecting.go) becomes: a member with no tiles
  contributes nothing and shows its state in the status bar (task 10). Keep ONE
  special full-surface hint for the all-members-unconnected/errored empty case, so
  an in-flight fleet is not misrepresented as an empty board (the same trap the
  `vmsLoaded` guard avoids today).
- **Reconcile per member.** Replace the single `manage.Reconcile(m.reg, live, m.scope)`
  with one call per connected member using that member's live list and scope. Skip
  disabled/errored members entirely (dormant, not pruned).
- **Model-by-value.** This is Charm v2 — return updated copies; do not mutate
  shared pointers across goroutines. Sampling/preflight happen inside `tea.Cmd`
  closures, results come back as messages.
- **This is the highest-risk task.** Keep the single-provider behavior bit-identical
  for a one-profile store (the zero-config path) — task 12's parity test will check
  it. Lean on `providerfake` (func-field double) to drive multi-member scenarios.
</details>
