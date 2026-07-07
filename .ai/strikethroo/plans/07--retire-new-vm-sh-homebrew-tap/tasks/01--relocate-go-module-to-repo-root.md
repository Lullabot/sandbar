---
id: 1
group: "sand-binary"
dependencies: []
status: "completed"
created: 2026-07-06
model: "sonnet"
effort: "medium"
skills:
  - go
complexity_score: 6
complexity_notes: "Mechanical but broad — 41 import references across 25 files plus go.mod/go.sum, cmd, internal, and README all move up one level. A missed import fails to compile, so the toolchain verifies completeness, but the breadth demands care."
---
# Relocate the Go module from `tui/` to the repo root

## Objective
Move the Go module up from `tui/` to the repository root and change its module
path from `github.com/lullabot/sandbar/tui` to `github.com/lullabot/sandbar`,
rewriting every import to drop the `/tui` segment. This puts the module and the
repo-root playbook (`site.yml`, `roles/`, `group_vars/`, …) in the same tree so
a later task can `go:embed` the playbook — which `go:embed` cannot do while the
module lives below the playbook. The relocation is purely structural: no
behavior changes.

## Skills Required
- **go** — Go module layout (`go.mod` module path), package imports, and using
  the compiler + test suite to prove an exhaustive mechanical rewrite is
  complete.

## Acceptance Criteria
- [ ] `go.mod` and `go.sum` live at the repo root; `go.mod`'s first line is
      `module github.com/lullabot/sandbar` (no `/tui` suffix).
- [ ] `cmd/`, `internal/`, and `tui/README.md` have moved from `tui/` up to the
      repo root; the now-empty `tui/` directory is gone.
- [ ] Every import path is rewritten to drop `/tui`. Verify:
      `grep -rn "lullabot/sandbar/tui" . --include='*.go'` prints nothing.
- [ ] From the repo root, `go build ./cmd/sand` succeeds and produces a `sand`
      binary.
- [ ] From the repo root, `go test ./...` passes (the pre-existing
      `limae2e`-gated tests stay excluded — they require `-tags limae2e`).
- [ ] The relocated `README` still renders and its build/run instructions point
      at the repo-root paths (`go build ./cmd/sand`), not `tui/…`.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Current module: `module github.com/lullabot/sandbar/tui` (Go 1.24.2,
  toolchain go1.24.4) rooted at `tui/`.
- 41 import references across 25 `.go` files reference the `…/sandbar/tui`
  prefix (packages `lima`, `provision`, `ui`, `vm`, `registry`, `browse`).
- Files to move up one level: `tui/go.mod`, `tui/go.sum`, `tui/cmd/…`,
  `tui/internal/…`, `tui/README.md`, and the hidden `tui/.claude/` if present.
- No dependency versions change — this is a path move only.

## Input Dependencies
None. This is the first task; it unblocks all others.

## Output Artifacts
- A repo-root Go module (`github.com/lullabot/sandbar`) that builds and tests
  clean, with `cmd/sand`, `internal/…`, `go.mod`, `go.sum`, and `README` at the
  root. This is the foundation the embed (task 2), headless CLI (task 3),
  release automation (task 4), and CI migration (task 5) all build on.

## Implementation Notes
This is a mechanical rewrite; lean on the Go toolchain as the oracle — a missed
`/tui` import will not compile, and a green `go test ./...` proves nothing was
dropped.

<details>
<summary>Step-by-step</summary>

1. From the repo root, move the module contents up out of `tui/`:
   - `git mv tui/go.mod go.mod`
   - `git mv tui/go.sum go.sum`
   - `git mv tui/cmd cmd`
   - `git mv tui/internal internal`
   - `git mv tui/README.md README-sand.md` **or** move it to `docs/` — pick one
     landing spot; the removal task (task 6) later rewrites its "Relationship to
     new-vm.sh" section, so just make sure it survives the move with its content
     intact. (Do **not** clobber the existing top-level `README.md`, which is the
     project quick-start.)
   - Move `tui/.claude/` up too if it exists, then remove the empty `tui/`
     directory (`rmdir tui` / `git rm` any leftovers).
2. Edit `go.mod`: change the first line from
   `module github.com/lullabot/sandbar/tui` to
   `module github.com/lullabot/sandbar`. Leave the `go`/`toolchain` and
   `require` blocks untouched.
3. Rewrite every import. The prefix `github.com/lullabot/sandbar/tui/internal/…`
   becomes `github.com/lullabot/sandbar/internal/…`. A safe in-place rewrite:
   ```sh
   grep -rl 'lullabot/sandbar/tui' --include='*.go' . \
     | xargs sed -i 's#github.com/lullabot/sandbar/tui/#github.com/lullabot/sandbar/#g'
   ```
   Then re-run `grep -rn "lullabot/sandbar/tui" . --include='*.go'` and confirm
   zero matches (this also catches the module path itself if any doc comment
   embeds it).
4. Verify from the repo root:
   - `go build ./cmd/sand` → produces `./sand`.
   - `go test ./...` → all packages pass.
   - `go vet ./...` → clean (optional but cheap).
5. Update the relocated README's build/run commands to the repo-root form
   (`go build ./cmd/sand`, run `./sand`). Do **not** rewrite the new-vm.sh
   narrative here — that is task 6's job; just fix paths so nothing says
   `cd tui`.
6. Do not attempt the `go:embed` work here — that is task 2. This task ends when
   the module builds and tests green from the root.

Note: `go.sum` needs no regeneration (same deps); if `go build` complains,
`go mod tidy` from the root will reconcile it, but it should not be necessary.
</details>
