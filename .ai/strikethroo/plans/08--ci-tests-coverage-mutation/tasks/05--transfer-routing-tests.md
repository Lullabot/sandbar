---
id: 5
group: "tier2-tests"
dependencies: []
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "medium"
skills:
  - go-testing
  - bubbletea
---
# Tier-2: cover transfer.go routing (updateBrowse / updateDest)

## Objective
Lock in the file-transfer routing in `internal/ui/transfer.go` — specifically the upload-vs-download target-VM selection (which must target the transfer's VM, guarding the documented wrong-VM bug) and the Esc→`ClearSelection` arm (which prevents an un-escapable destination prompt). Test via direct `Update()` calls, matching the package's style. Test-only.

## Skills Required
`go-testing`, `bubbletea` — drive the model's `Update` with typed key messages and assert on model state / routed target.

## Acceptance Criteria
- [ ] A test drives `updateBrowse` to the point of selection and asserts the destination lister/target is derived from `m.transferVM` (the transfer's VM), not another VM — proving the wrong-VM branch is correct.
- [ ] A test drives `updateDest` with an Esc key and asserts `m.browser.ClearSelection()` was effected (the pending selection is cleared and the view returns to the browser, so a subsequent keystroke does not bounce back into the dest prompt).
- [ ] Verification: `go test ./internal/ui/ -run 'Transfer|Browse|Dest' -v` passes; `go tool cover -func` shows `updateBrowse` (was 0%) and `updateDest` (was ~25%) increased.
- [ ] No production `.go` files changed.

## Technical Requirements
- `internal/ui/transfer.go`: `updateBrowse` routes browser keys and, on a `browser.Selected()` hit, sets src + advances to `viewDest`; the target-VM lister is built at ~`transfer.go:80` via `m.lookupVM(m.transferVM)` / `browse.NewGuestLister(m.cli, m.transferVM)`. `updateDest` handles Esc→`ClearSelection()` at ~`:101` and Submit→`launchCopy`.
- Reuse the package's existing test model constructors/helpers (`newTestModel`, `putOnBoard`, `isolateHostState`, `runeKey`) rather than building a model from scratch.

## Input Dependencies
None.

## Output Artifacts
Added routing tests in `internal/ui` (e.g. `transfer_test.go`).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- **Test philosophy (write a few tests, mostly integration):** verify custom business logic, critical paths, and edge cases specific to this app — test our code, not the framework/stdlib. Combine related scenarios into one task; favor integration/critical-path over per-method units; don't test trivial getters or stdlib behavior.
- Look at `internal/ui/transfer_test.go` and `model_test.go` for the established pattern (typed message structs, `ansi.Strip(view)` substring assertions, `isolateHostState`). Set `m.transferVM` to a known VM name and drive a selection; assert the dest lister/default dir is scoped to that VM.
- For the Esc arm, put the model into `viewDest` with a pending selection, send `tea.KeyEsc`, and assert the selection is cleared (either via an exported accessor or by observing that re-entering the browser does not immediately re-fire the dest prompt).
- Do NOT add `t.Parallel()` — the ui tests pin package-level host-probe vars (`hostMemBytesFn`, etc.) and are serial by design.
</details>
