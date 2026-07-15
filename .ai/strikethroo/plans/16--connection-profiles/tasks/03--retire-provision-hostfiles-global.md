---
id: 3
group: "host-access"
dependencies: []
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Retiring a load-bearing process-global seam by threading an explicit argument through provisioning; wrong wiring touches base images on the wrong host."
skills:
  - go
---
# Retire the `provision.hostFiles` process-global into a per-operation argument

## Objective
Replace the `provision.hostFiles` package-global (which assumes "one sand process
runs exactly one provider") with an **explicit per-operation host-access
argument** threaded from the provider that owns the operation. This is the
provision half of Component 2's dual-global fix and must land before create/reset
can act on a chosen profile.

## Skills Required
- `go` — signature refactor, threading a dependency through call chains, removing package state.

## Acceptance Criteria
- [ ] `provision.hostFiles` (var, `internal/provision/hostaccess.go` ~line 13), its `SetHostFiles` setter (~line 28), and `HostFiles()` getter (~line 36) are removed **or** reduced to no live process-global read: base-image operations (overlay read, version stamp, base lock, partial-instance cleanup during create/reset/cleanup) take the `lima.HostFiles` (or equivalent host-access handle) as an explicit parameter.
- [ ] Every provisioning entry point that used the global now receives the host-access from its caller. All current callers (create/reset/cleanup paths, `cmd/sand/create.go`) are updated to pass the host-access of the single resolved provider so the tree compiles and behaves identically for the one-provider case.
- [ ] The "one sand process runs exactly one provider" comment tied to this global is removed or rewritten to reflect the new explicit-argument model.
- [ ] Existing provision tests pass unchanged in behavior; add/adjust a test proving two **different** fake host-access handles can be used for two provisioning operations in the **same process** without cross-talk (no shared global).
- [ ] `go build ./...` and `go test ./internal/provision/... ./cmd/... -race` pass. **No real limactl/ssh target.**

## Technical Requirements
- Files: `internal/provision/hostaccess.go`, the provision functions that call the global (grep `hostFiles` / `HostFiles(` within `internal/provision`), and callers in `cmd/sand/create.go` (and any reset/cleanup entrypoints).
- The host-access type is `lima.HostFiles` (see `hostaccess.go:13` `lima.LocalFiles()`); a provider exposes its host-access (local vs `SSHHost`) — thread that handle in.
- Do NOT touch `ui.hostFiles` here — that is the UI seam, handled per-tile in task 7.

## Input Dependencies
None (Phase 1). Independent refactor of the provision package.

## Output Artifacts
- Provisioning operations that accept host-access explicitly per call.
- Consumed by: task 9 (create-on-profile passes the **selected profile's** provider host-access when provisioning).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- **Why this is separate from the fleet.** The provision seam is user-serialized
  (create/reset act on exactly one profile at a time), so it does NOT need
  per-tile resolution like the UI seam — an explicit argument is the clean fix.
  Doing it as a standalone refactor keeps the change reviewable and lets it land
  in Phase 1 before the fleet wiring.
- **Approach.** Find the provisioning functions that read `hostFiles`. Add a
  `hf lima.HostFiles` (or a small `HostAccess` interface if that reads cleaner)
  parameter to each, and remove the global read. Where a function calls another
  that needs it, thread it through. At the top of the chain (the create/reset
  entrypoints in `cmd/sand` and any internal orchestrator), obtain the host-access
  from the resolved provider and pass it down.
- **Transitional fallback (allowed only if unavoidable):** the plan permits a
  scoped per-operation swap-and-restore of a global as a transition, but the
  **preferred** end state is the explicit argument. Prefer the argument; only fall
  back to a scoped swap if an argument would require an unreasonably wide change,
  and if so, make the swap `defer`-restored around each provisioning call.
- **Provider host-access accessor.** If the `provider.Provider` interface does not
  already expose its host-access handle, add a minimal accessor (the local
  provider returns `lima.LocalFiles()`, the remote returns its `SSHHost`). Check
  `internal/provider/provider.go`, `local.go`, `remote.go` for an existing seam
  before adding one.
- **Test for no cross-talk.** Construct two fake `HostFiles` recording which one
  was written to, run two provisioning ops with each, and assert each op only
  touched its own handle — proving the global is gone.
</details>
