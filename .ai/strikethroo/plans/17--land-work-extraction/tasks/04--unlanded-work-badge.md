---
id: 4
group: "badge"
dependencies: [1]
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "medium"
complexity_score: 5
complexity_notes: "Tile-render addition reading the registry; two states in the existing amber band vocabulary; render/golden tests."
skills:
  - go
  - bubbletea
---
# Unlanded-work tile badge

## Objective
Add a small tile badge, derived purely from the checkout registry, that shows at
a glance which VMs hold work that isn't yet a PR (**actionable**) and which hold
work that exists nowhere but the VM (**at-risk**) — reusing the status bands'
existing amber `⚠` "worth your attention" vocabulary, not a new visual language.

## Skills Required
- **go** — reading the registry, aggregating per-VM state.
- **bubbletea** — the tile/status-band renderer and its golden-test harness.

## Acceptance Criteria
- [ ] The tile renderer gains a badge computed **only** from the registry
      (task 1), aggregated across the VM's checkouts:
      - **Actionable** — a branch **pushed but with no open PR** — rendered in the
        existing amber `⚠` warn vocabulary (`internal/ui/header.go` / band styles).
      - **At-risk** — **unpushed commits and/or uncommitted changes** — an `↑N` /
        dirty marker.
- [ ] A VM whose registry is **empty or stale** shows **nothing** (or a clearly
      stale indicator) — never a fabricated state.
- [ ] The badge fits the existing tile layout and status bands and degrades
      cleanly on a never-swept VM (no panic, no layout break).
- [ ] Because PR-existence resolves lazily (task 7), until it is known the badge
      reflects **push/dirty state alone**; the code path that consumes a known
      "no open PR" signal is present but tolerates "unknown".
- [ ] `go test ./internal/ui/... -race` passes with render/golden tests covering:
      actionable, at-risk, both-at-once, empty (nothing), and stale.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Reuse the amber warn styling already defined for the status bands
  (`internal/ui/header.go` and the band styles) — do not introduce a new color or
  glyph vocabulary.
- The badge is a **pure registry reader**; it issues no guest or network calls.
- Follow the existing golden-test convention in `internal/ui`
  (e.g. `header_bands_golden_test.go`, `boardshot_test.go`).

## Input Dependencies
- Task 1: registry accessors and the `Checkout` fields (`PushState`, `Dirty`,
  `Ahead`, `LastSeen`).

## Output Artifacts
- Badge computation + render integrated into the tile/header rendering, with
  golden tests for each state.

## Implementation Notes
<details>
<summary>Detailed implementation guidance</summary>

1. Add a helper that maps a VM's `VMCheckouts` to a badge state:
   `{actionable bool, atRisk bool, stale bool}` — actionable if any checkout is
   `pushed` with a known-absent PR (until task 7 resolves PR state, treat
   "unknown PR" as not-yet-actionable OR reflect push-only per the plan; pick the
   plan's "reflects push/dirty alone until resolved" behavior); at-risk if any
   checkout has `unpushed`/`never` with commits or `Dirty > 0`; stale if the
   registry's `SweptAt` is older than the freshness window or the VM isn't
   running.
2. Render it into the tile using the existing amber `⚠` band style for
   actionable and an `↑N`/dirty marker for at-risk. Find where tiles/bands render
   (`header.go`, tile render in `board.go`) and slot the badge without breaking
   the layout math.
3. Empty/stale → render nothing or a subdued stale marker; never invent state.
4. Golden tests: follow `header_bands_golden_test.go` / `boardshot_test.go`
   patterns; add fixtures for each state and assert the rendered output.
5. No RED→GREEN test dance is needed for pure rendering; a golden per state is
   the right coverage. Keep the badge logic (the pure mapping) unit-tested
   separately from the render.
</details>
