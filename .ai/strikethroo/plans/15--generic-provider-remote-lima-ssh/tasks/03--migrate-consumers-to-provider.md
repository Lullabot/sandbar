---
id: 3
group: "migration"
dependencies: [2]
status: "pending"
created: 2026-07-15
model: "sonnet"
effort: "high"
skills:
  - go
  - refactoring
complexity_score: 7
complexity_notes: "Cross-package migration of every consumer off the concrete Lima pointer, preserving four existing narrow test seams and the three-entrypoint no-drift rule."
---
# Migrate all consumers from `*lima.Client` to `provider.Provider`

## Objective
Flip every consumer — the three `cmd/sand` entrypoints, `internal/ui`,
`internal/browse`, and the free functions in `internal/provision` — from holding
the concrete `*lima.Client` to depending on the `provider.Provider` interface,
and centralise provider construction so the headless-create, TUI, and `sand
shell` paths cannot drift. Introduce the (opt-in) provider selection point, still
resolving to local Lima by default in this task.

## Skills Required
`go`, `refactoring` (interface substitution across packages without behaviour change).

## Acceptance Criteria
- [ ] No consumer package holds `*lima.Client`: `grep -rn '\*lima\.Client' cmd internal --include='*.go' | grep -v '_test.go'` returns only the local provider's own implementation (task 2's package), nothing in `cmd/sand`, `internal/ui`, `internal/browse`, or `internal/provision` consumer code.
- [ ] Provider construction is centralised (one helper the three entrypoints call), replacing the three duplicated `lima.New(lima.NewExecRunner())` sites in `cmd/sand/main.go`, `create.go`, and `shell.go`.
- [ ] The existing narrow seams are preserved and re-expressed over the interface: `ui.guestShell`, `browse.DirLister`/`NewGuestLister`, `cmd/sand.vmGetter`, `cmd/sand.headlessProvisioner`. Their tests still compile and pass.
- [ ] `internal/ui` model, `commands.go`, `transfer.go`, `heartbeat.go`, `commandreg.go` use the interface; `internal/browse.guestLister`, and the `provision` free functions (`ApplySecrets`, `StageOut`/`StageIn`, `applyGitCredEntries`, `guestHome`) take the interface (or the narrowed sub-interface they actually use).
- [ ] **Verification**: `go build ./... && go vet ./...` clean; `go test ./... -race` passes including the `teatest` golden snapshots (unchanged); `go test -tags limae2e -run E2E ./cmd/sand/` still passes against local Lima on this KVM host.

## Technical Requirements
- Default provider resolution = local Lima; behaviour identical to today for an unconfigured `sand`.
- Do not change the registry schema here (task 4) — only swap the client type and centralise construction.
- Keep golden files unchanged; if a golden diff appears, it signals a behaviour regression to fix, not to re-baseline.

## Input Dependencies
- Task 2: the `Provider` interface and local Lima provider.

## Output Artifacts
- All consumers depend on `provider.Provider`.
- A single provider-construction entrypoint the selection logic (task 4/5) hooks into.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

Wiring today (from the map): `cmd/sand/main.go:57`, `create.go:150`,
`shell.go:81` each do `cli := lima.New(lima.NewExecRunner())` then build a
`provision.Provisioner{Lima: cli}` and pass `cli` to `ui.New` / the headless
paths. Replace with one constructor (e.g. `provider.NewDefault()` returning a
`provider.Provider`) that all three call, so the "keep the three paths from
drifting" rule in `AGENTS.md` is enforced structurally.

`internal/ui/model.go:283` `func New(cli *lima.Client, prov *provision.Provisioner)` becomes `New(p provider.Provider)` (the provider already carries the
provisioner internally; if the TUI needs create/reset it calls `p.Create`/
`p.Reset`). `commands.go` functions (`listCmd`, `startCmd`, `stopCmd`,
`restartCmd`, `applySecretsCmd`, `deleteCmd`, `stopAllCmd`, `shellCmd`) take the
interface. `transfer.go` uses `p.Copy` and the provider's guest-path/home/user
accessors instead of `lima.GuestPath`/`GuestHome`/`GuestUser`. `heartbeat.go`
already has the narrow `guestShell` interface — keep it, just satisfy it from the
provider. `commandreg.go` gating on `v.Status == "Running"` is unchanged.

`internal/browse.NewGuestLister(cli *lima.Client, vm string)` becomes a narrow
interface (the guest lister only needs `Exec`/`Shell`). `cmd/sand/shell.go`'s
`vmGetter` (needs `Get`) and `headlessProvisioner` (needs create/recreate) are
already narrow — point them at the provider or a sub-interface.

The `provision` free functions take `*lima.Client` today; change them to take
the interface or a minimal sub-interface. `Provisioner.Lima *lima.Client`
becomes the lima core type it needs (it can keep using the concrete core, since
the provisioner is *inside* the local provider — it does not need the top
interface). Decide per call site: the provisioner internals keep the lima core;
the app-level consumers take `Provider`.

Run the full suite frequently; the golden TUI tests are the tripwire that the
migration changed no visible behaviour.
</details>
