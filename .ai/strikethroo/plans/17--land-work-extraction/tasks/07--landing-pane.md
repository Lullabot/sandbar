---
id: 7
group: "landing-pane"
dependencies: [1, 3, 6]
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 8
complexity_notes: "New Bubble Tea pane integrating registry + live gh reconciliation + per-row state machine (enabledFor idiom) + job/ledger streaming. The feature's UI centerpiece."
skills:
  - go
  - bubbletea
---
# Landing pane (`l`) — the pull-request cockpit

## Objective
Build the Landing pane opened by `l` on a focused, running VM: it lists the VM's
checkouts (worktrees grouped under their parent) with each one's branch and
state, resolves authoritative PR truth per pushed branch via the host gh adapter
(task 6) on open, and offers exactly one obvious action per row (open draft PR /
open in browser / none) using the `enabledFor` idiom. Every gh action runs as a
**job** so its output streams live and is retained as a **ledger** entry
reopenable via `L`.

## Skills Required
- **go** — integrating the registry, the gh adapter, and the job registry.
- **bubbletea** — a new pane (model/update/view), list rendering, and the
  existing job/log viewport plumbing.

## Acceptance Criteria
- [ ] `l` on a focused, **running** VM opens the Landing pane (gated with the
      `enabledFor` idiom, matching `shell`/`u`/`g` gating — running VM required).
- [ ] The pane lists the VM's checkouts from the registry (task 1), **worktrees
      grouped under their parent repo**, each showing branch + state.
- [ ] On pane open, a **lazy authoritative** host-side check
      (`gh.PRState`, task 6) resolves branch/PR truth for each **pushed** branch
      and reconciles the row (correcting a stale sweep heuristic).
- [ ] Each row falls into one state with one action:
      | Registry + gh | Row | Action (key) |
      | --- | --- | --- |
      | pushed, no PR | "pushed · no PR" (amber) | **Open draft PR** (gh) — or, without gh, open the **compare URL** in the browser |
      | pushed, PR #N open | "PR #N (draft)" + status | **Open in browser** (constructed PR URL) |
      | unpushed / dirty | "↑N unpushed" (at-risk) | none — push in the shell first |
      | no remote | "local only" | none |
- [ ] Each gh action is dispatched as a **job** (reuse `internal/ui/jobs.go`) so
      it streams live and persists as a ledger entry **reopenable with `L`**.
- [ ] **No guest execution on any action and no code copied to the host** — the
      only bytes crossing are PR metadata. Pushing is **not** a land verb.
- [ ] **Graceful degradation:** when host gh is absent/unauthed (task 6
      `Available` false), detection + rows still render, "open in browser" works,
      and **Open draft PR** falls back to opening the compare URL. The pane
      **surfaces which mode it is in**.
- [ ] A **GitLab/drupal.org** checkout is listed with its state but offers no
      one-key MR action (deferred) — at most "open in browser".
- [ ] `go test ./internal/ui/... -race` passes with tests for row-state mapping,
      the gh-absent fallback branch, and worktree grouping.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Follow the existing pane/model conventions in `internal/ui` (how other panes
  are opened, updated, and rendered; how `enterTarget`/`vmCommands` dispatch).
- Reuse the job registry (`internal/ui/jobs.go`) for action execution + ledger
  retention; do not build a parallel logging mechanism.
- PR-state reconciliation is **lazy at open** (not on every keystroke); treat the
  sweep's push-state as a hint, gh as authoritative (plan Component 4).
- gh scope is **GitHub only** for the one-key action; other forges show state +
  browser-open only.

## Input Dependencies
- Task 1: registry rows for the focused VM.
- Task 3: a live sweep keeps those rows fresh while the pane is open.
- Task 6: `PRState`, `CreateDraftPR`, `CompareURL`, `PRURL`, `OpenInBrowser`,
  `Available`.

## Output Artifacts
- A `landing` pane in `internal/ui/` (model/update/view + tests), wired to open
  from the board, with actions routed through the job registry.

## Implementation Notes
<details>
<summary>Detailed implementation guidance</summary>

1. Create the pane model: it holds the focused VM identity, the list of rows
   (built from `registry.Get`), a per-row resolved PR state, and the current gh
   mode (`available`/`browser-fallback`).
2. On open, fire a command that calls `gh.PRState` for each pushed branch
   (task 6) and folds results back via a message; until they resolve, show the
   sweep's push state as provisional.
3. Row → action mapping per the table; use the `enabledFor` idiom so each row
   exposes exactly one action key. "Open draft PR" calls `gh.CreateDraftPR`
   **as a job**; on `Available()==false` it instead calls `OpenInBrowser(CompareURL(...))`.
   "Open in browser" uses `PRURL`/`CompareURL` — always gh-free.
4. Wrap each action in a job (`internal/ui/jobs.go`) so output streams into the
   viewport and is retained; confirm the ledger entry reopens via `L` (task 8
   binds `L`, but the retention/reopen mechanism is job-registry native).
5. Group worktree rows under their parent repo (use `Kind`/`Parent` from task 1).
6. Surface the gh mode in the pane header/footer ("gh: browser fallback").
7. Tests: unit-test the pure row-state mapping (registry row + PRState → row +
   action) across all table cases incl. gitlab and gh-absent; a render test for
   grouping. Keep guest exec strictly out of every action path.
</details>
