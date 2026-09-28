---
id: 3
group: "tui-release-integration"
dependencies: [1]
status: "completed"
created: 2026-09-28
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
skills:
  - bubbletea-ui
  - go-testing
complexity_score: 8
complexity_notes: "Integrates asynchronous cached/network state with startup, trusted OSC 8 rendering, browser navigation, and strict responsive-header budgets."
---
# Integrate the daily check and header update notice

## Objective
Wire the release checker into TUI startup and render a trusted, navigable `(Update available!)` notice beside the full-header version when a newer stable release exists.

## Skills Required
Bubble Tea v2 asynchronous command/message integration and ANSI-aware terminal UI testing.

## Acceptance Criteria
- [ ] TUI startup immediately folds a valid cached release into the model and runs due-check/reservation/HTTP work asynchronously alongside provider refresh commands.
- [ ] Non-TUI commands remain offline and unchanged; no network or filesystem I/O occurs from `View`/header render functions.
- [ ] A newer comparable stable release renders `version (Update available!)` in the full header with the suffix linked to the validated new release notes; equal, older, prerelease, malformed, development, and dirty builds do not.
- [ ] The complete clause fits at 80 columns; narrower full headers shed the update suffix before the installed version, and compact mode preserves its existing version-free priority.
- [ ] A keyboard route from help/onboarding opens current or available release notes through the fakeable browser opener without making the header focusable.
- [ ] Failed checks remain silent, preserve cached update state, and do not block or alter provider connection messages.
- [ ] Header/model tests prove cache-first rendering, async result folding, OSC 8 target integrity, browser routing, 80-column output, suffix shedding, and compact behavior.
- [ ] `go test ./internal/ui` exits 0 with no golden regressions outside intentional onboarding/header changes.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
Add the release startup command to `model.Init` without serializing profile refreshes. Route completion messages regardless of the active view so results arriving during onboarding are retained for the board. Use ANSI-aware width functions already present in `header.go`; calculate the title, installed version, and update suffix as separate priority units. Do not introduce a focusable header control or a second command registry for VM actions.

## Input Dependencies
Task 1's cache/result/comparison APIs; current TUI header, startup command batching, help/onboarding browser routes, and layout contracts.

## Output Artifacts
TUI release-check wiring, header notice/link rendering, keyboard navigation integration, and focused behavioral/golden tests.

## Implementation Notes
<details>
<summary>Execution guidance</summary>

Keep cached rendering deterministic and network refresh message-driven. An update result belongs to application-global model state, not the active board view, so fold it before forwarding messages. Pin build versions in every new header golden. Make raw-output assertions on the OSC 8 URL and stripped-output assertions on width/text.

Meaningful tests verify custom business logic, critical paths, edge cases, and integration points. Test Sand's message routing and width priorities, not ANSI/Bubble Tea library internals. Combine cases in tables where appropriate and keep all HTTP, cache, clock, and browser effects injected.

</details>
