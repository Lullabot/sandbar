---
id: 3
group: "sand-binary"
dependencies: [1]
status: "pending"
created: 2026-07-06
model: "sonnet"
effort: "high"
skills:
  - go
  - cli
complexity_score: 8
complexity_notes: "Multi-component integration: new subcommand + flag parsing mirroring new-vm.sh, driving the existing Provisioner create/recreate/rebuild flows, PLUS replicating the managed-registry bookkeeping (Add/Reconcile/recreate-gate) that currently lives only in the UI layer. Secret-over-stdin hygiene must be preserved (security-sensitive)."
---
# Add the headless `sand create` subcommand (with managed-registry bookkeeping)

## Objective
Give `sand` a non-interactive provisioning mode that mirrors `new-vm.sh`'s flag
set, so CI and scripted users can create VMs with no TUI and no prompts. Add a
`sand create` subcommand (bare `sand` stays the interactive TUI) that parses the
`new-vm.sh` flags, builds a `vm.CreateConfig`, and drives the existing
`provision.Provisioner` create / `--recreate` / `--rebuild` flows, streaming
progress to stdout. Critically, `sand create` must also perform the
managed-registry bookkeeping that today lives only in `internal/ui/model.go`, so
a headless-created VM is recorded as managed (with its `CreateConfig`) and stays
recreate-able — matching the interactive path exactly.

## Skills Required
- **go** — subcommand dispatch, `flag` parsing, wiring the existing
  `Provisioner` + `vm.CreateConfig` + `registry.Registry` together.
- **cli** — non-interactive UX: flag surface parity, sensible exit codes,
  streamed progress, and secret handling that never leaks tokens to argv.

## Acceptance Criteria
- [ ] `sand create` exists as a subcommand; bare `sand` (no subcommand) still
      launches the TUI unchanged. `sand create -h` prints usage covering the
      flags below.
- [ ] The flag surface mirrors `new-vm.sh`'s `usage()` **minus `--ref`**:
      `--name`, `--hostname`, `--user`, `--git-name`, `--git-email`, `--cpus`,
      `--memory`, `--disk`, `--locale`, `--domain`, `--docker-proxy-host`,
      `--clone-url`, `--clone-token`, `--recreate`, `--rebuild`, `--base-name`,
      and `-y/--yes`. `--ref` is deliberately absent (the playbook is embedded;
      there is no ref to pin) — a comment records this so it is not read as a gap.
- [ ] Required-field validation reuses `vm.CreateConfig.Validate()` (git
      name/email required, name ≠ base-name, cpus ≥ 1). Missing required flags
      under `--yes` exit non-zero with a clear message (no interactive prompt).
- [ ] `sand create` drives the same provisioner entrypoints as the TUI:
      default → `CreateVM`, `--recreate` → `Recreate`, `--rebuild` → force a
      base rebuild before create (matching `new-vm.sh --rebuild` / the TUI).
- [ ] On success, `sand create` records the VM in the managed registry with its
      `CreateConfig` (`registry.Add`), runs the same reconcile pass, and honours
      the managed-VM gate before a `--recreate` — either by calling a shared
      helper extracted from `model.go` or by performing the bookkeeping itself.
- [ ] Secrets stay off argv: `--clone-token` (and finalize vars) reach the guest
      only via the existing vars-over-stdin-into-tmpfs path
      (`inGuestScript`/`runProvision`); a `sand create` invocation never places
      a token on any command line inside the guest.
- [ ] A unit test asserts that a headless create records the VM as managed with
      its `CreateConfig` (stub the provisioner/lima side; assert
      `registry.IsManaged(name)` is true and `registry.Config(name)` round-trips
      the config). Runnable via `go test ./... -run Headless` (or similar).
- [ ] `go build ./cmd/sand` and `go test ./...` pass from the repo root.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Entrypoint today: `cmd/sand/main.go` runs `Preflight`, `LocatePlaybook`, then
  the TUI. Add subcommand dispatch: `os.Args[1] == "create"` → headless path;
  otherwise TUI. Keep `lima.Preflight()` (limactl presence) on both paths and
  reuse `provision.LocatePlaybook()` (three-tier after task 2) for the playbook
  dir.
- Reuse, do not reimplement: `vm.CreateConfig` + `Validate()` +
  `EffectiveHostname()` + `ParseCPUs`; `provision.Provisioner.{CreateVM,Recreate,BuildBase}`;
  `registry.{Load,Add,Reconcile,Remove,IsManaged,Config,Base}`.
- **Interface constraint (from the plan):** `registry.Add`, the reconcile pass,
  and the recreate gate currently live in `internal/ui/model.go`
  (see `provisionDoneMsg`/`vmsLoadedMsg` handling and the `confirmBase` gate),
  **not** in the `Provisioner`. So headless cannot inherit them for free. Prefer
  extracting that bookkeeping into a small shared helper (e.g. in `registry` or
  a new `internal/manage` package) that both the TUI and `sand create` call, so
  the two entrypoints cannot drift. If extraction is too invasive, replicate the
  exact sequence in the headless path — but then add the unit test above to lock
  parity.
- `--rebuild` semantics: delete/rebuild the base image first, then create — mirror
  `new-vm.sh --rebuild`. The provisioner rebuilds the base automatically when
  stale (`baseStale`); `--rebuild` forces it regardless (e.g. delete the base via
  `lima.Delete(cfg.BaseName, true)` before `CreateVM`, or add a force path).
- Stream provisioner output to `os.Stdout` (the `Provisioner` methods already
  take an `io.Writer`); no Bubble Tea. Exit 0 on success, non-zero on error with
  the error on stderr.
- Defaults come from `vm.DefaultCreateConfig()`; `--yes` means "never prompt" —
  accept the defaults for anything unset (matching `new-vm.sh -y`). In headless
  mode there is no host-git autodetect requirement — require `--git-name` /
  `--git-email` explicitly (validation already enforces this).

## Input Dependencies
- **Task 1** (module at repo root) — required to build against the relocated
  packages.
- **Task 2** (embedded playbook / three-tier `LocatePlaybook`) — soft but
  strongly recommended: it is what lets `sand create` run with no checkout
  present (plan Success Criterion 1). The subcommand compiles and works from a
  checkout without task 2, but the no-checkout path needs it.

## Output Artifacts
- A working `sand create` headless command and the shared/embedded
  managed-registry bookkeeping. Consumed directly by the CI migration (task 5,
  which runs `./sand create --yes …`) and required before `new-vm.sh` can be
  removed (task 6).

## Implementation Notes
Keep the in-guest secret hygiene (Ansible vars streamed over stdin into tmpfs,
never argv) — it is part of the security posture, not just an implementation
detail. Treat `new-vm.sh`'s `usage()` as the parity checklist and record
`--ref`'s deliberate omission.

Per the project's test philosophy — *write a few tests, mostly integration*:
meaningful tests verify custom business logic, critical paths, and edge cases
specific to this application; test *your* code, not the framework. Write tests
for custom business logic and algorithms, critical workflows and data
transformations, edge/error conditions for core functionality, integration
points between components, and complex validation. Do **not** test third-party
or framework functionality, simple CRUD without custom logic, trivial
getters/setters/static config, or obvious code that would break immediately if
wrong. Combine related scenarios into a single task/test; favour
integration/critical-path coverage over per-method unit tests; avoid one test
per CRUD op; question whether simple functions need a dedicated test. Here that
means: one focused test proving a headless create records the VM as managed with
its `CreateConfig` (the load-bearing parity guarantee) — not a sweep of every
flag.

<details>
<summary>Step-by-step</summary>

1. In `cmd/sand/main.go`, branch on the first arg:
   - `sand` (no args) → existing TUI path (unchanged).
   - `sand create <flags>` → new `runCreate(...)`.
   - Unknown subcommand → usage + exit 2.
2. Implement `runCreate`:
   - Parse flags with a `flag.FlagSet` named `create`. Map each `new-vm.sh` flag
     to a `CreateConfig` field; support both `-y` and `--yes`; add `--recreate`
     and `--rebuild` bools. Do **not** add `--ref` (leave a comment: embedded
     playbook, no ref).
   - Seed from `vm.DefaultCreateConfig()`, overlay parsed flags, parse cpus via
     `vm.ParseCPUs`, then `cfg.Validate()`. On validation error: print to stderr,
     exit 1.
   - `lima.New(...)` + `Preflight()`; `provision.LocatePlaybook()` for the dir;
     `prov := &provision.Provisioner{Lima: cli, PlaybookDir: dir}`.
3. Managed-registry bookkeeping (extract-or-replicate):
   - `reg, _ := registry.Load()` (fall back to `NewEmpty()` on load error, like
     the TUI).
   - Reconcile against `limactl list` before acting (same as `vmsLoadedMsg`), so
     a VM deleted outside sand isn't wrongly treated as managed.
   - `--recreate`: enforce the managed gate — only recreate a VM the registry
     marks managed (mirror the TUI's `confirmBase != ""` gate). If not managed,
     exit non-zero with a clear message. Then call `prov.Recreate(ctx, cfg, os.Stdout)`.
   - `--rebuild`: force-delete the base (`cli.Delete(cfg.BaseName, true)`,
     ignoring not-found) then `prov.CreateVM(...)`.
   - default: `prov.CreateVM(ctx, cfg, os.Stdout)`.
   - On success: `reg.Add(cfg)` and persist (mirror `provisionDoneMsg`). Surface
     a warning (non-fatal) if the index write fails, exactly like the TUI.
   - Strongly prefer moving reconcile + add + gate into a shared helper both
     `model.go` and `runCreate` call; update `model.go` to use it so behaviour
     cannot drift.
4. Wire `ctx` with signal handling so Ctrl-C cancels the underlying `limactl`
   (the provisioner honours context cancellation), matching the TUI's cancel.
5. Add the parity unit test (see Implementation Notes) using the existing
   fakes/stubs (see `provision_test.go` / `model_test.go` patterns): drive
   `runCreate`'s bookkeeping with a stubbed provisioner that "succeeds", then
   assert `reg.IsManaged(name)` and `reg.Config(name)` round-trips the config.
6. Verify `go build ./cmd/sand` and `go test ./...`. Confirm `sand` (bare) still
   opens the TUI and `sand create -h` lists the full flag surface.
</details>
