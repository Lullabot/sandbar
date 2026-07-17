---
id: 10
group: "docs"
dependencies: [8, 9]
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "low"
complexity_score: 3
complexity_notes: "Documentation across five files + AGENTS.md invariants. Mechanical, but the security-model prose must be precise, so accuracy over volume."
skills:
  - technical-writing
---
# Documentation and AGENTS.md invariants

## Objective
Document the land feature and lock its invariants into `AGENTS.md` so future
changes don't erode them: the keybinding change, the unlanded-work badge, the
delete-guard warning, the Landing pane and its states, `sand land`, the `u`/`g`
data-not-code reframe, and the security-model framing that **land moves PR
metadata, not code**.

## Skills Required
- **technical-writing** — accurate, concise docs matching the existing style of
  `docs/using-sand/` and `docs/reference/`.

## Acceptance Criteria
- [ ] `docs/using-sand/tui.md` — keybindings updated to `l` = land, `L` = log;
      the unlanded-work badge documented (actionable vs at-risk); the delete-guard
      warning documented.
- [ ] `docs/using-sand/files-and-shells.md` — a **Landing** section (pane states,
      one-shot draft PR, the ledger reopenable via `L`) **and** the **`u`/`g`
      reframe: data, not code** (SQL dumps in; screenshots/videos/artifacts out —
      explicitly not a code path).
- [ ] `docs/using-sand/cli-reference.md` — `sand land` with `--pr`/`--web`,
      including the **gh-optional** behavior (browser-URL fallback; `--web` is
      gh-free; `--pr` in a pipe exits non-zero with the URL on stderr).
- [ ] `docs/reference/security-model.md` — land as the audited counterpart to the
      no-host-mount boundary: **it moves PR metadata, not code**, so landed work
      never auto-executes on the host; the **two-token split** (guest pushes, host
      opens the PR); and that code reaches the host only via the user's own
      reviewed `gh pr checkout`/pull.
- [ ] `AGENTS.md` — records the checkout-registry/sweep addition, the
      **zero-guest-contact delete** invariant, and the **"land never copies code
      to the host"** invariant.
- [ ] Docs reflect the **final** keybinding and CLI surface as implemented
      (verify against tasks 8 and 9, not the plan's early drafts).
- [ ] `git grep` for the old `l`→log wording in docs finds nothing stale.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Match the tone/structure of the surrounding docs; do not restructure unrelated
  sections. Scope edits to the project's own doc files (not `.ai/` or skill dirs).
- The security-model prose is the highest-stakes text: state the **provable**
  claim precisely (land controls what metadata reaches the host and records it in
  the ledger; it does not and cannot stop a guest from using its own push token).

## Input Dependencies
- Task 8: final keybinding mapping (`l` = land, `L` = log).
- Task 9: final `sand land` CLI surface and flag semantics.

## Output Artifacts
- Updated `docs/using-sand/tui.md`, `docs/using-sand/files-and-shells.md`,
  `docs/using-sand/cli-reference.md`, `docs/reference/security-model.md`, and
  `AGENTS.md`.

## Implementation Notes
<details>
<summary>Detailed implementation guidance</summary>

1. Read each target doc first to match its heading style and depth. Update the
   TUI keybinding table (l/L swap), add the badge legend (amber actionable /
   at-risk `↑N`·dirty), and the delete-guard warning description.
2. In files-and-shells.md, add a "Landing" section describing the pane states
   (pushed·no-PR → open draft PR; PR #N → open in browser; unpushed/dirty → push
   first; local-only) and the ledger; then rewrite the `u`/`g` description as
   **data, not code** with concrete examples (SQL dumps in; screenshots/videos
   out).
3. In cli-reference.md, add `sand land NAME [<path>] [--pr|--web]` with the exact
   gh-optional semantics from task 9 (TTY offer vs pipe non-zero+stderr; `--web`
   gh-free).
4. In security-model.md, add the land note next to the no-host-mount discussion;
   keep the claim precise and non-overreaching.
5. In AGENTS.md, add the three invariants concisely where similar invariants
   live, so future edits see them.
6. This is documentation — no test cycle; verify by `git grep` that no stale
   `l`→log wording remains and that the CLI/keys text matches the shipped code.
</details>
