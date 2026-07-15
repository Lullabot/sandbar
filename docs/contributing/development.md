# Development

How to build, test, and iterate on `sand` locally.

## Repository layout

`sand` has two halves that share one repo:

- **A Go TUI/CLI** (`cmd/sand`, `internal/…`) that drives [Lima](https://lima-vm.io)
  to create, clone, reset, and manage VMs, plus a host-side secrets store.
- **An Ansible provisioner** (`site.yml`, `roles/…`, `group_vars/`) that
  configures a VM once it boots. The Go side embeds and runs it — see
  [The Embedded Playbook](ansible-playbook.md) for how that actually works.

Inside `internal/`:

| Package | What it does |
|---|---|
| `lima` | Typed wrapper over the `limactl` CLI. All subprocess execution goes through a `Runner` interface so code is testable without a real binary. |
| `provision` | Orchestrates create/reset (base build, `limactl clone`, finalize) and the Ansible run; `staging.go` moves data across a reset. |
| `ui` | The Bubble Tea model, views, and commands (board/form/secrets/progress/…). |
| `secrets`, `registry`, `manage`, `browse`, `vm` | Host-side secrets store, managed-VM index, shared registry bookkeeping, file browser, domain types. |

Entrypoint: `cmd/sand/main.go`. There are three paths: a headless `sand create`
(`internal/manage`), the TUI, and a standalone `sand shell`
(`cmd/sand/shell.go`).

### Why the `limactl` CLI, not a Go API

Lima is written in Go, but it doesn't publish a stable public Go API — its
`pkg/…` packages are internal and change between releases, and importing
them would pull Lima's whole dependency tree into `sand` and pin it to a
single Lima version. `internal/lima` instead wraps the `limactl` CLI itself,
using its structured output (`--format json` for `list`, `--format
'{{ .Field }}'` templates for single values) as the supported, documented
integration surface. Because `limactl` logs to **stderr**
(`time=… level=… msg=…` lines) and writes its JSON/template output to
**stdout**, the runner captures the two streams separately — only stdout is
parsed, stderr is surfaced as diagnostics on failure — and the list parser
skips any stdout line that isn't a JSON object, so a stray notice degrades
to "ignored" rather than failing the listing.

## Build, run, format, vet

```
go build ./cmd/sand      # build the binary
go run ./cmd/sand        # run the TUI
gofmt -l .               # must print nothing; format before committing
go vet ./...
```

There is deliberately **no Makefile**, and no Node/npm toolchain anywhere in
this repository — including for the docs, below. A contributor who assumes
`make build` works should be corrected here rather than by a failing command.

## Testing

```
go test ./...                                  # unit + integration, no VM needed
go test ./internal/ui -run TestTUI -update     # regenerate TUI golden snapshots
go test -tags limae2e ./...                    # real-VM end-to-end (needs limactl + KVM)
```

`go test ./...` covers unit tests and the `teatest`-based TUI golden
snapshots and never boots a real VM. Real-VM end-to-end tests are gated
behind the `limae2e` build tag, so plain `go test ./...` skips them; run them
locally on a host with Lima (or dispatch the `test.yml` workflow, whose
`lima-e2e` job runs them under QEMU+KVM).

## Docs

The documentation site (the one you are reading) is built with MkDocs
Material, invoked entirely through [`uv`](https://docs.astral.sh/uv/)'s
`uvx` — no global Python, no virtualenv, and no Node toolchain:

```
uvx --with-requirements docs/requirements.txt mkdocs serve          # live preview
uvx --with-requirements docs/requirements.txt mkdocs build --strict # check
```

`--strict` is the only quality gate this toolchain offers — it fails the
build on any broken link or nav entry pointing at a missing page — and it
runs on every pull request. A contributor changing docs should run it
locally before pushing.

## CI

`.github/workflows/test.yml` triggers on `push` to `main`, on
`pull_request`, and on `workflow_dispatch`. A plain feature-branch push runs
**no** CI by itself — only once a pull request exists, or via an explicit
dispatch:

```
gh workflow run test.yml --ref <branch>
gh run list --workflow test.yml --branch <branch> --limit 1   # get the run id
gh run watch <id> --exit-status
```

That dispatch run is distinct from the `pull_request` run that fires (on the
same SHA) once a PR is opened against the branch.

## Conventions

- Commits use [Conventional Commits](https://www.conventionalcommits.org)
  (`feat:`, `fix:`, `test:`, `ci:`, `docs:`, `chore:`, scopes like
  `fix(reset):`). Releases are automated by release-please, which parses
  them — see [Releases](releases.md).
- Match the surrounding code's comment density and idiom: this codebase
  favours explanatory comments on the *why*, not the *what*.
- When you change TUI rendering, update the affected golden snapshots
  (`-update`) and confirm the text diff is the change you intended.

## Regenerating the home-page screenshot

The board image on the site's home page lives at `docs/images/board.png`. The
committed copy was rendered headlessly from the TUI's wide-board test fixture
with [`freeze`](https://github.com/charmbracelet/freeze), so its CPU and memory
bars read `—` — there is no live VM behind a fixture. To reshoot it with real
values from your own machine, run the [VHS](https://github.com/charmbracelet/vhs)
tape next to the image:

```
cd docs/images && vhs board.tape
```

That drives a real `sand` session, so keep a VM or two around first, or the
board will be empty.
