---
id: 4
group: "distribution"
dependencies: [2, 3]
status: "pending"
created: 2026-07-06
model: "sonnet"
effort: "medium"
skills:
  - goreleaser
  - github-actions
complexity_score: 6
complexity_notes: "Release tooling: a root .goreleaser.yaml + tag-triggered workflow + version ldflags wiring + Homebrew publisher config. Single domain, but depends on human-provisioned tap repo and token, and must be validated on a pre-release tag."
---
# Release automation via GoReleaser + a Homebrew tap

## Objective
Ship `sand` as a one-command install on macOS and Linux. Add a root-level
`.goreleaser.yaml` and a tag-triggered release workflow so that pushing a
version tag cross-compiles `sand` for darwin/linux × amd64/arm64, stamps the
version, publishes the binaries to GitHub Releases, and — via GoReleaser's
Homebrew publisher — updates the formula in `lullabot/homebrew-sandbar`
(declaring `depends_on "lima"`) so users run
`brew install lullabot/sandbar/sand`.

## Skills Required
- **goreleaser** — `.goreleaser.yaml` builds, archives, `brews:` (Homebrew tap)
  publisher, and ldflags version stamping.
- **github-actions** — a tag-triggered release workflow consuming the scoped tap
  token secret.

## Acceptance Criteria
- [ ] A root-level `.goreleaser.yaml` builds `main: ./cmd/sand`, `binary: sand`,
      for `goos: [darwin, linux]` × `goarch: [amd64, arm64]` with
      `CGO_ENABLED=0`.
- [ ] The version is stamped via ldflags (`-X main.version={{.Version}}`); a
      `var version = "dev"` exists in `cmd/sand/main.go` and `sand --version`
      (or `sand version`) prints the stamped value.
- [ ] A `brews:` block publishes to the `lullabot/homebrew-sandbar` tap using
      the `HOMEBREW_TAP_GITHUB_TOKEN` secret, and the generated formula declares
      `depends_on "lima"`.
- [ ] A tag-triggered release workflow (e.g. `.github/workflows/release.yml`,
      `on: push: tags: ['v*']`) checks out the repo, sets up Go, and runs
      `goreleaser release` with the tap token in its environment.
- [ ] `goreleaser check` passes on the config
      (`go run github.com/goreleaser/goreleaser/v2@latest check` or the pinned
      action), and a dry `goreleaser release --snapshot --clean` builds all four
      artifacts locally without publishing.
- [ ] The plan's human prerequisites are documented (not performed) for the
      release maintainer: create the empty `lullabot/homebrew-sandbar` repo and
      provision a scoped `HOMEBREW_TAP_GITHUB_TOKEN` Actions secret with push
      access to it.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- After task 1 the module is at the repo root (`github.com/lullabot/sandbar`) and
  the binary is `./cmd/sand`; GoReleaser builds from there. After task 2 the
  playbook is embedded, so the released binary is self-sufficient (no checkout).
- All dependencies are pure Go (see `go.mod`), so `CGO_ENABLED=0` is safe and
  gives static cross-compiled binaries.
- Version stamping: add `var version = "dev"` in package `main`
  (`cmd/sand/main.go`) and a `--version`/`version` handler that prints it;
  GoReleaser overrides it at build time via `ldflags: -X main.version={{.Version}}`.
- The Homebrew publisher (`brews:` in GoReleaser v2) needs: the target tap repo
  (`owner: lullabot`, `name: homebrew-sandbar`), the token env
  (`HOMEBREW_TAP_GITHUB_TOKEN`), and a `dependencies: [{ name: lima }]` entry so
  the formula emits `depends_on "lima"` (the TUI/preflight shells out to
  `limactl`).
- **Human prerequisites (do not perform; document + consume):** the tap repo and
  the scoped token are provisioned by a human, mirroring Plan 06's
  org-transfer framing. This task wires GoReleaser/CI to *consume* them; it does
  not create org repos or mint tokens.
- Validate on a pre-release tag (e.g. `v0.0.0-rc1`) before the first real
  release; keep `--snapshot` runnable for local verification without a tag.

## Input Dependencies
- **Task 1** (module at repo root) — required: GoReleaser builds `./cmd/sand` at
  the root module path.
- **Task 2** (embedded playbook) — required for a *usable* released binary: a
  brew-installed `sand` has no checkout, so it must carry the embedded playbook.
- **Task 3** (`sand create`) — required for the released binary to be useful
  headless (the reason a Go-free user installs it).

## Output Artifacts
- `.goreleaser.yaml`, a tag-triggered release workflow, and version stamping.
  Produces published darwin/linux amd64+arm64 artifacts and an auto-updated tap
  formula — the `brew install lullabot/sandbar/sand` install path. The tap's
  availability is a precondition for the script removal (task 6).

## Implementation Notes
This is config plus a tiny code change (the `version` var). Keep it minimal —
GoReleaser's defaults cover archives/checksums; only override what the plan
requires (targets, CGO off, ldflags, the `brews:` tap with `depends_on lima`).

<details>
<summary>Step-by-step</summary>

1. Add `var version = "dev"` to `cmd/sand/main.go` and a version handler:
   `sand --version` (and/or `sand version`) prints `version`. Keep it before the
   TUI/`create` dispatch so it works without limactl.
2. Create `.goreleaser.yaml` at the repo root (GoReleaser v2 schema):
   ```yaml
   version: 2
   builds:
     - id: sand
       main: ./cmd/sand
       binary: sand
       env: [CGO_ENABLED=0]
       goos: [darwin, linux]
       goarch: [amd64, arm64]
       ldflags:
         - -s -w -X main.version={{.Version}}
   archives:
     - id: sand
       formats: [tar.gz]
   brews:
     - name: sand
       repository:
         owner: lullabot
         name: homebrew-sandbar
         token: "{{ .Env.HOMEBREW_TAP_GITHUB_TOKEN }}"
       dependencies:
         - name: lima
       description: "Headless + TUI manager for Claude Code development VMs (Lima)"
   ```
   Adjust field names to the installed GoReleaser v2 version if `goreleaser check`
   flags anything.
3. Add `.github/workflows/release.yml`:
   ```yaml
   name: release
   on:
     push:
       tags: ['v*']
   permissions:
     contents: write
   jobs:
     goreleaser:
       runs-on: ubuntu-24.04
       steps:
         - uses: actions/checkout@v4
           with: { fetch-depth: 0 }
         - uses: actions/setup-go@v5
           with: { go-version-file: go.mod }
         - uses: goreleaser/goreleaser-action@v6
           with: { version: '~> v2', args: release --clean }
           env:
             GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
             HOMEBREW_TAP_GITHUB_TOKEN: ${{ secrets.HOMEBREW_TAP_GITHUB_TOKEN }}
   ```
4. Verify locally without publishing: `goreleaser check` (config valid) and
   `goreleaser release --snapshot --clean` (builds all four
   darwin/linux×amd64/arm64 archives under `dist/`). Confirm a built binary
   reports the snapshot version.
5. Document the human prerequisites for the release maintainer (in the release
   workflow header comment and/or the release/tap docs): create
   `lullabot/homebrew-sandbar`; add a scoped `HOMEBREW_TAP_GITHUB_TOKEN` secret
   with push to that repo. Note that the first real release should be preceded by
   a pre-release tag to validate the publisher end to end.
6. Do not delete `new-vm.sh`/`install.sh` here (task 6) and do not touch
   `test.yml` (task 5).
</details>
