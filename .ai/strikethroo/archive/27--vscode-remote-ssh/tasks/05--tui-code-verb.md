---
id: 5
group: "tui"
dependencies: [4]
status: "completed"
created: 2026-11-13
skills:
  - go
  - bubbletea
complexity_score: 6
complexity_notes: "Command registry entry, a consent overlay and an async command; one task because the overlay exists only for this verb."
---
# TUI `c code` verb, Include consent overlay, and entry cleanup on delete

## Objective
Expose the shared operation on the board as `c code`, with a y/n consent overlay for adding the Include line, and remove a VM's managed entry when the board deletes it.

## Skills Required
Go, Bubble Tea v2 (`charm.land/...`).

## Acceptance Criteria
- [x] `vmCommands` gains `c` / "code" with an `about` sentence; enabled only for a running, non-building VM.
- [x] Missing + writable Include → overlay naming the file and exact line; `y` adds it then proceeds; `n`/`esc` proceeds without it and the result notes the alias will not resolve until the line is added. Symlink/unwritable → no overlay; the result includes the line to add.
- [x] The operation runs as an async `tea.Cmd`; the result message shows "Opening VS Code on <alias>…" or the Remote-SSH fallback; errors appear in the message log.
- [x] Successful board delete calls `sshconfig.Remove` for that VM's alias (best effort; error logged, not fatal), using the same alias helpers as the providers.
- [x] Tests isolate host state (`isolateHostState(t)` plus `HOME`/`XDG_CONFIG_HOME`), fake the launcher, and assert the far side: the managed file contains the entry, the user config gained the Include after `y`, the launcher received the expected argv. Goldens regenerated with `go test ./internal/ui -run TestTUI -update` and the diff limited to the new help/footer text.
- [x] `go test ./internal/ui` passes.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
Follow commandreg.go and the AGENTS.md board rules (single registry; footer/help derive from it; focus pinned to VM identity). Reuse the existing confirmation-overlay pattern (see the delete confirm) instead of a new screen.

## Input Dependencies
Task 4 (shared operation, launcher seam).

## Output Artifacts
TUI verb, overlay, delete cleanup, updated goldens.

## Implementation Notes
<details>

- Mirror the `d` delete confirm state machine with a small pending state holding the target (scope + name), not a slot index.
- Delete-cleanup alias: Local profile → `sshconfig.LimaAlias(name)`; others → `sshconfig.ProfileAlias(profileName, name)`. Find where the model maps a scope to its profile name.
- Test philosophy: write a few tests, mostly integration; assert at the boundary the user cares about (files written, argv launched).

</details>
