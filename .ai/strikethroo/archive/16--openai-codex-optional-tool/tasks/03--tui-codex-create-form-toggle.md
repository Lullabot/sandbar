---
id: 3
group: "tui"
dependencies: [2]
status: "completed"
created: 2026-07-16
model: "sonnet"
effort: "medium"
skills:
  - golang
  - bubbletea
---
# Add the "Install OpenAI Codex" toggle to the TUI create form

## Objective

Surface the `WithCodex` selection in the TUI create form as a toggle structurally identical to the existing "Install Claude Code" one, so the TUI and headless CLI stay equivalent: initialized from the (possibly base-adopted) `CreateConfig`, written back on submit, covered by the existing form tests' pattern.

## Skills Required

`golang` + `bubbletea` — this repo's `internal/ui` form conventions (toggle list entries backed by model fields).

## Acceptance Criteria

- [x] `internal/ui/model.go` gains a `toolCodex` field alongside `toolClaude` (same type/placement).
- [x] `internal/ui/form.go`: create-form initialization sets `m.toolCodex = cfg.WithCodex` next to the `toolClaude` line (after the `ApplyToolset` base-adoption block, so an existing base's selection is what the form shows); the toolset toggle list gains an entry labeled "Install OpenAI Codex" with `baseWideHelp("OpenAI Codex")` and get/set closures on `toolCodex`; the submit path writes `cfg.WithCodex = m.toolCodex` next to the existing `cfg.WithClaude = m.toolClaude` write-back.
- [x] Reset mode is untouched: no `resetWithCodex`, no "Preserve" toggle for codex (explicitly out of scope per the plan).
- [x] The reset-path write-back block (which restores `cfg.WithClaude = m.resetWithClaude` etc.) is left consistent — verify whether that block restores all toolset fields or only claude's, and mirror its existing structure for codex ONLY if omitting it would let the form submit a codex value the reset flow never showed; document the choice in a short comment.
- [x] `internal/ui/form_test.go` extended in the existing pattern: the codex toggle round-trips (toggle on → submitted config has `WithCodex: true`; default off → false).
- [x] Verification: `go test ./internal/ui/...` exits 0.
- [x] Verification: `go build ./...` exits 0.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Mirror the existing toggle entry at `internal/ui/form.go:330-334` (label, help, get, set) exactly in style.
- Initialization site: `form.go:231-234` (`BaseToolset` adoption then `m.toolClaude = cfg.WithClaude`).
- Submit write-back site: `form.go:514-519` (both the reset-restore branch and the normal branch).
- Keep list ordering sensible: place the codex toggle immediately after the Claude Code toggle so the two agent CLIs sit together.

## Input Dependencies

Task 2 — `CreateConfig.WithCodex` must exist (`internal/vm/vm.go`).

## Output Artifacts

- Updated `internal/ui/model.go`, `internal/ui/form.go`, `internal/ui/form_test.go`.
- Together with Task 2, completes both selection entrypoints (CLI + TUI).

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

Read the whole toggle plumbing before editing: `form.go` defines the create-form toggles as a slice of struct literals with `label`, `help`, `get`, `set`; the model holds one bool per toggle. `baseWideHelp(name)` produces the standard "affects the shared base image" help text — reuse it, do not hand-write help copy.

The subtle spot is the reset flow (`form.go` around lines 273 and 511-519): the model records `resetWithClaude` when entering reset mode so a reset re-provision does not accidentally re-converge a de-selected tool. Read those comments; if the mechanism is per-tool (a field per tool), codex needs the same treatment for correctness of reset-on-a-codex-enabled-VM — but do NOT add a preserve-codex-settings toggle (that is credential preservation, explicitly out of scope). If the mechanism instead snapshots the whole config, no change is needed. Decide from the code, not this note, and leave a one-line comment.

For the test, find the existing form test that flips the Claude toggle (`form_test.go` references `WithClaude`) and add the codex equivalent in the same table/style. Run with `go test ./internal/ui/... -run Form -v` first to see the pattern's names, then the full package.
</details>
