# AGENTS.md

Guidance for AI coding agents working in this repository. Keep it accurate as
the project evolves.

## What this is

`sand` is a tool for spinning up disposable Claude Code development VMs. It has
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
  sidecar marker. There is no
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
  not directly on `Provider`.
- `registry` — managed-VM index, now `(connection scope, name)`-keyed
  (schema v3, auto-migrated on read). Each entry's connection `Scope` is
  derived from which profile's provider created it (`LocalScope` for local
  Lima, a remote identity like `user@host:port` for remote), so the same VM
  name can exist independently under two different profiles and a remote
  profile's VMs never mix with the local list.
- `ui` — the Bubble Tea model, views, and commands (board/form/secrets/progress/
  profile-management/…).
- `secrets`, `manage`, `browse`, `vm` — host-side secrets store (schema v3,
  now also keyed by connection scope — distinct from its pre-existing
  per-directory scope, see `docs/reference/files-and-state.md`), shared
  registry bookkeeping, file browser, domain types.

Entrypoint: `cmd/sand/main.go`. There are three paths: a headless `sand create`
(`internal/manage`), the TUI, and a standalone `sand shell` (`cmd/sand/shell.go`);
keep them from drifting — the create/TUI paths construct their provider(s) via
`provider.BuildFleet` over the `profiles` store's enabled profiles (a headless
command binds only the one profile it targets; the TUI binds every enabled
one), and both shell entrypoints (the TUI's `S` verb and `sand shell`)
construct their guest-attach command exclusively via `provider.AttachArgv()`,
the one place in sand that knows tmux exists (for local Lima) or SSH (for remote).

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

- `lint` — Ansible syntax check.
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
  run the fast Go suite — that's the `unit` job.)
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

For the security rationale, see the plan's Risk Considerations and the spec
comment at `roles/claude-code/tasks/main.yml`.

## The base image / clone / finalize provisioner (read before touching `internal/provision`)

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
  never gets.
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
