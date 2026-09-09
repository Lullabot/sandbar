---
id: 6
group: "sand-commands"
dependencies: [5]
status: "completed"
created: 2026-08-04
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Bubble Tea async command wiring; blocking the event loop or leaking a child on pane exit would freeze or orphan the board."
skills:
  - go
  - bubbletea
---
# TUI Landing pane review key

## Objective

Add a review key to the TUI's Landing pane so the selected checkout row can open
the review UI in a browser, reaching parity with `sand land --review` without
blocking the board's event loop.

## Skills Required

- **go** — reusing task 5's orchestration function.
- **bubbletea** — an asynchronous `tea.Cmd`, a message round trip, and key/help
  registration in an existing pane.

## Acceptance Criteria

- [ ] The Landing pane's help line shows the new review key alongside the
      existing act and rescan keys.
- [ ] `go test ./internal/ui/... -race -v -run Landing` passes, including a test
      that pressing the key on a checkout row dispatches the review command and
      a test that the completion message updates the row state.
- [ ] A test asserts the key is a no-op when there is no selected checkout row
      (empty sweep), rather than panicking or dispatching.
- [ ] A test asserts the pane's `Update` returns promptly when the key is
      pressed — the orchestration runs inside a `tea.Cmd`, never inline in
      `Update`.
- [ ] The row reflects an in-progress state while the review is open and returns
      to normal on completion or error; an error is surfaced in the pane rather
      than silently dropped.
- [ ] Any existing board golden/snapshot tests are updated and pass:
      `go test ./internal/ui/... -race` is green.
- [ ] `go test ./... -race` passes and coverage stays at or above
      `COVERAGE_FLOOR`.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Call task 5's orchestration function; do **not** duplicate port selection,
  server launch, forwarding, readiness or teardown logic.
- The pane is a Bubble Tea model whose `Update` must not block. All of the work
  belongs in a `tea.Cmd` that yields a completion message.
- Pick a key that does not collide with the pane's existing bindings
  (`enter`/`o` act, `r` rescan) or the board's global keys.

## Input Dependencies

Task 5's reusable orchestration function, which must already accept a
`context.Context` and an `io.Writer` rather than reaching for `os.Stdout` or
installing its own signal handler.

## Output Artifacts

- A new key binding, its help entry, and its dispatch in `internal/ui/landing.go`
- A completion message type and its handler
- Tests covering dispatch, completion, the empty-selection case, and non-blocking
  `Update`

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Read `internal/ui/landing.go` first.** It already has everything this task
mirrors: `landingActKey` / `landingRefreshKey` bindings, a `landingHelp()` that
lists them, an `updateLanding` key switch, per-row actions dispatched as
commands (`runLandingAction`), and async completion handlers
(`handleLandCommitPushDone`, `handleLandRefresh`) that fold a result message back
into the model. Follow those patterns rather than inventing new ones — in
particular, the commit-and-push action is the closest analogue because it also
runs guest work asynchronously and reports back.

**Key choice.** `enter`/`o` and `r` are taken on this pane. Check the board's
global bindings in `internal/ui/keys.go` before choosing, and pick something
mnemonic that is free — `v` (view/review) is a reasonable candidate. Register it
in `landingHelp()` so the footer shows it.

**Dispatch shape.**

```go
case key.Matches(msg, landingReviewKey):
    return m, m.runLandingReview()
```

where `runLandingReview` returns nil when there is no selected checkout row, and
otherwise returns a `tea.Cmd` closing over the provider, the selected checkout
and a context, calling task 5's orchestration and yielding a
`landReviewDoneMsg{path string, err error}`.

**In-progress state.** The orchestration blocks for as long as the human is
reviewing — potentially many minutes. Mark the row (or the pane header) as
review-in-progress when dispatching and clear it in the done handler, so the
board does not look idle while a review is open. Reuse whatever mechanism the
pane already uses to show a row mid-action.

**Cancellation.** Derive the command's context so that leaving the pane or
quitting the board tears the review down rather than orphaning the guest server
and the forwarder child. Check how the pane's existing async actions obtain their
context and follow suit.

**Testing.** The `internal/ui` package already drives the model with synthetic
`tea.KeyPressMsg` values and asserts on returned commands and view output — copy
that style. Assert the dispatch and the message handling; do not try to run a
real review. If the board has golden/snapshot tests that include the help
footer, regenerate them.

</details>
