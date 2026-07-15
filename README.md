# sandbar

`sand` is a single Go binary that provisions disposable Claude Code
development VMs on [Lima](https://lima-vm.io). Spin up an isolated, fully
provisioned VM in seconds, point Claude Code at a repository, and throw the
VM away when you're done.

**For full documentation, visit
[https://lullabot.github.io/sandbar/latest/](https://lullabot.github.io/sandbar/latest/)**

## Install

```bash
brew install lullabot/sandbar/sand
```

That's it — no Ansible, no Go toolchain, and no clone of this repository
required. Homebrew pulls in [Lima](https://lima-vm.io) as a dependency.

## Quick start

Open the interactive TUI board:

```bash
sand
```

Press `n` to create a VM, then `S` on its tile for a shell. Or drive it
headlessly:

```bash
sand create
sand shell claude
```

See [Getting Started](https://lullabot.github.io/sandbar/latest/getting-started/)
for the full walkthrough, or the
[CLI Reference](https://lullabot.github.io/sandbar/latest/using-sand/cli-reference/)
for every command and flag.

## Development

Building from a checkout, running tests, and how `sand` embeds and runs
its Ansible provisioner are covered in [AGENTS.md](AGENTS.md) and the
[Contributing](https://lullabot.github.io/sandbar/latest/contributing/development/)
docs.

## Testing

```sh
go test ./...                       # fast unit + integration suite (no VM needed)
go test ./... -race                 # the same, with the race detector (what CI runs)
go test -tags limae2e ./...         # real-VM e2e — needs a host with Lima + KVM
```

**Coverage.** CI's `unit` job measures coverage over `./internal/...` (the
`cmd/sand` entrypoint glue is excluded) and fails if it drops below a floor
committed in `.github/workflows/test.yml` (`COVERAGE_FLOOR`). It's a manual
ratchet — no third-party service; the run uploads an HTML report as a build
artifact. To reproduce the gate locally:

```sh
go test ./... -race -covermode=atomic -coverpkg=./internal/... -coverprofile=coverage.out
go tool cover -func=coverage.out | tail -1     # the gated total
go tool cover -html=coverage.out               # browse it
```

**Mutation testing** (advisory, core packages) runs weekly in CI and on demand.
Locally:

```sh
go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.5.0
gremlins unleash --config .gremlins.yaml ./internal/provision/...
```

**Ansible role tests.** `molecule/` holds converge/verify scenarios for the
`base` and `samba` roles, run in a systemd-capable Debian container (weekly / on
demand in CI). The other roles are a documented follow-up. Locally, with
`molecule` + Docker installed: `molecule test -s base` (samba's idempotence
stage has a known failure — see AGENTS.md).
