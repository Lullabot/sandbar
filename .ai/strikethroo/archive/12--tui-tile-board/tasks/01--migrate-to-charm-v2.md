---
id: 1
group: "foundation"
dependencies: []
status: "completed"
created: 2026-07-12
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Mechanically broad rather than conceptually hard: touches every file in internal/ui plus ~90 tests and cmd/sand/main.go. The risk is a behavioural change hiding inside regenerated golden noise."
skills:
  - go
  - bubble-tea
---
# Migrate the TUI to Charm v2 (bubbletea, bubbles, lipgloss, teatest)

## Objective

Move the whole application from Charm v1 to v2 — `bubbletea/v2`, `bubbles/v2`,
`lipgloss/v2`, and `x/exp/teatest/v2` — as an **isolated, green checkpoint with no
redesign work in the tree**. When the board later misbehaves, the migration must not
be a suspect.

## Skills Required

`go`, `bubble-tea` — the v1→v2 API migration (message types, key handling, renderer
and style semantics, component model changes in `spinner`, `table`, `textarea`,
`textinput`, `viewport`, `help`).

## Acceptance Criteria

- [x] `go.mod` requires `github.com/charmbracelet/bubbletea/v2` (v2.0.8+),
      `bubbles/v2` (v2.1.1+), `lipgloss/v2` (v2.0.5+), and
      `github.com/charmbracelet/x/exp/teatest/v2`. No v1 Charm module remains in the
      `require` block or in `go.sum` as a direct dependency.
- [x] `grep -rn "charmbracelet/\(bubbletea\|bubbles\|lipgloss\)\"" --include=*.go .`
      returns **zero** hits (i.e. no unversioned/v1 import paths anywhere).
- [x] `go build ./...` and `go vet ./...` pass.
- [x] `go test ./...` passes — **all** existing tests, including the ~90 in
      `internal/ui`. Test key-event construction is updated to v2's key API.
- [x] The four goldens (`TestTUIListView`, `TestTUIDetailView`, `TestTUIDeleteConfirm`,
      `TestTUISecretsPanelEmpty`) are regenerated with `go test ./internal/ui -update`,
      **and the diff is reviewed and explained in the task's completion report**: every
      changed line is renderer/spacing noise, not a behavioural change. Paste the diff.
- [x] `TestTUINewFormAcceptsTyping` (behavioural, no golden) still passes — it is the
      canary that a v2 regression cannot rubber-stamp.
- [x] `go test -race ./...` passes.
- [x] **No redesign work is included.** The board, tiles, heartbeat, job registry, and
      command registry are all out of scope for this task. The only intended
      user-visible change is none.

## Technical Requirements

- Entry point: `cmd/sand/main.go:64` — `tea.NewProgram(ui.New(cli, prov), tea.WithAltScreen())`.
  v2 renames/reshapes program options and the `tea.Model` interface (`Init`/`Update`/`View`
  signatures changed; `View() string` may become `View() fmt.Stringer` depending on the
  release — follow the actual v2.0.8 API, do not guess).
- `internal/ui/model.go` — `tea.WindowSizeMsg`, `tea.KeyMsg`, `spinner.TickMsg`.
  In v2, key messages split into `tea.KeyPressMsg` / `tea.KeyReleaseMsg`; the ctrl+c
  interception and every `updateX(msg tea.KeyMsg)` signature changes with it.
- `internal/ui/styles.go` — v2 lipgloss changes colour construction and introduces an
  explicit renderer/colour-profile model. Keep the existing ANSI-256 indices and the
  existing style names; this task must not restyle anything.
- Tests construct keys as `tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}` in many
  places — this pattern changes. Consider a small test helper so the ~90 call sites
  change once rather than ad hoc.
- `internal/ui/teatest_test.go` — swap to `x/exp/teatest/v2` (untagged; resolve with
  `go get github.com/charmbracelet/x/exp/teatest/v2@latest`). Keep the harness
  conventions intact: `t.Setenv("XDG_DATA_HOME", t.TempDir())`,
  `teatest.WithInitialTermSize(100, 30)`, `ansi.Strip` snapshots, the `listFakeRunner`.

## Input Dependencies

None. This is the first task and the branch is already rebased onto `main`.

## Output Artifacts

- A fully v2 codebase with a green suite — the foundation every other task builds on.
- The regenerated goldens.
- A note in the completion report on any v2 API that behaves differently and will
  matter to the board (notably: the cell-diffing renderer and `PlaceOverlay` in the
  compositor, which later tasks depend on).

## Implementation Notes

<details>
<summary>Guidance</summary>

Do this as a mechanical, whole-tree migration in one pass; do not try to run v1 and v2
side by side.

Order that tends to work: `go get` the four modules → fix import paths → fix
`cmd/sand/main.go` → fix `internal/ui/*.go` until `go build ./...` passes → fix the
tests → regenerate goldens last, once the build and the behavioural tests are green.
Regenerating goldens **before** the behavioural tests pass is how a real regression gets
baked into a golden.

Read the actual v2 API from the module source in the module cache rather than relying on
memory of v1. `go doc github.com/charmbracelet/bubbletea/v2` and reading
`$(go env GOMODCACHE)/github.com/charmbracelet/bubbletea/v2@v2.0.8/` is faster than
guessing and then chasing compile errors.

The golden diff review is the point of this task, not a formality. The plan's stated
risk is exactly "the v2 migration hides a behavioural change inside renderer noise". If
a golden diff shows a *field disappearing*, a *label changing*, or a *row reordering* —
that is not renderer noise. Stop and investigate.

Per `PRE_TASK_EXECUTION.md`: this task writes no new tests (the existing suite is the
test), so state that explicitly and rely on the existing suite as the RED→GREEN signal —
it starts red (does not compile against v2) and must end green.
</details>
