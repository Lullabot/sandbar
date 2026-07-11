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

## Conventions

- **Commits use [Conventional Commits](https://www.conventionalcommits.org)**
  (`feat:`, `fix:`, `test:`, `ci:`, `docs:`, `chore:`, scopes like
  `fix(reset):`). Releases are automated by release-please, which parses them.
- Match the surrounding code's comment density and idiom — this codebase favours
  explanatory comments on the *why*, not the *what*.
- When you change TUI rendering, update the affected goldens (`-update`) and
  confirm the text diff is the change you intended.
