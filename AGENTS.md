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

### Go package layout (`internal/`)

- `lima` — typed wrapper over the `limactl` CLI. All subprocess execution goes
  through a `Runner` interface so code is testable without a real binary.
- `provision` — orchestrates create/reset (base build, `limactl clone`,
  finalize) and the Ansible run; `staging.go` moves data across a reset.
- `ui` — the Bubble Tea model, views, and commands (list/detail/form/secrets/…).
- `secrets`, `registry`, `manage`, `browse`, `vm` — host-side secrets store,
  managed-VM index, shared registry bookkeeping, file browser, domain types.

Entrypoint: `cmd/sand/main.go`. There is a headless `sand create` path
(`internal/manage`) and the TUI path; keep them from drifting — both go through
the same `provision`/`registry` seams by design.

## Build, run, format

```
go build ./cmd/sand      # build the binary
go run ./cmd/sand        # run the TUI
gofmt -l .               # must be empty; format before committing
go vet ./...
```

There is no Makefile.

## Testing

```
go test ./...                                  # unit + integration (no VM needed)
go test ./internal/ui -run TestTUI -update     # regenerate TUI golden snapshots
go test -tags limae2e ./...                    # real-VM e2e (needs limactl + KVM)
```

Conventions:

- **No test may require a real `limactl`.** Use a fake `lima.Runner` (see the
  `fakeRunner`/`listFakeRunner` types in the `*_test.go` files) that returns
  canned output. `New`/model construction takes a `*lima.Client` built over the
  fake.
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

## CI (`.github/workflows/test.yml`)

Three jobs:

- `lint` — Ansible syntax check.
- `unit` — `go vet ./...` and `go test ./...` (fast, no VM).
- `lima-e2e` — builds `sand` and provisions a real Lima VM end to end under
  QEMU+KVM on the hosted runner. (~6 min; it does not run the Go test suite —
  that's the `unit` job.)

**Triggers:** `push` only on `main`, plus `pull_request` and
`workflow_dispatch`. A plain feature-branch push therefore runs **no** CI.

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
  **The header band's hidden count is what keeps this honest** (`headerCounts`
  in `internal/ui/header.go`) — do not remove it; it is the entire mitigation
  the plan accepted for making a stale, multi-gigabyte base image invisible
  from the TUI. `X` (stop all) still means *every managed VM*, not the ones a
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
- **`beginStream` starts a job; it does not choose a screen.** Which view a run
  lands on belongs to the caller, and the two callers want opposite things: a
  **build returns to the board**, where its tile carries the badge and the
  progress bar (its log is one `l` away), while a **transfer opens its log**,
  having no tile bar of its own. Flipping the view inside `beginStream` is how
  every run ended up seizing the terminal with a full-screen Ansible dump —
  the takeover the job registry exists to end. The suite did not catch it
  because the suite asserted it; do not reintroduce either.
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

## Conventions

- **Commits use [Conventional Commits](https://www.conventionalcommits.org)**
  (`feat:`, `fix:`, `test:`, `ci:`, `docs:`, `chore:`, scopes like
  `fix(reset):`). Releases are automated by release-please, which parses them.
- Match the surrounding code's comment density and idiom — this codebase favours
  explanatory comments on the *why*, not the *what*.
- When you change TUI rendering, update the affected goldens (`-update`) and
  confirm the text diff is the change you intended.
