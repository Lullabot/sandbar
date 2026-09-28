---
id: 2
group: "tui-onboarding"
dependencies: [1]
status: "completed"
created: 2026-09-28
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "medium"
skills:
  - bubbletea-ui
  - go-testing
complexity_score: 8
complexity_notes: "Adds a pre-board child view, durable state, origin-aware routing, terminal detection, hyperlinks, browser actions, and responsive golden coverage."
---
# Implement the first-run onboarding workflow

## Objective
Add the dedicated first-run onboarding screen, durable acknowledgement, Warp-specific warning, support/current-release links, keyboard browser actions, and help-screen reopening flow without blocking existing provider startup.

## Skills Required
Bubble Tea v2 view/update/key-routing design and Go behavioral/golden testing.

## Acceptance Criteria
- [ ] With no `${XDG_STATE_HOME:-~/.local/state}/sandbar/onboarding.json`, the initial TUI view is onboarding while existing startup commands continue in the background.
- [ ] The screen explains the persistent guest tmux session and `C-a d`, links to the GitHub new-issue page and installed release notes, and exposes keyboard actions through the fakeable workstation browser opener.
- [ ] Exactly `TERM_PROGRAM=WarpTerminal` adds a clear Sand/Claude Code compatibility warning; generic terminal variables and other values do not.
- [ ] First-run dismissal atomically persists acknowledgement and enters the board; write failure warns but still permits entry, causing onboarding to reappear next launch.
- [ ] Help exposes a binding defined once to reopen onboarding; `esc` from that origin returns to help without rewriting acknowledgement, and `q` does not quit from onboarding.
- [ ] Existing profile, registry, secrets, checkout, board-focus, and provider startup behavior remains unchanged.
- [ ] Focused model tests and ANSI-stripped TUI goldens cover ordinary/Warp onboarding, persistence/relaunch, help reopening, browser success/failure, and 80x24 plus constrained layouts.
- [ ] `go test ./internal/ui` exits 0 after the new behavior and goldens are reviewed.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
Extend the existing root view enum, dispatcher, and renderer. Store acknowledgement separately from every existing schema using tolerant read and atomic write behavior. Reuse the TUI's existing `ghActions.OpenInBrowser` seam or extract only the smallest shared opener abstraction needed for clean testing. Generate OSC 8 hyperlinks only from fixed support and validated release URLs supplied by task 1. Derive displayed help from actual bindings.

## Input Dependencies
Task 1's trusted installed-release link helper and release metadata types; current help screen, keymap, layout, and browser-opening seams.

## Output Artifacts
Onboarding state/view/update code, help integration, deterministic Warp detection, and focused unit/behavioral/golden tests.

## Implementation Notes
<details>
<summary>Execution guidance</summary>

Record whether onboarding was opened as the initial surface or from help. Keep first-run dismissal distinct from child-screen back navigation. Load only the small local acknowledgement during model construction; do not wait for providers or GitHub before painting. Ensure new tests isolate `XDG_STATE_HOME`, `XDG_CACHE_HOME`, `XDG_DATA_HOME`, and `LIMA_HOME` before constructing a real model.

Meaningful tests verify custom business logic, critical paths, edge cases, and integration points. Exercise complete key-driven routes and persisted far-side state rather than testing Bubble Tea primitives. Combine related scenarios and retain readable ANSI-stripped goldens; do not create tests for trivial getters or framework behavior.

</details>
