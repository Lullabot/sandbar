---
id: 8
group: "profile-ui"
dependencies: [1, 7]
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "high"
complexity_score: 8
complexity_notes: "New TUI view plus live fleet mutation (tear-down/rebuild of bindings) with an idle gate — lifecycle correctness on top of the fleet model."
skills:
  - go
  - bubbletea
---
# Profile management screen with live fleet mutation and an idle gate

## Objective
Add a TUI **profile management screen** (create / edit / enable-disable / delete)
and make those mutations take effect **live** without restarting sand: a
newly-enabled profile spins up its binding and begins async connect/refresh; a
disabled/deleted profile has its binding torn down and its tiles removed.
Disable/delete/connection-edit are **gated on the profile being idle**. This is
Component 4's management half.

## Skills Required
- `go` — lifecycle/teardown, wiring to the profiles store and fleet.
- `bubbletea` — a new `view` alongside board/form/secrets.

## Acceptance Criteria
- [ ] A new management `view` lists every profile with its type, enabled/error state, and (for RemoteSSH) its target. Reachable via a keybinding consistent with the existing views (board/form/secrets).
- [ ] From it the user can **create** a RemoteSSH profile (a form over host/user/port/identity-path/lima-home), **edit** an existing one, **enable/disable** without deleting, and **delete**. The Local profile shows **no delete verb** but is enable/disable-able and **renameable**.
- [ ] Create/edit **validate target uniqueness** (reuse task 1's validation) — duplicate `user@host:port` or a second Local is rejected with a message.
- [ ] Mutations are **live**: enabling re-runs the fleet builder for that profile and starts its async connect/refresh; disabling/deleting tears down the binding (stops its refresh/heartbeat cmds, removes its tiles) with no restart.
- [ ] **Idle gate:** disable/delete/connection-field-edit is **refused** with a message naming the blocking job if a build/provision or file transfer is in flight on that profile (reuse the existing jobs-in-flight gate that blocks Delete while a VM builds). A **pure rename** (or metadata-only edit that leaves the `user@host:port` target unchanged) is **not** gated and needs no rebuild.
- [ ] Deleting a profile removes only the local profile entry + its runtime binding; it does **not** touch the remote server or its VMs (they stop appearing and reappear if the profile is re-added). The registry entries are left **dormant, not pruned**.
- [ ] `go test ./internal/ui/... -race` passes with a test for: enable→refresh→disable cycle, disable refused while a job is in flight, and a live rename that keeps tiles/last-used intact. Update golden files for the new view. **No real backend.**

## Technical Requirements
- Files: `internal/ui/` (a new view file + wiring in `model.go`'s view switch and key handling), the profiles store (task 1), the fleet model (task 7).
- Reuse the jobs registry (`internal/ui/jobs.go`) idle check already used to gate Delete.

## Input Dependencies
- Task 1: profiles store (CRUD + validation + last-used).
- Task 7: fleet model (to add/remove live members).

## Output Artifacts
- A profile management view + live fleet mutation with idle gating.
- Consumed by: task 11 (tui.md docs), task 12 (management lifecycle integration test).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- **View pattern.** Follow how the existing secrets/form views are modeled
  (a `view` enum value + a sub-model + render + key handling). List profiles from
  the store; a sub-form edits connection fields.
- **Live add.** On enable/create, persist via the store, then build that one
  binding (task 4's helper) and append a `fleetMember` (task 7) in
  `connecting` status, and kick its connect cmd. On disable/delete, find the member
  and drop it (its next tick simply is not armed; in-flight cmds' results are
  ignored because the member is gone — guard the message handler against a missing
  member).
- **Idle gate.** Before disable/delete/connection-edit, check the jobs registry for
  any in-flight job whose scope belongs to that profile; if found, refuse and show
  the blocking job's name. This mirrors the existing Delete-while-building gate —
  find that code and reuse the predicate.
- **Rename is special.** A rename changes only `Profile.Name` (the `id` and the
  target-derived scope are untouched), so it does NOT tear down the binding and is
  NOT idle-gated. Persist the new name; tiles, jobs, last-used all follow via the
  stable id/scope.
- **Dormant registry entries.** Do not call any reconcile/prune on delete/disable —
  there is no live provider to reconcile against and the VMs still exist remotely.
  Re-adding the profile restores the tiles.
- **Golden files.** The new view changes rendered output; regenerate goldens under
  `internal/ui/testdata/` per the existing harness (`teatest_test.go`/`boardshot_test.go`).
</details>
