---
id: 3
group: "review-range"
dependencies: [1]
status: "completed"
created: 2026-09-29
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "medium"
skills:
  - bubbletea
  - go
---
# Landing pane review-range prompt

## Objective
Add an `R` key to the Landing pane that opens a one-line range prompt and starts the shared review with the entered arguments.

## Skills Required
`bubbletea` (v2, `charm.land/...`) for the prompt and key handling; `go`.

## Acceptance Criteria
- [ ] `landingReviewRangeKey` (`R`) is added, appears in the pane's derived footer help next to `v review`, and is hidden while a review is in flight on that row.
- [ ] `R` opens a single-line `textinput` sized like the issue input, with a constant placeholder/help ("Blank → whole branch", examples `HEAD~2`, `HEAD~2...HEAD`, `--staged`).
- [ ] `enter` splits on whitespace (no shell, no quote handling) and starts the review through the existing review-start body with `DiffArgs`; blank equals `v`; `esc` closes with nothing started.
- [ ] `v` and `V` behave exactly as before; while the prompt is focused, keys (including `q`) go to the input, and leaving the pane closes it.
- [ ] The prompt and footer fit 80x24 (help drawn under the height budget).
- [ ] Verify: `go test ./internal/ui` passes with behavioural tests (open/type/submit reaches the session with split args, blank equals `v`, `esc` starts nothing, no start while one is in flight) and updated goldens; `go test ./internal/ui -run TestTUI -update` was run and the diff of `internal/ui/testdata` shows only the footer entry and the prompt.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
Edit `internal/ui/landing.go`: bindings near `landingReviewKey`, the key switch near line 1085, the footer help list near line 1122, `startLandingReview`, and `newIssueInput` (copy its `SetWidth` handling). Use `isolateHostState(t)` in tests; fake the provider with `internal/providerfake`.

## Input Dependencies
`Session.DiffArgs` from task 1.

## Output Artifacts
The TUI range prompt, tests and goldens.

## Implementation Notes
<details>

- Extend `startLandingReview(fresh bool)` (or a thin wrapper both verbs call) to accept diff args; do not duplicate the in-flight guard, context ownership or URL forwarding.
- All help strings must be constants (no environment-derived text in goldens).
- Test philosophy: write a few meaningful tests, mostly integration. Drive the model with real key events; test this pane's behaviour, not bubbletea itself. No `t.Parallel()`.
</details>
