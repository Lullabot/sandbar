---
id: 11
group: "documentation"
dependencies: [9]
status: "completed"
created: 2026-07-12
model: "sonnet"
effort: "medium"
skills:
  - technical-writing
---
# Update README and AGENTS.md for the board

## Objective

The TUI's user-facing surface changes substantially and its keybindings change in a way that breaks
existing muscle memory. Any documented claim that provisioning blocks the UI is now **false**. Update
the user docs, and record in `AGENTS.md` the architectural facts a future agent would otherwise have to
rediscover — **and the ones it could plausibly get wrong**.

## Skills Required

`technical-writing` — accurate, plain documentation of a shipped surface.

## Acceptance Criteria

- [x] **`README.md` / `README-sand.md`** describe the **board**, its focus model, and the new keys. Any
      description or screenshot of the old table list is removed. Any claim that provisioning takes the
      screen hostage is corrected — it no longer does.
- [x] The docs state that the board shows **managed clones only**, that there is **no toggle**, and that
      base images are managed via `limactl`.
- [x] **`AGENTS.md`** records **all** of the following (each is a fact a future agent could plausibly get
      wrong, and several are things this plan had to discover the hard way):
      - The project is on Charm **v2** (bubbletea, bubbles, lipgloss) and `x/exp/teatest/v2`.
      - The board is the **only** roster surface. There is no table view and no compact roster. Do not
        add one without a scope change.
      - The board shows **managed clones only, always**. Base images and unrelated Lima VMs get no tile,
        and there is **no toggle** — `f` and `m.managedOnly` were deleted deliberately. The header band's
        **hidden count** is what keeps this honest; **do not remove it**. `X` (stop all) still means
        *every managed VM*, not the ones a `/` filter leaves visible.
      - Because every tile is managed by construction, the **managed/external badge is uniform and
        therefore hidden by the exception-only rule** — it is not special-cased.
      - The design targets **1–3 VMs**, up to 10. Density features are deliberately absent.
      - **`CPUs` and `Memory` on `vm.VM` are allocations, not utilization.** Live utilization comes only
        from the guest heartbeat, and only for running VMs. **Never render an allocation as a utilization
        gauge.**
      - **Lima reports only `Running` and `Stopped`.** A provisioning VM is `Running` to Lima. `Building`
        and `Failed` are **sand-side** states derived from the job registry, which is consulted **ahead of**
        `vm.Status` when rendering a tile. **Never render `vm.Status` directly on a tile** — a failed
        provision would show as a green "Running".
      - Tile order is **stable**; focus is pinned to VM **identity**. Both are deliberate — do not
        "improve" them into state-grouped sorting.
      - The job registry retains the **last run per VM, in memory, including its log**, and that log is
        reopenable from the tile. **Failed jobs are kept, not discarded** — dropping them would make a
        failed provision render as healthy. Run history is **not** persisted and there is no multi-run
        history; that was deliberately out of scope.
      - Keys, help text, and verb eligibility all derive from **one command registry**. Do not reintroduce
        a hand-maintained help list beside it — that duplication is what this replaced, and it had already
        drifted. There is deliberately **no command palette**.
      - **An assertion must reach the boundary the user cares about.** Golden tests prove a screen
        *painted*. In-process behavioural tests prove the model or the store changed. **Neither proves the
        guest changed.** This rule is written in blood: the secrets editor shipped past a passing golden
        (it dropped every keystroke), and then its replacement behavioural tests passed while `ctrl+s`
        still never reached the guest. If a claim crosses into a VM, onto a disk, or across a process
        boundary, **test the far side**.
      - Saving a secret **applies it to a running guest**. Do not "simplify" that back to a store-only
        write — **the apply is the feature**.
      - The **naming prohibition**: no nautical metaphor anywhere — no harbour, slip, boat, pier, moored,
        deck, or cargo in any identifier, comment, or user-visible string.
- [x] Every documented key **matches the shipped command registry**. Verify by reading the registry, not
      by copying this task's description of it.
- [x] The docs contain **no** reference to `docs/ui-redesign/` — that exploration branch is not being
      merged and nothing may depend on it.

## Technical Requirements

- The single source of truth for the keys is the command registry from task 02, as shipped in tasks 08
  and 09. Read it.
- `AGENTS.md` exists on `main` (added in `38cfd94`). Extend it; do not rewrite it wholesale.

## Input Dependencies

Task 09 — the board must be complete, or the docs will describe something that does not exist.

## Output Artifacts

- Updated `README.md` / `README-sand.md` and `AGENTS.md`.

## Implementation Notes

<details>
<summary>Guidance</summary>

Write the `AGENTS.md` entries as **constraints with their reasons**, not as a feature list. Nearly every
bullet above is a place where a future agent's instinct is *wrong*: state-grouped sorting looks like an
improvement, a `managedOnly` toggle looks friendlier, rendering `vm.Status` looks simpler, a store-only
secret save looks cleaner. Each of them would reintroduce a bug this plan deliberately closed. The reason
is the load-bearing half of the sentence — keep it.

No new tests are warranted for this task (documentation has no runtime surface). Per
`PRE_TASK_EXECUTION.md`, state that explicitly rather than inventing a test to satisfy the cycle.

The verification for this task is **reading the shipped code and confirming the docs match it** — not
reasoning about what the plan said would ship. Where the code and the plan disagree, the **code** is what
the docs describe, and the disagreement is worth flagging in your completion report.
</details>
