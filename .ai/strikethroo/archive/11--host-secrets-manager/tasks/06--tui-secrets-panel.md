---
id: 6
group: "tui"
dependencies: [1, 5]
status: "completed"
created: 2026-07-09
model: "sonnet"
effort: "high"
skills:
  - go
  - bubbletea-tui
---
# TUI Secrets Panel (resolves issue #3)

## Objective
Add a secrets panel to the `sand` TUI for the selected VM: list secrets (masked), add/edit a secret, and a dedicated "refresh GitHub token" action that updates the host store and syncs it live. This is the concrete resolution of issue #3, generalised to all secrets.

## Skills Required
- **go** — extend `internal/ui`.
- **bubbletea-tui** — a new panel/view consistent with the existing TUI (list, form input, key bindings).

## Acceptance Criteria
- [ ] `go build ./...` and `go test ./internal/ui/...` pass.
- [ ] From the TUI, opening the secrets panel for a selected VM lists that VM's secrets with values masked (a `format_test.go`-style unit test asserts masking).
- [ ] The panel supports adding a secret (global, directory-scoped env, or GitHub token) that persists to the host store via `internal/secrets`.
- [ ] A "refresh GitHub token" action updates the store's `github` entry and triggers the live sync entry point from task 5.
- [ ] Secret values entered in the TUI are never rendered back in cleartext (masked in the list) and never logged.

## Technical Requirements
- Reuse `internal/secrets` for persistence/redaction and the shared "render into running VM" entry point from task 5 for the refresh/apply action.
- Match the existing TUI structure in `internal/ui` (see `form.go`, `detail.go`, `commands.go`, `format.go`).
- Input fields for secret values should be masked in the UI.

## Input Dependencies
- Task 1: `internal/secrets` store package.
- Task 5: the live-sync entry point (`sand secret sync` logic) for the refresh action.

## Output Artifacts
- A TUI secrets panel wired into the existing view navigation.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

Model the panel on the existing Bubble Tea views in `internal/ui`. Add a keybinding from the VM detail view to open "Secrets". The panel shows a masked list (reuse the redaction helper from task 1). Provide an add/edit form (reuse `form.go` patterns) with a category selector (global / directory env / GitHub) and, for scoped items, a directory field.

The "refresh GitHub token" action: pick the target `github` scope, prompt for the new token (masked input), write it via `internal/secrets`, then call the task-5 render-into-running-VM entry point so it applies live. Surface the same honest effect note the CLI prints (git = immediate).

Keep the panel read-mostly and safe: mask values everywhere, never echo or log cleartext. Do not reimplement rendering — delegate to the task-5 entry point. This task closes issue #3.
</details>
