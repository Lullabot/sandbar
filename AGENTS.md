# AGENTS.md

Guidance for AI coding agents working in this repository. Keep it accurate as
the project evolves.

## What this is

`sand` is a tool for spinning up disposable development VMs for coding agents. It has
two halves that share one repo:

- **A Go TUI/CLI** (`cmd/sand`, `internal/…`) that drives [Lima](https://lima-vm.io)
  to create, clone, reset, and manage VMs, plus a host-side secrets store.
- **An Ansible provisioner** (`site.yml`, `roles/…`, `group_vars/`) that
  configures a VM once it boots. The Go side embeds and runs it.

User-facing documentation lives in a published MkDocs Material site
(`docs/`, `mkdocs.yml` at the repo root) — see "Docs" under Build, run,
format below. `README.md` is a short landing page that points at the site;
it is not where prose belongs.

### Go package layout (`internal/`)

- `provider` — the backend-agnostic seam (`Provider` interface) that owns the
  whole VM lifecycle (discovery, power, provisioning), guest transport, and
  interactive attach. Three implementations: local Lima (`NewLocalLima`),
  remote-Lima-over-SSH (`NewRemoteLima`), and Proxmox VE over its REST API
  (`NewProxmox`). **Do not assume the transport is always Lima/SSH** — the
  Proxmox provider (`proxmox*.go`) drives an HTTP API (`internal/pve`), builds
  its base as a PVE *template*, discovers each VM's IP from the guest agent and
  then reuses the SSH transport for shells/copy, satisfies `HostFiles` with a
  local per-endpoint state dir (no "host where limactl runs" exists), and
  implements `Provenancer` via PVE tags + the description field rather than a
  sidecar marker. `Provider.ForwardArgv(v, hostPort, guestPort)` returns
  arguments for a process that forwards a guest loopback port to workstation
  loopback. The ports can differ. It returns nil when the backend already
  forwards ports: local Lima uses the same port on guest and workstation.
  Remote Lima returns `ssh -N -L` arguments targeting the configured
  `SSHHost`, where Lima has already forwarded the guest port. Proxmox returns
  SSH arguments targeting the guest directly. Like `AttachArgv` and
  `RunArgv`, this method performs no I/O; tests check its arguments without
  SSH, a network, or a VM. `internal/landreview` starts the returned command
  and stops it when the review ends. There is no
  process-global "the provider" anymore: `provider.BuildFleet`
  constructs one `Binding` (provider + registry.Scope) **per enabled
  Connection Profile** from `internal/profiles`' persisted store, so a
  headless command binds to exactly the one profile it's told to act on
  (`--profile`, else last-used, else Local), and the TUI holds one binding
  per enabled profile live at once. The old `SAND_PROVIDER`/`SAND_REMOTE_*`
  env-var selection (`Resolve()`) is **removed** — profiles are the only
  configuration surface now. The `lima` package exports a host-access seam
  (`Host` = `Runner` + `HostFiles`, local vs SSH) that the two providers
  differ on; the local provider is behaviourally identical to sand's
  previous direct use of `*lima.Client`.
- `profiles` — the persisted, secret-free Connection Profile model
  (`profiles.yaml`) that is now the single source of truth for every
  location `sand` can run VMs on: a permanent Local profile plus any number
  of named `remote-ssh` profiles (host/user/port/key-path/Lima-home, no
  secrets) or `proxmox` profiles (host/node/pool/storage/bridge + a
  `token_file` **path**, still no secrets — the token value lives in the file,
  loaded by `profiles.LoadToken`, which refuses one readable by group/other).
  The TUI's profile creation form (`p` → `n`) opens a type picker to choose
  Remote SSH or Proxmox (Local is permanent/pre-seeded, never creatable) before
  presenting the form. The Proxmox form includes one boolean **checkbox** input
  (`insecure` for self-signed certs) — the sole non-text input in the profile
  form family; **space** toggles it and enter does not, because enter is the
  form's "next field" everywhere else and a row enter cannot leave is a trap.
  **A form that does not render every field must carry the rest across on
  save.** `Store.Update` replaces the record wholesale, so a field with no
  input is erased from `profiles.yaml` by an edit as innocent as a rename
  unless `submitProfileForm` copies it from the stored profile — silently,
  since the user never saw it. Both forms render every field of their type
  today (`user`, `image_storage` and `base_image` used to be the exception, and
  were carried across until they got inputs of their own), so the rule is
  currently vacuous — and only stays that way if the next Proxmox field gets a
  row in `profileFormSlots`. Optional is not a reason to omit one: an empty
  input means "use the provider's default", which the row's `info` help names
  ("Blank → …"), rendered under the form for whichever row has focus — the
  same treatment the VM create form's `fieldInfo` gets. Every such string must
  be a **constant**: deriving one from the environment (`vm.HostUser()` is the
  tempting one) bakes the developer's machine into every golden file. And the
  help block is drawn under a height BUDGET, after the error and footer are
  measured — a thirteen-row form plus unbudgeted help scrolled the footer off
  the bottom at 80x24, hiding the only statement of how to save or leave.
  A row whose help says "Required." must be in `submitProfileForm`'s
  required-field table, and vice versa. For `identity_path`, `storage` and
  `bridge` that table is the ONLY gate — `profiles.validate` checks just
  host/node/pool/token_file, deliberately, so `LoadFrom` keeps loading a
  hand-edited file rather than locking the user out of every other profile.
  Without the gate the form saves a profile that can never create a VM
  (`pve.CreateVMOptions` rejects an empty storage or bridge outright).
  For the same reason `connectionFieldsEqual` must compare every field
  `provider.TargetConfigFor` reads: one left out is read as a pure rename and
  never rebuilds the live binding.
  Deliberately does not import `provider` (to avoid an import
  cycle) — `provider.BuildFleet` is what converts a `Profile` into a
  `Binding`.
- `pve` — a small, dependency-free (`net/http`) client for the Proxmox VE REST
  API used only by the Proxmox provider. Encodes the PVE semantics that matter:
  tri-state task `exitstatus` (`WARNINGS: n` is **success**), a 403's detail
  living in the HTTP reason phrase (not the body), async `POST`/sync `PUT`
  config, and node/VM stat fields that lie (QEMU `disk` is hardcoded 0). Adds
  **no** module dependency — keep it that way.
- `lima` — typed wrapper over the `limactl` CLI. All subprocess execution goes
  through the `Host` interface (a union of `Runner` and `HostFiles` seams),
  and `Runner` is the gateway for subprocess execution — both implement it
  (`ExecRunner` for local, `SSHHost` for remote-Lima-over-SSH), so code is
  testable without a real binary. When provisioning, the provisioner itself
  depends on `*lima.Client` and the `Host` seam, never on `Provider`.
- `provision` — orchestrates create/reset (base build, `limactl clone`,
  finalize) and the Ansible run; `staging.go` moves data across a reset.
  Depends on `*lima.Client` and the `Host` seam (for base-image file access),
  not directly on `Provider`. Both backends share `staging.go`'s
  `PlanProject`, which checks what the guest actually holds before choosing
  what to preserve. They also share `StageGuard`: before attempting to
  delete the guest, a failure removes the staged copy because the original
  data is still in the VM. Once deletion is attempted, every failure keeps
  the archives and reports their path for recovery.
- `registry` — managed-VM index keyed by connection scope and name (schema
  v3, migrated on read). The creating provider determines the scope, such
  as `LocalScope` for local Lima or `user@host:port` for remote Lima. This
  keeps local and remote entries separate and lets scopes have VMs with the
  same name. Every write must go through `mutate`: lock the file with `statelock`,
  re-read it, apply the change, then save. Saving a stale in-memory map can
  erase another process's updates. Call `save` only from `mutate`.
  `Reconcile` must prune only entries the caller already knew about; a VM
  created after the caller's listing is not evidence of a deleted VM.
- `statelock` — advisory file locks (`<path>.lock`, `syscall.Flock`) for
  `registry` and `secrets` read-modify-write operations. If the lock cannot
  be acquired within the wait budget, the write proceeds without it, as
  with the provisioner's base lock.
- `ui` — the Bubble Tea model, views, and commands (board/form/secrets/progress/
  profile-management/…).
- `landreview` — runs a browser review for one guest checkout using
  `self-review-serve` (`ServeBinary`). Start the server before choosing the
  forward: it selects a free port and has no flag for a fixed one. Parse its
  `[serve] Review ready at http://127.0.0.1:<port>/` message with
  `serveReadyRe`, then reserve a workstation port and run `ForwardArgv`'s
  command. Local Lima instead uses the guest port directly. Probe
  `/api/config` and check its JSON to identify the server; probing `/` could
  accept an unrelated application on a colliding port. The server writes
  `review.xml` and exits to signal completion. Keep this shared operation
  here so both the CLI and TUI can use it; the TUI cannot import `cmd/sand`.
  Assistant skills are installed before an agent starts: the `project` role
  calls `self-review-install-skills` for the initial clone. Landing must not
  install them as a review-start side effect. The same command accepts a repo
  path for checkouts cloned later and also configures the user's active global
  Git excludes file.
- `secrets`, `manage`, `browse`, `vm` — host-side secrets store (schema v3,
  now also keyed by connection scope — distinct from its pre-existing
  per-directory scope, see `docs/reference/files-and-state.md`), shared
  registry bookkeeping, file browser, domain types.

Entrypoint: `cmd/sand/main.go`. Keep CLI operations consistent with their
TUI equivalents: `sand create` is `n`, `sand reset` is `R`, `sand shell` is
`S`, `sand land` is `l`, and `sand paste-image` is `v`. Publishing is also
available from the Landing pane. The CLI previously lacked the TUI's reset
preserve options; shared operations prevent this kind of drift.

Create/reset checks and bookkeeping belong in `internal/manage`
(`RecreateBase`, `RecordSuccess`, `Reconcile`). After a build,
`cmd/sand/secrets.go`'s `settleSecrets` must match the TUI's
`provisionDoneMsg` handling. Both shell entrypoints must use
`provider.AttachArgv()` to construct their guest attach command.

Create and the TUI build providers through `provider.BuildFleet` from the
enabled profiles. A CLI create binds only its target profile; the TUI binds
all enabled profiles. Commands acting on an existing VM
(`reset`, `shell`, `land`, `paste-image`) use `resolveVMProfile` to find its
owning profile through the marker, registry, then live listing.

**A reset keeps the target VM's identity and project.** Its name, base image,
and clone URL come from its recorded configuration. The TUI locks the name
and repository rows (`fieldLocked` in `internal/ui/form.go`); `sand reset`
has no `--clone-url`; `sand create --recreate --clone-url` is rejected.
Allowing the URL to change could make a preserve option name the old
project while cloning a different one. Create another VM for a different
repository.

## Build, run, format

```
go build ./cmd/sand      # build the binary
go run ./cmd/sand        # run the TUI
gofmt -l .               # must be empty; format before committing
go vet ./...
```

There is no Makefile.

## Docs

The documentation site (`docs/`, `mkdocs.yml`) is built with MkDocs
Material, invoked through [`uv`](https://docs.astral.sh/uv/)'s `uvx` — no
global Python, no virtualenv, no Node toolchain:

```
uvx --with-requirements docs/requirements.txt mkdocs serve          # live preview
uvx --with-requirements docs/requirements.txt mkdocs build --strict # check (CI gate on PRs)
```

`--strict` fails the build on any broken link or a `nav:` entry pointing at
a missing page. Run it locally before pushing a docs change.

## Testing

```
go test ./...                                  # unit + integration (no VM needed)
go test ./internal/ui -run TestTUI -update     # regenerate TUI golden snapshots
go test -tags limae2e ./...                    # real-VM e2e (needs limactl + KVM)
```

Conventions:

- **Consumers of `provider.Provider` should fake the interface itself** using
  `internal/providerfake.Provider` — one test double struct with a function
  field per interface method, so a test drives the exact behaviour it cares
  about and never panics on a forgotten mock. See the package doc for the
  defaulting contract (unset fields return sensible zero values). The TUI,
  browse, and entrypoint tests (`internal/ui`, `internal/browse`, `cmd/sand`)
  all depend on `provider.Provider`, so this is the primary test seam.
- **For tests that genuinely need limactl-shaped provisioner plumbing
  underneath**, the local provider still offers runner-level fakes: use a fake
  `lima.Runner` (see the `fakeRunner`/`listFakeRunner` types in the `*_test.go`
  files) that returns canned output, and build a `*lima.Client` over it for
  deeper testing. This is heavier but necessary when a test must drive the
  base-image machinery or other lima-core logic, not just the provider
  interface.
- **No test may write to the developer's host state.** A fake `Runner` stops a
  test from *running* `limactl`; it does nothing about the files the code
  around it writes. Isolate the environment too — `isolateHostState(t)` in
  `internal/ui` sets **both** `XDG_DATA_HOME` (managed-VM index, secrets store)
  and `LIMA_HOME` (the base image's playbook-version stamp). `LIMA_HOME` is not
  hypothetical: the TUI tests build a real `provision.Provisioner` over a fake
  runner, so driving a create walked `ensureBaseStopped` → `writeBaseVersion` and
  stamped the developer's real `sandbar-base` as freshly built from a playbook it
  had never seen — which makes `baseStale` skip the rebuild the user needs and
  clone from a stale image, silently.
- **TUI integration tests** use `charmbracelet/x/exp/teatest`
  (`internal/ui/teatest_test.go`): they boot the whole program in a simulated
  terminal, drive it with real key events, and snapshot
  `ansi.Strip(FinalModel().View())` against `internal/ui/testdata/*.golden`.
  Goldens are ANSI-stripped on purpose — portable across colour profiles and
  readable in review. Regenerate with `-update` and eyeball the diff.
- **Tests that boot real VMs** are gated behind `//go:build limae2e`. Plain
  `go test ./...` skips them. Run them locally on a host with Lima (this dev
  box has KVM); they are **not** run by CI's `go test`.
- Isolate on-disk state: tests set `XDG_DATA_HOME` to a temp dir so the managed
  index and secrets store never touch the developer's real files.
- **No `t.Parallel()` — the suite is deliberately serial.** Tests pin
  package-level function-var seams (`hostMemBytesFn`, `playbookVersionFn`,
  `buildVersion`) and use `t.Setenv` heavily; running them in parallel would race
  on that shared mutable state. Do not add `t.Parallel()` to a test that touches
  those seams, and think twice before introducing parallelism at all.
- **The concurrency tests are timing-based** (`buildDelay`, `time.Sleep` in
  `internal/provision/provision_test.go`'s base-image race tests). They work
  under `-race`, but if you touch the provisioning concurrency model, prefer
  converting them to channel/barrier-based determinism rather than tuning sleeps
  — a timing test that stops catching the race fails silently.
- **Coverage floor.** New code should keep the `unit` job's coverage gate green
  (`./internal/...` ≥ `COVERAGE_FLOOR`); when you add meaningful coverage, bump
  the floor in the same PR so it ratchets up. When a failure arm can't be reached
  without a production-code seam, flag it as a follow-up rather than contorting a
  test around it.

## CI (`.github/workflows/test.yml`)

Five jobs:

- `lint` — Ansible syntax and isolated agent lifecycle checks
  (`PYTHONDONTWRITEBYTECODE=1 /usr/bin/python3 -m unittest discover -s tests -p '*_test.py'`).
- `unit` — `go vet ./...` and `go test ./... -race -covermode=atomic` (fast, no
  VM). It also enforces a **self-contained coverage gate**: coverage is measured
  over `./internal/...` only (the `cmd/sand` main glue is excluded so it doesn't
  distort the number) and the job fails if the total drops below the
  `COVERAGE_FLOOR` env value committed in the workflow. The floor is a **manual
  ratchet** — bump it by hand in a PR as coverage rises; never auto-committed
  from CI, and no third-party coverage service. The run uploads `coverage.out` +
  `coverage.html` as an artifact.
- `lima-e2e` — builds `sand` and provisions a real Lima VM end to end under
  QEMU+KVM on the hosted runner. Also runs the `cmd/sand` `limae2e` tests
  (headless create + `--recreate` gate) first, on max free disk. (It does not
  run the fast Go suite — that's the `unit` job.) It checks all four agent
  executables in a clone, their absence from the base, and remembered choices
  in the next create.
- `mutation` — **advisory** gremlins mutation testing over the core packages
  (`provision`, `registry`, `vm`, `lima`; `ui` is out of the initial scope).
  Non-blocking (`continue-on-error`).
- `molecule` — converge/verify for the `base` and `samba` roles in a
  systemd-capable Debian container. **Advisory** (`continue-on-error`) because
  `roles/samba`'s `smbpasswd` task is unconditionally `changed_when: true`, so
  its idempotence stage fails until the role itself is revisited (follow-up).

**Triggers:** `push` only on `main`, plus `pull_request` and
`workflow_dispatch`. The heavy `mutation` and `molecule` jobs additionally run
on a weekly `schedule` and are gated to `schedule`/`workflow_dispatch` only, so
they never block a PR. A plain feature-branch push runs **no** CI.

A separate workflow, `.github/workflows/docs.yml`, covers the docs site: a
`mkdocs build --strict` job on every `pull_request`, and on `push` to `main`
or a release tag (`v*`), a `mike deploy` that commits the built site to the
`gh-pages` branch (`push`-to-branch, not the OIDC `actions/deploy-pages`
flow). A release tag additionally moves the `latest` alias.

To validate a branch before a PR exists, dispatch it:

```
gh workflow run test.yml --ref <branch>
gh run list --workflow test.yml --branch <branch> --limit 1   # get the run id
gh run watch <id> --exit-status
```

That is a `workflow_dispatch` run on the branch tip — distinct from the
`pull_request` run that fires (on the same SHA) once the PR is opened.

## The TUI: board architecture (read before touching `internal/ui`)

The TUI's home surface was rewritten from a `bubbles/table` list to a **tile
board** (`internal/ui/board.go`). The facts below are each a place a future
agent's instinct will be *wrong* — the reason is the load-bearing half of
every bullet, not the constraint itself.

- **Charm v2.** This project is on `charm.land/bubbletea/v2`,
  `charm.land/bubbles/v2`, and `charm.land/lipgloss/v2` — not
  `github.com/charmbracelet/...`. Tests import
  `github.com/charmbracelet/x/exp/teatest/v2`. Adding an import from the v1
  module path will not just fail to compile cleanly against the rest of the
  package — the v1 and v2 types are different and don't interoperate.
- **There is NO VM SCREEN, and the board is the only per-VM surface.** It was
  deleted: the tile already showed everything it did (state, live cpu/memory,
  disk, uptime), and the one fact it had that the tile did not — the allocated
  core count — now rides on the cpu gauge's own label, `cpu (4c)` (`cpuLabel`,
  tile.go). Every verb fires on the tile under the focus ring, from `vmCommands`
  (commandreg.go). `enter` on a VM tile does NOTHING; it is a verb only on the
  ghost, where it creates a VM. Do not reintroduce a zoom/detail screen — it is a
  second render path for facts the tile already carries, and the last one drifted
  (it rendered `vm.Status` raw, so a failed build read as a green "Running" on the
  very screen an alarmed user opened *because* the tile went red).
- **The board is the only roster surface.** There is no table view and no
  compact list to fall back to. Do not add one without a scope change — it
  would be a second render path for "which VMs exist", and the two would
  drift the way the old table's help bar already had before this rewrite.
- **The board shows managed clones only, always — no toggle.** `f` and
  `m.managedOnly` were deleted on purpose. The one exception is not a
  loophole: a VM with a provision job in flight (or one whose last provision
  *failed*) still gets a tile, even before `IsManaged` is true — because
  filtering on `IsManaged` alone would hide a VM during its own build (Lima
  doesn't report it yet, and it isn't recorded managed until the build
  succeeds) and would erase a failed build's tile entirely, leaving the
  failure with nowhere to be reported or deleted from. Base images and
  unrelated Lima VMs get no tile and there is no key that brings them back.
  The header band used to carry a **hidden count** ("1 base, 2 external hidden")
  as the mitigation for that, and it was **removed on request** in favour of the
  live host readout. So the cost is now **unmitigated and deliberate**: a stale,
  multi-gigabyte base image is invisible from the TUI and is managed with
  `limactl`. If that invisibility ever bites, bring the count back — do not add a
  second roster surface. `X` (stop all) still means *every managed VM*, not the ones a
  `/` search leaves visible (`stopAllTargets` walks `m.vms`, not the filtered
  view, on purpose).
- **The managed/external badge is uniform, and therefore hidden, by
  construction.** Because every tile on the board is managed (see above), the
  exception-only badge rule (`computeFleetUniformity` in `internal/ui/tile.go`)
  never finds a VM to call out. It is not special-cased to hide it — it just
  never has anything to show.
- **The design targets 1–3 VMs, up to 10.** Density features (compact rows,
  virtualized scrolling beyond the simple row-scroll `board.go` already has,
  pagination) are deliberately absent. Do not add them speculatively.
- **The header reports USE, not ALLOCATION**, and so do the tiles. Both read the
  live guest heartbeat — the same and only source — so the two surfaces cannot
  disagree. The header shows host vCPUs busy (each guest's `CPUPct` is a share of
  ITS OWN vCPUs, so it is scaled by that VM's `CPUs` before being summed), the
  memory the guests are actually holding, and free disk. It previously summed the
  allocations; that number never moves and reads as a crisis on an idle machine.
- **A metric with no reading renders as an em dash, never as 0.** A running VM
  whose heartbeat has not reported yet — or whose heartbeat the idle gate tore
  down — has an UNKNOWN cpu, not an idle one. `tileGaugeNoReading` (tile.go) and
  the header both refuse the zero. Relatedly, **every gauge row is fixed**: cpu,
  mem and disk each own a row on a running tile whether or not there is a reading.
  Packing them from the top made disk slide up into a missing gauge's slot, so
  leaving the board and coming back appeared to lose data that was never lost.
- **`CPUs` and `Memory` on `vm.VM` are allocations, not utilization.** They
  are what Lima was told to give the guest, not what the guest is using.
  Rendering one as a filled utilization gauge is a lie with a progress bar
  around it. Live utilization comes only from the guest heartbeat
  (`internal/ui/heartbeat.go`), and only for a running VM with an actual
  sample (`guestSample.Has*`) — never a zeroed bar standing in for "no
  reading yet".
- **Lima reports only `Running` and `Stopped`.** A provisioning VM is
  `Running` to Lima — Lima has no concept of "being provisioned". `Building`
  and `Failed` are sand-side states derived from the job registry
  (`deriveStatus`, consulted *ahead of* `vm.Status`) — see
  `internal/ui/jobs.go`. **Never render `vm.Status` directly on a tile**: a
  failed provision would show as a reassuring green "Running", and an
  in-progress one would show as an idle VM with nothing happening.
- **Tile order is alphabetical and stable; focus is pinned to VM identity,
  not slot index.** Both are deliberate. Sorting by status (running-first)
  looks like an improvement but makes pressing a destructive key (`x`, `d`)
  teleport the focused tile across the board as a *side effect of the verb
  just pressed* — exactly when the user is most likely to press another key.
  Tracking focus by slot index has the same failure through a different
  door: a refresh reorders the roster mid-keypress and a key meant for
  `prod-box` lands on `dev-box`. `focusedVM()`/`syncBoard()` in `board.go`
  are the only correct way to ask "what VM is the ring on".
- **The job registry retains the last run per VM *per kind* — a provision and
  a file transfer are two runs — in memory, including its log; failed jobs
  are kept, not discarded.** Dropping a failed job on completion would make a
  failed provision render as healthy the moment its progress view closes. So
  would keying the registry by VM name alone, which is why it is keyed by
  `jobKey` (VM + `jobKind`): a failed build's tile stays red and Lima still
  calls that half-built VM `Running`, so `u` is offered on it — and a copy
  sharing the build's slot would evict it, flip the tile green, and destroy
  the Ansible log that was the only record of the failure. **Only a
  provision moves a VM's status** (`deriveStatus`); a copy that fails is a
  failed copy, not a broken VM. Run history is **not** persisted across
  restarts and there is no multi-run history beyond those two slots — both
  are deliberately out of scope; do not build a storage format for this
  without a scope change.
- **Keys, help text, and verb eligibility all derive from one command
  registry** (`internal/ui/commandreg.go`). Do not reintroduce a
  hand-maintained help list beside it — that duplication is what this
  replaced, and it had already drifted: the old hand-maintained help switch
  advertised `x stop`, `u upload`/`g download`, and `R reset` unconditionally,
  so a stopped VM's footer offered actions that silently did nothing when
  pressed. There is deliberately no fuzzy command palette; this file "stays
  narrow on purpose" per its own header comment.
- **An assertion must reach the boundary the user cares about.** A golden
  test proves a screen *painted*. An in-process behavioural test proves the
  model or the store changed. **Neither proves the guest changed.** This rule
  is written in blood: the secrets editor shipped past a passing golden (it
  dropped every keystroke because the textarea never had focus), and then its
  replacement behavioural tests passed while `ctrl+s` still never reached the
  guest. If a claim crosses into a VM, onto a disk, or across a process
  boundary, test the far side — not just that the model or the screen agree
  with themselves.
- **Saving a secret applies it to a running guest immediately** (see
  `updateSecrets` in `internal/ui/secrets.go`, which batches
  `applySecretsCmd` when the VM is running). Do not "simplify" this back to a
  store-only write with a generic "applies on next start" message — the apply
  *is* the feature; without it, rotating a token on a VM you're actively
  using requires a restart you shouldn't need.
- **`limactl shell` forks an ssh child that inherits the exec pipes.**
  Cancelling the context orphans the ssh process, which keeps the pipes open
  and leaks the goroutine holding the SSH connection (this is how the guest
  heartbeat talks to a running VM). `internal/lima/runner.go` sets
  `cmd.WaitDelay` for exactly this reason — do not remove it as dead-looking
  configuration.
- **Ansible prints no task count anywhere in its own output.** The in-guest
  script derives an exact denominator via `ansible-playbook --list-tasks` and
  echoes `SAND_ANSIBLE_TASK_TOTAL` so the tile's build progress bar has an
  honest fraction instead of an animated guess.
- **`q` quits from the BOARD ONLY.** It is not on any child
  screen, deliberately: on a child screen the key that means "I am done here" is
  `esc`, and a `q` sitting beside it turns one mistyped key into "close the
  application" rather than "close this screen". The root screen is the only place
  with nowhere left to go back to, so it is the only place that offers the exit.
- **The ghost tile is a selectable CELL, not a printed instruction.** The empty
  slot takes the focus ring like any tile (`ghostFocusName`, a sentinel holding a
  NUL byte that Lima cannot produce, so it can never collide with a VM name), and
  `enter` on it opens the create form; `n` still works from anywhere. Two rules
  follow, and both are load-bearing: focus on the ghost is **sticky** — a VM
  appearing must not steal the ring, or a user who deliberately arrowed onto the
  empty slot would have focus yanked away on every refresh tick — but a **create
  moves the ring to the new VM** (`beginJob`), because that is the user acting, not
  the board reordering itself under them. `syncBoard` only adopts the ghost once
  `vmsLoaded` is true: before the first `limactl list` lands the board is empty
  because nothing is loaded, not because the host is bare, and the identity pin
  would then hold the ring on the ghost as the real tiles arrived.
- **`beginStream` starts a job; it does not choose a screen.** Which view a run
  lands on belongs to the caller, and the two callers want opposite things: a
  **build returns to the board**, where its tile carries the badge and the
  progress bar (its log is one `l` away), while a **transfer opens its log**,
  having no tile bar of its own. Flipping the view inside `beginStream` is how
  every run ended up seizing the terminal with a full-screen Ansible dump —
  the takeover the job registry exists to end. The suite did not catch it
  because the suite asserted it; do not reintroduce either.
- **`limactl list` fails outright while ANY instance is mid-clone or mid-delete**
  ([lima-vm/lima#5236](https://github.com/lima-vm/lima/issues/5236)). `limactl
  clone` creates the instance directory before writing its `lima.yaml`, and
  `limactl list` aborts on the first instance it cannot load instead of skipping
  it — so it exits 1 and prints **nothing**, and every other instance vanishes
  from the listing too. The window is 40–60s for a clone (i.e. most of a create
  or reset) and sub-second for a delete. `limactl shell`, `start` and `stop` are
  unaffected; only enumeration breaks.
  `lima.ErrListRacedInstanceDir` recognises it and `vmsLoadedMsg`'s handler keeps
  the fleet it already has, saying so **once** instead of on every 5s tick. Do not
  "simplify" that away into a plain error: it is the difference between a build
  the user is watching and a screen full of failures about a VM that is coming up
  exactly as intended. If #5236 is fixed upstream, the error string stops
  appearing and the workaround stops firing on its own.
- **`limactl copy`'s backend is pinned to `scp`, and the pin is load-bearing.**
  Under limactl 2.1.3 the backend decides **where the files land**, not just how
  fast: Lima's rsync backend appends a trailing slash to every path of a
  recursive copy (`pkg/copytool/rsync.go`), and `srcdir/` means *the contents of*
  `srcdir` to rsync — so it splats a directory into the destination and never
  creates it, while scp nests it. `--backend=auto` prefers rsync **whenever the
  guest has it installed**, which made placement a function of the sandbox's
  packages. Do not "optimize" back to `auto` or `rsync`. For the same reason the
  destination handed to `lima.Copy` is the user's directory **verbatim** — do not
  reintroduce a basename-appending compensation layer, which nests correctly only
  until the destination already contains the directory (the second upload of
  `mydir` then lands in `dest/mydir/mydir`).
- **Naming prohibition: no nautical metaphor anywhere.** No harbour/harbor,
  slip, boat, pier, moored, deck, or cargo in any identifier, comment, or
  user-visible string, in this subsystem or elsewhere in the repo.
- **The checkout registry (`internal/checkouts`) is populated ONLY by a
  sweep of a RUNNING guest, and is otherwise a passive, host-side cache.**
  The unlanded-work badge and `sand land`'s listing read this cached data and
  never re-sweep on their own — the badge in particular runs on the render
  path, where any guest contact is forbidden outright. A stopped or
  never-swept VM's entry can only get staler, never fresher.
  The ONE exception is the delete guard's freshness re-read (see the next
  bullet), which is bounded, running-VM-only, and user-initiated.
- **The delete guard (`internal/ui/deleteguard.go`) NEVER starts a stopped
  VM to inspect it — this is load-bearing, not incidental.** A user deleting
  a VM may be doing so precisely because they suspect it is compromised;
  booting it to look inside defeats the point. A stopped VM's confirmation is
  therefore composed from the registry's already-cached data alone, labeled
  `(as of <ago>)` so it is never mistaken for a live read.
  `deleteguard_test.go`'s `TestDeleteGuardNoGuestContactStoppedVM` enforces
  this with a Runner that fails the test if any of its methods are called; do
  not weaken that test to "fix" a feature request here.
  A RUNNING VM is different, and deliberately so: its cached entry can be a
  full `sweepInterval` stale, which is exactly the window in which someone
  edits a file and then reaches for delete. Raising the confirmation for a
  running VM therefore fires ONE re-read (`sweepRegistry.sweepOnce`) and holds
  the Confirm key until it lands. That is not a new capability — it is the
  same read-only pass that VM's own sweep loop is already running, against a
  VM that is already up — and it is hard-bounded by `sweepOnceTimeout` so a
  wedged guest degrades to the cached answer plus "(could not re-check just
  now)" rather than freezing the overlay. Cancel is always accepted.
- **Landing (`l` / `sand land`) never copies code to the host.** It moves PR
  metadata only — branch name, compare URL, PR number/state — via the
  workstation's own `gh` (a two-token split: the guest pushes with its own
  least-privilege token, the host opens the PR with the workstation's `gh`
  and credentials). `--web` never calls `gh` at all. Do not add a code path
  here that fetches a diff, a patch, or the checkout's working tree onto the
  host. That specifically rules out "just stream the changes to `gh api`" —
  GitHub's Git Data API CAN build a commit with no clone, but every byte
  would pass through the host to get there, which is the thing this forbids;
  it would also need the HOST token to gain `Contents: write`, inverting the
  two-token split. Committing and pushing belongs IN the guest, which is what
  the Landing pane's commit-and-push action does (`landCommitPushCmd` +
  `commitAndPushExpr`): it suspends the TUI, runs `git commit`/`git push`
  inside the VM against the user's real terminal, and brings back nothing but
  an exit code. `commitAndPushExpr` must stay a LITERAL string — the checkout
  is selected by `Provider.RunArgv`'s `--workdir` argv element, because the
  path/branch/remote all come from sweeping the guest and must never reach a
  guest `bash -c` as text — that would break the "the only guest mount is read-only, nothing
  leaves the VM except through the TUI's Upload/Download" property this
  feature was deliberately built alongside without becoming a silent
  third exception to it. See `docs/reference/security-model.md`'s "Landing"
  section for the precise, non-overreaching claim this makes.
- **`gh` is invoked argv-only, NEVER through a shell.** Every argument
  reaches `gh` as its own argv element (`internal/landgh`'s `Runner`), so a
  branch name or `org/repo` containing `;`, backticks, or `$(...)` is inert
  — and those values come from a sweep of the GUEST, the lowest-trust
  source in the system. The visible consequence is that a credential held
  only in a shell alias or wrapper function (the 1Password `gh` plugin and
  similar injectors) is invisible to sand, which reports `gh: not
  authenticated` even though the same command works at the user's prompt.
  That is the documented trade, not a bug: do NOT "fix" it by re-invoking
  `gh` through `sh -c` or the user's login shell.
  The 1Password shell plugin specifically IS supported (`internal/landgh/
  opplugin.go`), and supporting it cost nothing here: `op plugin run -- gh
  <args...>` is an ordinary argv invocation, so sand simply prepends those
  elements — no shell is introduced and every argument still arrives as its
  own element. Detection is FILE-ONLY (`~/.config/op/plugins.sh` plus `op` on
  PATH): probing by running `op` could raise an authorization prompt
  underneath the full-screen TUI, so it must stay a pure filesystem read. An
  explicit `GH_TOKEN`/`GITHUB_TOKEN` in sand's environment bypasses the
  plugin path entirely.
- **No drupal.org credential ever belongs in a guest, full stop
  (`internal/drupalorg`).** Publishing a checkout's commits to drupal.org
  (`sand publish`, and the Landing pane's `publish to drupal.org` row) does
  NOT reuse the GitHub two-token pattern above by giving the guest its own
  drupal.org push token — it is host-side by design: the guest only ever
  clones and reads drupal.org anonymously, and produces an inert
  `drupalorg.ChangeSet` (commits, messages, file actions) that is
  structurally incapable of naming a destination — see the package doc
  comment on `internal/drupalorg/payload.go`. Only the host, reading a
  personal access token from
  `${XDG_CONFIG_HOME:-~/.config}/sandbar/drupalorg.token` (`LoadToken` in
  `internal/drupalorg/token.go`, the single read site; never
  `internal/secrets`, which exists to deliver secrets INTO a guest), ever
  attaches that credential — and only for the one call
  (`Publisher.Publish` in `internal/drupalorg/publish.go`) that writes. The
  reason is a blast-radius one, not a taste preference: a drupal.org
  account PAT is account-wide, not scoped to one repository the way a
  GitHub fine-grained token is, and a working Drupal contributor's account
  can hold push access to modules on tens of thousands of sites. Do not
  "simplify" this by wiring a drupal.org token into guest provisioning or
  the secrets store — that is the one thing this design forbids. A manual,
  narrower fine-grained token bounded to a single issue fork does exist as
  something a developer can set up by hand through drupal.org's web UI, but
  `sand` deliberately automates none of it. Keep the reason straight,
  because two different mechanisms get conflated here and the distinction
  decides the design: GitLab exposes **no API whatsoever** that can create a
  *fine-grained* token (`POST /user/personal_access_tokens` takes only the
  `k8s_proxy` and `self_rotate` scopes and no boundary parameter), so there
  is nothing to call and nothing drupal.org could unblock to change that. A
  *project access* token is the mechanism that does have an endpoint — and
  that one requires Maintainer, which drupal.org grants nobody on an issue
  fork, and is separately blocked at drupal.org's edge. Neither is
  automatable, for unrelated reasons, and this page's how-to for it is
  **withheld**: writing it up as safe is gated on a negative control nobody
  has run (a token bounded to one issue fork must be shown to FAIL a push to
  a canonical `project/<module>`, which needs a real drupal.org account). See
  `docs/using-sand/drupalorg-publishing.md#why-publication-is-host-side` for
  that gate and the reasoning in full. Publication also replays each guest
  commit as its own API call rather than pushing a ref — see
  `internal/drupalorg/publish.go`'s file doc comment for why a failed
  replay leaves earlier commits public with no rollback, and why a re-run
  is the only recovery.
- **Collect commits using both the fork upstream and canonical project
  base** (`internal/drupalorg/collect.go`). `BuildCollectCommand` takes
  `forkBase` (the checkout's upstream tracking ref) and `projectBase` (the
  tip of `ForkedFromProject`'s default branch, read by `Client.BranchTip`
  on the host). The guest lists `HEAD --not "$forkBase" "$projectBase"`.
  Excluding only the fork branch can replay upstream commits introduced by
  a merge or rebase under the PAT owner's name. The integration tests cover
  both cases with real Git repositories; do not reduce the range to
  `"<base>..HEAD"`. Use the canonical parent's base, because the fork's copy
  may still point to the commit from when it was created.
  The guest may lack the host-resolved `projectBase` object. Probe with
  `cat-file -e` and report how to fix a missing object; collection does not
  fetch. Resolve the destination before collection in both the CLI
  (`doPublish`) and TUI (`handleLandingFork` → `collectCmd`), so the parent
  is known. Even a "nothing to publish" result therefore requires anonymous
  destination reads. `TestDoPublishNothingToPublishSkipsConfirmation`
  checks this sequence.
- **Create a missing issue branch from the canonical parent**
  (`publish.go`'s `startBranch`/`startProject`). The fork's base may be old;
  replaying changes onto it can revert upstream changes in touched files.
  Reject a missing `ParentBranch` rather than falling back to the fork.
  Test fixtures must use different `forkDefault` and `parentBranch` values
  (currently `10.x` and `11.x`) so they detect use of the wrong source.
- **Reject a diverged fork before writing**
  (`checkForkDiverged`/`ForkDivergedError`). `alreadyLandedCount` matches
  commits at the branch tip. An unrelated commit at that tip can prevent
  resumption and cause replay to duplicate earlier commits or fail when
  creating an existing file. Reject when `present == 0` and a change-set
  identity already appears within the commits returned by `branchCommits`
  (`len(cs.Commits)`). Do not reject `present == 0` alone: it also occurs
  on a valid first publish. Do not replace the ordered match with set
  membership; `alreadyLandedCount`'s comment explains why order matters.
- **Reject merge commits in the collected range; never skip them**
  (`MergeCommitsError`). The content API has no second-parent field, so it
  cannot reproduce a merge. The guest emits `merge=` lines and stops;
  `ParseCollect` produces a `*MergeCommitsError` naming the SHAs and asking
  the developer to rebase. Keep the decision in Go and retain the refusal:
  silently omitting merges would publish a different history.
- **Linked worktrees inherit their main clone's directory-scoped token.**
  Git matches `includeIf "gitdir:…"` against `$GIT_DIR`, which for a linked
  worktree is `<main-clone>/.git/worktrees/<name>`. The mechanism in
  `internal/provision/gitcred.go` therefore cannot assign a separate token
  by the worktree's checkout path. See "Known limitation" in
  `docs/using-sand/secrets.md`.

## VM Ownership and Provenance (read before touching `internal/manage`, `internal/provider`, `internal/registry`)

**Ownership model:** A VM is **managed** (sand-owned) iff it carries a provenance marker on its
host. The local `managed-vms.json` registry (`~/.local/share/sandbar/managed-vms.json`) is
now a **cache + known-targets list + one-release legacy fallback**, NOT the source of truth.
`Scope` (profile identity, e.g. `user@host:22`) groups the UI and keys known targets; it
no longer decides ownership. The authority is the marker. Because the marker lives with the
VM on its host, EVERY controller that can reach the host sees the same managed set — two
laptops driving one Mac mini, or a host's own local sand and a remote client, converge with
no sync protocol.

**Marker contract** (for future Proxmox/cloud implementers):
- **Location:** `<LimaHome>/<name>/sandbar.json` on the host (Lima home directory).
- **JSON schema** — `internal/provider/provenance.go`'s `Provenance` struct
  (`provider.MarkerSchemaVersion`, currently **2**):
  ```json
  {
    "schema": 2,
    "base": "sandbar-base",
    "config": { /* vm.CreateConfig: name, BaseName, CPUs, Memory, Disk, etc. */ },
    "sandbar_version": "0.6.0",
    "created_at": "2026-07-17T12:34:56Z",
    "provisioning": true
  }
  ```
  `provisioning` (v2, `omitempty`) marks an IN-FLIGHT build; a v1 marker has no such key and
  decodes as ready (`false`). Build a marker with `provider.NewProvenance(cfg, provisioning)`
  — it stamps the current schema version and strips secrets (CloneToken never touches disk).
- **Lifecycle:**
  - **Written in-flight on clone (v2):** the provider writes a `provisioning:true` marker the
    moment the clone boots (`limaProvider.Create` sets `provision.CreateOptions.OnCloned`,
    which the provisioner calls at the durable post-clone point), so other controllers show
    the VM **Building** while it provisions — not nothing. A failure before that point deletes
    the instance dir (and marker) with it, leaving no stale claim.
  - **Flipped to ready on success:** `manage.RecordSuccess` calls `provider.Provenancer.MarkManaged`
    with a `provisioning:false` marker, overwriting the in-flight one.
  - **Removed on delete:** the marker file is deleted with the instance directory when
    `provider.Delete` removes the instance. No separate marker cleanup needed.
  - **Adopted on upgrade (one-time, one-release fallback):** `manage.AdoptOnce` runs at most
    once per process per scope. It calls `registry.Adopt` to stamp a (ready) marker onto any
    managed-but-unmarked instance, so upgrading controllers keep pre-provenance VMs. Idempotent
    (repeated calls are a map lookup). After one release, the fallback path
    (`manage.RecreateBase`'s registry query and `board.go`'s legacy gate) can be removed — see
    the "legacy, remove after one release" comments in those files.

**The provider seam:** `provider.Provenancer` (`internal/provider/provenance.go`) is the
interface a backend implements (or inherits) to read and write markers. Today's Lima
implementations (local and remote-over-SSH) satisfy it with `limaprovenance.go`, which
reads/writes the `sandbar.json` sidecar file via the provider's own `HostFiles` handle
(local filesystem or SSH). The Proxmox backend implements the same interface a different
way — `proxmoxprovenance.go` stores a `sandbar` tag plus a fenced JSON block in the VM's
description (no sidecar file), reads the whole fleet's provenance from one tag-filtered
`/cluster/resources` call, and never clobbers operator-authored tags or description text.

**Board status:** a VM carrying an in-flight (`provisioning:true`) marker but no local build
job — i.e. one another controller is building — renders as **Building**, not Running
(`deriveStatus`'s `remoteProvisioning` input, fed from the member's provenance map). The
`lima_home` connection-profile field also scopes the remote `limactl` (discovery), not just
sand's file reads, so discovery and marker reads always resolve the same instance directory.

**Batched read:** Both local and remote providers read all instance markers in one host
round trip via `lima.HostFiles.ReadInstanceMarkers` — no per-instance syscall. The local
implementation scans the filesystem directly; the remote implementation (SSH) walks the
remote Lima home with a shell script and length-frames the results over stdin so JSON
with embedded newlines survives intact (see `internal/lima/sshhost.go`'s `ReadInstanceMarkers`).

## The `sand paste-image` feature: IMAGE-ONLY invariant (read before extending clipboard handling)

The `sand paste-image` command and TUI verb (`v`) stage a host clipboard
image on a guest's single-slot file (`~/.sand/clip/latest.png`) so Claude
Code's native Ctrl-V paste works. **This feature is IMAGE-ONLY by contract
and by construction.** Do not weaken or remove this guarantee:

- **Host-side read (`internal/clipboard`)** gates on an advertised `image/*`
  type before fetching any bytes. A clipboard with no image type yields a
  sentinel and **fetches zero bytes**. Tests assert that a text-only
  clipboard produces the sentinel, never image bytes.
- **Guest-side shims** (`roles/claude-code` `sand-xclip` and `sand-wl-paste`)
  have no write path, no `text/*` branch, and no fallback for non-image
  targets — they refuse anything that is not an image. This is **independent**
  of the host read; the shim cannot be tricked into serving text even if the
  host seam were extended (which it should not be).
- **Do not add a text fallback** to `internal/clipboard` or the guest shims
  under any circumstance. The password-leak surface that makes a live
  clipboard bridge unacceptable is the exact surface this feature closes.
  Text is never sent. If a user wants to paste text into a guest, they attach
  to the shell with `S` and use `cat` or the shell itself.

The clipboard read is one-shot and runs on the machine executing `sand`, not
the remote host (for remote-Lima deployments). Only the image bytes cross
the network.

**This invariant is about the HOST → GUEST direction only.** Copying text the
other way — guest → host, a selection the user makes inside the VM's tmux
reaching their own clipboard over OSC 52 — is a separate, deliberate feature
(`internal/lima.clipboardCmds` plus `set -s set-clipboard on` in
`roles/user/templates/tmux.conf.j2`), and enabling it does not weaken anything
above. The password-leak surface is a host clipboard the guest can read at
will; a copy the user performs inside the guest is neither.

For the security rationale, see the plan's Risk Considerations and the spec
comment at `roles/claude-code/tasks/main.yml`.

## The base image / clone / finalize provisioner (read before touching `internal/provision`)

- **Coding agents belong to individual VMs.** Claude Code, Codex, OpenCode,
  and Pi install current releases during finalize/full, never base. The base
  retains shared runtimes and removes legacy agent installs via
  `agent-cleanup`. Agent selections must not invalidate the v3 dependency
  stamp. The global, secret-free `agent-preferences.json` remembers submitted
  choices across profiles; an absent file may be seeded once from a legacy
  v2 base stamp. Saved all-off is a real preference. Existing VM records keep
  their Claude/Codex booleans (including false); missing OpenCode/Pi means off.
  Reset starts from those recorded VM choices, not global preferences.
- **Reset has one agent-state preservation option.** Keep its path set in
  `internal/provision/staging.go` (`AgentStatePaths`), covering all four
  agents even when deselected. This includes credentials and sessions and
  crosses the host staging boundary, so keep the warning accurate. Restore
  settings without overwriting them with templates; freshly install selected
  executables. Claude-only clipboard/session integrations remain Claude-only.

- **Clones inherit the base image's `lima.yaml` — including its mounts.**
  `limactl clone` copies the base's entire instance directory. The only
  post-clone config write is `Configure` (`internal/lima/client.go`), which
  sets cpus/memory/disk **and strips writable mounts**. This is why the
  read-only playbook mount works inside a clone (finalize rsyncs from
  `/mnt/playbook`), and it is why any writable mount ever added to the base
  builder **must** be stripped from the clone: work VMs run Claude
  unsupervised, and "delete the VM and everything it produced is gone"
  depends on there being no writable host mount. The strip is a **security
  control**, not a tidy-up, and a test enforces it
  (`TestConfigureStripsWritableMountAgainstRealLimactl`). Today the base
  overlay (`internal/provision/overlay.go`) does not add a writable mount at
  all — a writable apt-archive-cache mount was tried and backed out in favour
  of a `limactl copy` seed/harvest (`internal/provision/aptcache.go`) that
  needs no mount — so the strip currently has nothing to remove. It stays
  anyway, as a standing guard. Do not remove it, and do not add a writable
  mount to the clone path believing the base's precedent generalizes to work
  VMs.
- **`playbook_embed.go`'s `go:embed` set and the rsync filter in
  `internal/provision/provision.go` (`inGuestScript`) must stay in step.**
  Both spell out the same fileset — `site.yml`, `ansible.cfg`, `inventory`,
  `roles/`, `group_vars/` — and the base version stamp
  (`internal/provision/baseversion.go`, `playbookFileset`) now hashes exactly
  that fileset too, so a test pinning the embed set to the rsync filter
  (`TestGuestSyncCopiesOnlyThePlaybook`) guards the stamp's correctness as
  well. Add a file to one and forget the other two, and either the guest gets
  content the stamp never sees, or the stamp churns on content the guest
  never gets. **Keep `roles/` as a single embed entry.** Vendoring a Node
  app there previously required listing roles individually to exclude a
  contributor's `node_modules/`, which otherwise enlarged the binary and
  was copied into every VM. Installing the published package avoids that.
  Do not add package-manager output under a role's `files/` directory.
- **Install the published browser review package** (`roles/self-review`).
  The role creates a small `package.json` pinned by `selfreview_version`,
  runs `npm install`, links the CLI onto PATH, and verifies it runs.
  Renovate tracks the version. There is no build step or committed lockfile.
  Keep the npm override until the unused dependency is removed upstream:
  `@self-review/serve` declares `@self-review/react` at runtime, but serves
  a prebuilt client and imports only `@self-review/core` outside Node's
  built-ins. Replacing React with `@self-review/types` avoids its unused
  dependency tree. For the measured release, this reduced the installation
  from 310 MB/320 packages to 17 MB/17 packages. Deleting React afterwards
  would leave dependencies npm installed at the top level. The role's
  verification and `lima-e2e` size and resolution checks must stay: if a new
  release needs React at runtime, the base build must fail. Remove the
  override when upstream moves React to `devDependencies`.
- **Every base mutation belongs inside the base lock held by
  `prepareBaseAndClone`.** Build, in-place re-apply (converge), the 30-day
  refresh, and `--rebuild`'s destroy are all reached through
  `ensureBaseStopped`, called only from inside `prepareBaseAndClone`'s
  `lockBase`/`release` pair — never from a caller that deletes or mutates the
  base on its own. Staleness and age decisions (`baseStale`,
  `baseNeedsRefresh`) must be **read after the lock is acquired**, not cached
  outside it and carried in: a create that queued behind someone else's
  rebuild must see what that rebuild left behind, not act on a verdict formed
  before the wait. This is the easiest property in the codebase to regress —
  a "helpful" refactor that hoists a staleness check above the lock, or adds
  a new way to delete the base, reopens the exact race (`baselock.go`'s doc
  comment; `prepareBaseAndClone`'s doc comment in `provision.go`) this
  machinery exists to close.
- **The `docker` group (and every other package/group grant) happens in the
  BASE phase**, gated `when: provision_phase != 'finalize'` in `site.yml` —
  not in finalize. A clone already has the group in `/etc/group` before it
  ever boots, and every `limactl shell` does a fresh login with a fresh
  `initgroups()`, so finalize needs no bounce to make group membership
  effective. This corrects the folklore that used to justify an unconditional
  post-finalize restart: `createVM` (`internal/provision/provision.go`) now
  bounces the VM only when the guest itself reports
  `/var/run/reboot-required` (a kernel/libc upgrade), and `Reset` warns
  instead of silently destroying a live tmux session before bouncing one.

- **`roles/user`'s `~/.tmux.conf` deploy is deliberately UNGATED**, unlike its
  identity-free neighbours that all carry `when: provision_phase != 'finalize'`.
  Adding that gate looks like a consistency fix and passes every manual test,
  because a VM cloned from a CURRENT base still comes out right. What it breaks
  is a clone taken from an older base (up to 30 days, per the self-refresh):
  it silently keeps whatever config that base was built with, and a stale tmux
  config is wrong rather than broken — no error, just a dead clipboard or a
  missing binding. Re-rendering one small template per clone is the cheaper
  side of that trade. `TestTmuxConfDeployedInEveryPhase` guards it, the way
  `molecule/base` guards the same property for the timezone tasks.

- **Measure reset transfers on the host; show no backup percentage**
  (`internal/provision/stageprogress.go`). Count bytes in the archive being
  written during backup or read during restore. This works on both backends
  without guest tools. Report backup bytes and rate, not an estimated total:
  another directory scan would add work and still would not predict the
  compressed size. Restore can use the completed archive's size. Keep
  progress lines short and put numbers first; the tile may have only 36
  columns and truncates the latest `==>` message.

- **A provisioning failure must say WHICH LAYER failed.** Each phase runs as one
  ssh session to the guest, so an `exit status 255` from it is ssh's own status —
  the connection dropped, or the remote command was killed by a signal — and
  never a status the playbook produced (`set -e` bash around `ansible-playbook`,
  whose failure statuses are 2/4/250). The Proxmox provider annotates that case
  (`transportError`, `internal/provider/proxmox.go`); do not let the bare status
  through, and do not widen the annotation to other statuses — dressing a real
  guest failure up as a transport problem sends the reader away from the actual
  error. Two opt-ins exist because this failure class is otherwise
  undiagnosable: `SAND_KEEP_FAILED` suppresses the partial-VM purge that would
  destroy the guest journal holding the explanation, and `SAND_SSH_DEBUG` writes
  ssh's protocol log to a FILE (`-E`) — never to stderr, which every guest-command
  caller merges into the stream the TUI's progress parser reads. The
  keepalive options in `sshBase` (`internal/lima/sshhost.go`) belong to the same
  story: without them a reaped connection hangs forever instead of failing, and a
  hang carries no evidence at all.

## What a reset preserves (read before touching `internal/provision/preserve.go`)

A reset copies preserved data to the host before deleting the VM, then
restores it to the new clone. `preserve.go` decides what to copy and when to
restore it. Both Lima's `Reset` and Proxmox's `resetInstance` must use these
shared rules.

- **Restore before or after finalize according to what Ansible should
  update.** Restore the Claude login and whole home before finalize so
  Ansible can update its configuration files. Restore the project and
  individually selected checkouts after finalize to avoid overwriting them.
  Omit `project_clone_url` when a checkout will be restored. Reversing this
  order can leave stale configuration or overwrite a preserved checkout.
- **Whole-home preservation includes all other options.** `StagePreserve`
  returns early after staging the home to avoid duplicate archives. It
  still calls `probeProject` so finalize knows whether to skip cloning.
- **Validate preserve paths before deleting the VM.** Guest-supplied
  `PreservePaths` reach `tar -C <home> <rel>` and root's recursive `chown`
  during restore. `preservePathRel` must reject paths outside the guest home
  to prevent traversal such as `..` reaching `/`. A missing path is only a
  notice: checkout paths come from a cache and may no longer exist.
- **Allow tar exit status 1 during backup, but no other nonzero status.**
  Files can change while a running guest is being copied; GNU tar reports
  that as status 1. Status 2 indicates a failure and must remain fatal.
- **Probe compression support in the source guest.** Use zstd when
  available and fall back to gzip for older guests. In the recorded
  benchmark, a 1.3 GB tree took 54 seconds with gzip and 2.5 seconds with
  `zstd -T0 -3`. During restore, `tarDecompressFlag` must read the archive's
  magic bytes to select the format. Do not depend on state saved before
  the guest was deleted. Streaming through stdin requires an explicit
  zstd flag because tar cannot seek to detect it. Name archives `.tar`,
  since `.tgz` would incorrectly imply gzip.
- **Stage archives under `XDG_STATE_HOME` by default.** `/tmp` may use RAM,
  and a large archive may be the only copy after VM deletion. `stageBaseDir`
  honours an explicit `TMPDIR` first and falls back to the system temporary
  directory if the state directory is unavailable. Every test that resets
  with a preserve option must set `TMPDIR` to isolate host state.
- **Exclude `~/.ssh/authorized_keys` from whole-home preservation**
  (`homeExcludes`). Restoring the old file could remove the key needed to
  connect to the rebuilt VM halfway through its reset.
- **Read the reset form's checkouts from the cached registry**
  (`internal/ui/resetpreserve.go`), without contacting the guest. The VM
  may be stopped, and the form must open immediately. Show the cached age
  in each row's help. Pass the recorded absolute path to `ResetOptions`;
  the shortened `~/…` label uses an estimated home and is display-only.
- **Do not assume fixed reset-toggle indices in tests.** The order is
  whole home, Claude settings, project when configured, then checkouts.
  Enabling whole-home preservation must keep the other rows visible so
  the focus and previous choices stay predictable.
- **Whole-home preservation locks included rows without changing their
  saved values** (`formToggle.locked`). Display them checked, skip them in
  `nextOperableToggle`, and restore the user's earlier choices when the
  whole-home option is turned off. Show `lockedToggleSuffix` (` (locked)`)
  as well as dimming the row; colour alone is invisible in monochrome
  terminals and ANSI-stripped golden tests.
- **Keep MAC restoration and DHCP identity configuration together.**
  `generalizeScript` clears `/etc/machine-id` so new clones have distinct
  identities. `roles/base` sets `DUIDType=link-layer` so DHCP identity
  follows the MAC that `proxmoxmac.go` preserves during reset. Removing
  either part can change the lease after a rebuild. The template also
  clears `/etc/hostname` to avoid clones initially announcing
  `sandbar-base`. After the real name is set, use `networkctl renew` to
  announce it. This must not be a hostname-change handler: cloud-init may
  already have set the name, leaving no Ansible change to trigger it. Do
  not use `reconfigure`, which can drop the provisioning SSH connection's
  address.

## VM naming (read before touching `Provider.ValidateName`)

- **Keep naming rules in each backend.** Lima accepts `test_vm` and rejects
  `test--vm`; Proxmox does the reverse. `Provider.ValidateName` delegates to
  `lima.ValidateInstanceName` or `pve.ValidateVMName`. Do not move it into
  `vm.CreateConfig.Validate`, which has no backend context.
- **Check Lima's rule against the real binary.** The rule comes from
  limactl's error message. `TestValidateInstanceNameAgainstRealLimactl`
  supplies an incomplete template: valid names reach template validation,
  while invalid names fail earlier. It creates no VM and skips if limactl
  is absent. Exclude length from that comparison: limactl's limit depends
  on the length of `<LIMA_HOME>/<name>/ssh.sock.<16 digits>` relative to
  `UNIX_PATH_MAX`. `lima.MaxInstanceNameLen` provides a fixed cap in sand.
- **Validate only new VM names.** `submitForm` and `sand create`'s
  `checkBackendName` run the check. `submitReset`, `sand reset`, and
  `sand create --recreate` skip it because the VM exists and its name
  cannot be edited. Rechecking would prevent rebuilding older VMs whose
  names no longer pass. Keep `checkBackendName` separately testable so
  tests cover this exception.
- **Keep validation free of I/O.** It runs while submitting the create
  form. API calls or limactl processes could block the TUI. Connectivity
  belongs in `Preflight`; `TestValidateNameMakesNoCalls` enforces this.
- **Explain the invalid character or format and name the backend.** A
  generic API error or raw regular expression is hard to act on. Return
  Proxmox's validation error without another `proxmox:` prefix, since it
  already names Proxmox and the form has limited room for help text.

## Conventions

- **Commits use [Conventional Commits](https://www.conventionalcommits.org)**
  (`feat:`, `fix:`, `test:`, `ci:`, `docs:`, `chore:`, scopes like
  `fix(reset):`). Releases are automated by release-please, which parses them.
- **A commit message must not reference a plan, phase, or task** — no "plan 17
  phase 3", no "task 04". The one exception is a commit whose changes are
  confined to `.ai/strikethroo/`, where the plan *is* the subject. Commit
  messages are read years later by people who have no access to the planning
  artefact and no reason to want one; the message has to stand on its own, and
  release-please copies the subject line verbatim into `CHANGELOG.md`, where a
  phase number is pure noise. Say what changed and why instead. The same rule
  applies to code comments (see below).
- **Code comments must not reference plan documents either.** A comment
  pointing at "plan 17, Component 2" is a dangling link the moment the plan is
  archived, and it substitutes a pointer for the reason the reader actually
  needs. Inline the rationale.
- Match the surrounding code's comment density and idiom — this codebase favours
  explanatory comments on the *why*, not the *what*.
- When you change TUI rendering, update the affected goldens (`-update`) and
  confirm the text diff is the change you intended.
