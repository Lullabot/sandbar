---
id: 4
group: "fleet"
dependencies: [1]
status: "pending"
created: 2026-07-15
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Replaces the single-provider resolution seam with a fleet builder and removes the env-var surface (hard BC break) — integration point touching several callers and tests."
skills:
  - go
---
# Replace `provider.Resolve` with a profile-driven fleet builder; remove env vars

## Objective
Replace env-var-based single-provider resolution with a **fleet builder** that
reads the profiles store and constructs one `{profile, provider, scope}` binding
per **enabled** profile, and **delete** the `SAND_PROVIDER` / `SAND_REMOTE_*`
environment-variable surface entirely. This is the spine of Component 2.

## Skills Required
- `go` — provider construction, error handling, deleting a config surface, updating tests.

## Acceptance Criteria
- [ ] A new fleet builder (e.g. `provider.BuildFleet(store *profiles.Store) Fleet`) returns a binding per enabled profile: Local → `NewDefault` + `registry.LocalScope`; RemoteSSH → `NewRemoteLima` + the scope derived from its target.
- [ ] A profile whose provider **fails to construct** (bad config/preflight) becomes an **error binding** in the fleet (carrying the profile identity + the error) rather than aborting the whole build — one bad remote never stops the others.
- [ ] A RemoteSSH profile is converted to `provider.TargetConfig` (secret-free) and its scope comes from `TargetConfig.Scope()` (`user@host:port`), reusing the existing derivation (select.go:86) — the fleet does not invent a new scope-key format.
- [ ] `provider.Resolve()` and `resolveTargetConfig()` (env → one provider) and the `SAND_PROVIDER`/`SAND_REMOTE_HOST|USER|PORT|IDENTITY|LIMA_HOME` constants in `internal/provider/select.go` are **removed**. `provider.TargetConfig` is **retained** as the internal secret-free shape.
- [ ] `internal/provider/select_test.go` (tests reading the env constants) is repointed at the fleet builder / `TargetConfig` constructed in-test, or removed where obsolete. `internal/provider/remote_e2e_test.go` (reads `SAND_REMOTE_*` via `provider.SandRemote*Env`, `//go:build limae2e`) is repointed at an in-test `TargetConfig`/profile.
- [ ] `grep -rn "SAND_PROVIDER\|SAND_REMOTE" internal cmd` returns **no** references outside historical plan/changelog text.
- [ ] `go build ./...` and `go test ./internal/provider/... -race` pass. **No unit test requires a real limactl/ssh target** (the `limae2e`-tagged E2E stays behind its tag).

## Technical Requirements
- Files: `internal/provider/select.go` (Resolve ~126, resolveTargetConfig ~94-111, TargetConfig ~58-72, TargetConfig.Scope ~86, SAND_* consts ~21-34), `select_test.go`, `remote_e2e_test.go`.
- Uses `internal/profiles` (task 1) for the profile list.
- The fleet type is a new lightweight struct/slice of bindings; keep it in the `provider` package (or a small new file) so `cmd/sand` and `internal/ui` can consume it.

## Input Dependencies
- Task 1: the `internal/profiles` store and `Profile` model.

## Output Artifacts
- `provider.BuildFleet` (or equivalent) + a `Fleet`/binding type: `{Profile, Provider, Scope, Err}`.
- Consumed by: task 5 (CLI), task 7 (TUI fleet model).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- **Binding shape.** Something like:
  ```go
  type Binding struct {
      Profile profiles.Profile
      Prov    Provider        // nil when Err != nil
      Scope   registry.Scope
      Err     error           // non-nil => error binding
  }
  type Fleet []Binding
  func BuildFleet(store *profiles.Store) Fleet { ... }
  ```
  Only **enabled** profiles produce bindings. For each, construct the provider;
  on failure, append a binding with `Err` set and `Prov` nil (do not return early).
- **Local vs remote.** Local → `NewDefault(...)` with `registry.LocalScope`.
  RemoteSSH → build a `TargetConfig{Host,User,Port,IdentityPath,LimaHome}` from the
  profile, then `NewRemoteLima(cfg)` and `cfg.Scope()` for the registry scope.
- **Preflight belongs to the caller, not the builder.** `BuildFleet` should
  construct providers but NOT synchronously preflight remotes (task 7 runs preflight
  async per profile in the TUI; task 5 preflights the one CLI-selected profile).
  Constructing `NewRemoteLima` must not itself do a blocking SSH round-trip; if it
  currently does, ensure construction is cheap and defer the round-trip to
  `Preflight()`.
- **Env removal is a sanctioned hard BC break** (plan Implementation Risks): the
  env surface is unreleased. Remove the constants and their doc references in this
  change so nothing dangles. Docs are updated in task 11, but delete any inline
  doc comments in select.go here.
- **Tests.** Replace env-var-driven `select_test.go` cases with fleet-builder
  cases: an all-local store yields one local binding; a store with a bad remote
  yields a local binding + an error binding; a store with two enabled profiles
  yields two bindings. Use `providerfake` where a real provider would be needed.
- Do not wire the fleet into the TUI here — task 7 owns that. This task stops at a
  compiling, tested builder plus the CLI still calling something sensible (leave
  `cmd/sand` compiling; task 5 finishes the CLI conversion — if removing `Resolve`
  breaks `create.go`/`shell.go`/`main.go` compilation, add a minimal temporary
  shim `resolveSingle(store, profileName)` used by those three until task 5, or
  coordinate by having this task update them to pick the enabled Local/last-used
  binding. Prefer the latter if small.)
</details>
