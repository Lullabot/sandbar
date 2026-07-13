---
id: 3
group: "board-foundations"
dependencies: [1]
status: "completed"
created: 2026-07-12
model: "sonnet"
effort: "medium"
skills:
  - go
  - terminal-ui
---
# The layout classifier: one classify(w, h) → layoutMode, no magic offsets

## Objective

Make responsiveness a **single, testable decision** instead of a scatter of magic
offsets. One function maps the terminal's dimensions to a layout mode; every pane derives
its size from that mode's budget. It runs once per `WindowSizeMsg` and nowhere else.

The contract is absolute: **the classifier always returns a renderable mode.** There is no
terminal size at which `sand` shows a "terminal too small" wall.

## Skills Required

`go`, `terminal-ui` — responsive layout budgeting for a fixed-cell grid.

## Acceptance Criteria

- [x] A new `internal/ui/layout.go` defines `func classify(w, h int) layoutMode` and a
      `layoutMode` type carrying the derived budgets every pane needs: tile columns, tile
      width/height, grid viewport height, whether the header band is full or compact,
      whether the messages strip is shown, and the footer's height.
- [x] The magic offsets are **gone**: `grep -rn "width-6\|width-8\|h - secretsChrome\|secretsChrome" --include=*.go internal/ui`
      returns zero hits. `secretsChrome = 16` (duplicated between `secrets.go` and
      `model.go`'s `WindowSizeMsg` handler) is deleted in favour of a mode-derived budget.
- [x] `classify` is called from exactly one place — the `WindowSizeMsg` handler. Assert this
      by grep: exactly one call site.
- [x] The modes shed the least-essential surface first, in this order as size contracts:
      multi-column grid → single column; full header band → compact counts; **messages strip
      is the first pane to go**; the footer and the grid never go.
- [x] **Table-driven test** over a spread of sizes — at minimum 80×24, 100×30, 120×40,
      200×60, and pathological small (40×10, 20×5) — asserting for each that: every derived
      budget is `> 0`, the sum of pane heights does not exceed `h`, tile width does not
      exceed `w`, and the mode is renderable. **No size returns an error or a "too small"
      sentinel.**
- [x] At 80×24 the mode yields a **single tile column** and a still-navigable board.
- [x] `go test ./...` passes.

## Technical Requirements

- Today's offsets to replace, from `model.go`'s `WindowSizeMsg` handler:
  `viewport` = `(w-8, h-12)`, `table` = `(w-6, h-12)`, secrets textarea = `h - secretsChrome`,
  plus `contentWidth() = width-4` (floor 20) in `model.go`.
- `appStyle` has `Padding(1, 2)` — the horizontal chrome budget must account for it rather
  than hardcoding `-4` at each call site.
- The tile's content is six lines at most (title, status, cpu, mem, disk, up/last-used) plus
  a rounded border — so a tile's natural height is a known constant, and the classifier's
  job is deciding how many fit and how wide they get, not inventing tile internals. Task 07
  owns the tile's content; this task owns its **budget**. Agree the constant explicitly:
  export a `tileHeight`/`tileMinWidth` from this file for task 07 to render into.
- The classifier serves the board **and** the other screens (VM screen, form, progress,
  secrets, browse) — all of which currently size themselves ad hoc. Bring them onto the mode's
  budgets too; that is what lets `secretsChrome` die.

## Input Dependencies

Task 01 — Charm v2 (`lipgloss/v2` sizing semantics, `tea.WindowSizeMsg`).

## Output Artifacts

- `internal/ui/layout.go` — `classify(w, h)`, `layoutMode`, and the tile size constants task
  07 and task 08 render into.
- A table-driven layout test that is the regression net for every offset this task deletes.

## Implementation Notes

<details>
<summary>Guidance</summary>

The reason this is its own task and not folded into the board is that it is the one piece of
the redesign that can be **fully tested without a terminal**: it is a pure function from two
ints to a struct of ints. Keep it that way — no `lipgloss` rendering, no model, no I/O in
`classify`. That purity is the whole point.

"80×24 must be a good experience, not a survivable one." Concretely: at 80×24 the board is
still a board — bordered tiles, colour, a focus ring, a footer. It is not a fallback list and
it is not a truncated table. If the budget maths says a tile does not fit at 80×24, the tile
gets narrower, not deleted.

Do not build a compact-roster mode. The plan cut it explicitly (YAGNI: at ≤10 VMs the board
always fits, and a second render path means a second set of goldens and a standing risk that
the two surfaces disagree).

**Test philosophy**: write a few tests, mostly integration. Meaningful tests verify custom
business logic, critical paths, and edge cases — test *your* code, not the framework. The
table-driven size sweep **is** the meaningful test here; it is exactly "edge cases and error
conditions for core functionality". Do not write a test per field of `layoutMode`.

Per `PRE_TASK_EXECUTION.md`, RED → GREEN → REFACTOR: write the size-sweep table first with
the invariants (all budgets positive, heights sum within `h`, no error at any size), watch it
fail, then implement `classify` until it passes.
</details>
