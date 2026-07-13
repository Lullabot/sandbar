---
id: 8
group: "board"
dependencies: [2, 7]
status: "completed"
created: 2026-07-12
model: "opus"
effort: "xhigh"
complexity_score: 9
complexity_notes: "Replaces the app's home surface. Deletes list.go and its table.Model, rewrites key routing onto the command registry, and must get focus right — identity-pinned, stable-ordered, scroll-following. Focus is where a grid UI is most likely to betray the user, and a verb reaching the wrong VM is a destructive bug."
skills:
  - go
  - bubble-tea
---
# The board: grid, identity-pinned focus, managed-only filter, search

## Objective

Replace `viewList` with the tile board — the app's **home surface and only roster**. Arrow keys move
a focus ring across a grid of tiles in two dimensions; single-key verbs act on the focused tile;
`enter` zooms it into the **existing** full-screen VM screen (`viewDetail`).

**Focus is where a grid UI is most likely to betray the user**, and that is why this is its own task
rather than "render some cards".

## Skills Required

`go`, `bubble-tea` — grid layout, focus routing, viewport scrolling, and key dispatch.

## Acceptance Criteria

- [x] **`internal/ui/list.go` and its `table.Model` are deleted.** `grep -rn "table.Model\|newTable\|refreshRows" --include=*.go .`
      returns zero hits. There is **no** second render path, no toggle back, no compact-roster fallback.
- [x] A new `internal/ui/board.go` composes the tiles (task 07) into a grid sized by the layout mode
      (task 03), and is the default view.
- [x] **Stable order.** Tiles are sorted **alphabetically** and the order **does not change when a
      VM's state changes**. Test: stop the focused VM and assert its index in the rendered order is
      unchanged. (Grouping running-first is rejected: at ≤10 VMs the whole fleet is on screen so
      grouping saves no scanning, while re-sorting on a state transition makes pressing `x` teleport
      the focused tile across the board **as a direct side effect of the verb the user just pressed** —
      at exactly the moment they are most likely to press another key.)
- [x] **Identity-pinned focus.** Focus tracks the **VM's name**, never the slot index. A refresh, an
      addition, or a deletion must **never** silently slide the focus ring onto a different VM. Tests:
      (a) a `vmsLoadedMsg` that inserts a VM alphabetically *before* the focused one leaves focus on
      the same **name**; (b) deleting the focused VM moves focus predictably (to a neighbour) rather
      than to whatever now occupies the old index; (c) a verb dispatched after a refresh reaches the
      VM **under the ring** and not the one that used to be there.
- [x] **The board shows managed clones and nothing else, always.** `f` and `m.managedOnly` are
      **deleted**; the filter is **unconditional**. A tile exists iff `reg.IsManaged(name)`.
      **Base images get no tile** either (today's `f` shows them — `isBaseImage` must no longer admit
      them to the roster). `grep -rn "managedOnly" --include=*.go .` returns zero hits.
- [x] **`/` name search is kept** and ported: a live-typing mode that narrows the visible tiles, `esc`
      to clear. The existing behaviour where a search **captures action keys** must be preserved —
      port `TestSearchCapturesActionKeys`. Single-key verbs and a typing mode compete for the same
      keystrokes, and the search must win while it is open.
- [x] Focus stays pinned to VM **identity across a filter change**, exactly as it does across a refresh.
- [x] **`X` (stop all) still means "stop every managed VM", not "stop what I can currently see."** An
      active `/` filter does **not** narrow it. Today's `stopAllTargets()` documents this at length and
      already excludes base images — preserve the semantic, and port its test.
- [x] **The grid scrolls, and focus follows scroll.** At 80×24 a single tile column holds roughly two
      tiles, so a power user with ten VMs will scroll. The viewport keeps the focused tile visible, and
      moving focus past the viewport's edge **scrolls** rather than trapping the ring. Test it.
- [x] `enter` opens the **existing** full-screen VM screen (`viewDetail`) for the focused tile; `esc`
      returns to the board **with focus still on the same VM**. There is **no** overlay inspector — that
      surface was considered and cut.
- [x] Key dispatch derives from task 02's **command registry** — a verb fires iff `enabledFor(focused)`.
- [x] `go test ./...` and `go test -race ./...` pass. The existing `internal/ui` suite (~90 tests, much
      of `model_test.go`) will churn heavily — tests for deleted surfaces are deleted; tests for
      **behaviour that survives** (search capture, stop-all semantics, confirm flow) are **ported, not
      dropped**. State in your report which tests you deleted and why.

## Technical Requirements

- Deleted from `list.go`, with their behaviour either ported or deliberately dropped:
  `newTable()`, `refreshRows()`, `summarizeNames()`, `selectedName()`, `lookupVM()`, `isBaseImage()`,
  `stopAllTargets()`, `updateList()`, `listView()`. **`stopAllTargets` and `lookupVM` are ported.**
  `beginAction` (in `list.go` today) is **kept** — it sets `m.acting`, batches the spinner tick, and
  no-ops the tick if already acting (which is what prevents a double-speed spinner). Move it somewhere
  sensible; do not lose it.
- The old contract every action key depended on was `table.SelectedRow()[0]` is the name. The new
  contract is the focused VM **name**. Make it explicit and single-sourced.
- `m.view = viewX` remains the screen-swap mechanism; the board is simply the new default view.
  `viewList` is replaced by `viewBoard` (or `viewList` is repurposed — pick one and be consistent).
- The empty-slot affordance — a **ghost tile** reading "press `n` to add a VM" — is **retained**, and
  it is retained *because* a 1–3 VM board is mostly empty: the dominant state of the target user's
  board becomes a call to action instead of dead space.

## Input Dependencies

- Task 02 — the command registry (the dispatcher and the footer both derive from it).
- Task 07 — the tile renderer (which transitively brings task 03's layout mode, task 04's job
  registry, and task 05's heartbeat).

## Output Artifacts

- `internal/ui/board.go` — the grid, the focus ring, the scrolling viewport, the search mode, the
  managed-only filter.
- `internal/ui/list.go` — **deleted**.
- The surface task 09 hangs the header band, the messages strip, and the footer on.

## Implementation Notes

<details>
<summary>Guidance</summary>

Two properties are **non-negotiable**, and they are the reason this task exists separately:

**Stable order.** Do not "improve" it into state-grouped sorting. The plan rejects that explicitly and
records why. If you find yourself sorting by status, stop.

**Identity-pinned focus.** This is the difference between a board that is safe to hold a destructive
key on and one that is not. The failure is silent and it is severe: the user arrows to `prod-box`,
a refresh tick lands, the list reorders, and `d` deletes `dev-box`. Track the **name**.

The always-on managed filter has a real cost, and the plan accepts it deliberately: **base images
become invisible and unmanageable from the TUI**, with no toggle to reveal them. A stale base image
is heavy (2.2GB and 7.2GB on the current test host). The mitigation is **one string, not a second
surface** — the header band carries a count of what is hidden (task 09 renders it). Do not
re-introduce the toggle, and do not quietly let base images back onto the board because it seems
friendlier.

`X` (stop all) not being narrowed by `/` is subtle enough that today's code documents it at length.
It means "stop every managed VM", full stop. Port that, and port its test.

**Test philosophy**: write a few tests, mostly integration. Meaningful tests verify custom business
logic, critical paths, and edge cases specific to this application — test *your* code, not the
framework. Here, the meaningful tests are all about **focus and dispatch**: focus survives a refresh
that reorders the fleet; a verb reaches the VM under the ring; search captures action keys; stop-all
ignores the filter; the grid scrolls and the ring stays visible; stable order across a state change.
These are behavioural assertions over real model state — **not goldens**. Goldens come in task 09 and
they are layout-regression insurance only.

Per `PRE_TASK_EXECUTION.md`, RED → GREEN → REFACTOR on each.

The harness's own comment names the bug class it exists to catch — "an editor/form that opens
unfocused and silently drops input" — **and that bug shipped anyway, past a passing golden.** A golden
proves a screen *painted*. It never proves a verb reached the right VM. Write the behavioural test.
</details>
