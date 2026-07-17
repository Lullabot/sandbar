---
id: 5
group: "sand-paste-image"
dependencies: [3]
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "medium"
skills:
  - go
  - bubbletea
complexity_score: 6
complexity_notes: "Bubble Tea verb registration + async command + result message, following the transfer verbs but without the file-browser wizard; must act on the registry-provided VM, not m.detail."
---
# TUI: "paste image" verb (key `v`)

## Objective
Add a tile verb to the board that stages the clipboard image on the focused
running VM's guest clipboard, bound to `v`, labeled "paste image", running as a
direct async action with a status-line result and no board navigation change.

## Skills Required
- `go` — wiring in `internal/ui`.
- `bubbletea` — command registry entry, async `tea.Cmd`, result message handling.

## Acceptance Criteria
- [ ] A new command in `internal/ui/commandreg.go` with
      `key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "paste image"))` and an
      `enabledFor` guard restricting it to Running VMs (same guard as `S`/`u`/`g`).
- [ ] Pressing `v` on a running VM's tile dispatches an async `tea.Cmd` that calls
      task 3's `PasteImage` for THAT tile's VM (taken from the command-registry
      argument, never `m.detail`) and returns a result message.
- [ ] The result is shown on the status line (e.g.
      `staged image on <vm> — press S then Ctrl-V` or `no image on clipboard`);
      the board view does NOT change (decision B: stay on board).
- [ ] It does NOT open the file browser (unlike upload/download); no
      `startTransfer` path.
- [ ] `v` is disabled/absent on a stopped VM's tile.
- [ ] `go build ./...` and `go test ./internal/ui/...` pass; add/adjust a
      command-registry test asserting the verb's presence, key, label, and
      Running-only enablement (mirroring existing commandreg tests).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Follow the existing verb pattern in `internal/ui/commandreg.go` (see the
  `S` shell and `u`/`g` transfer entries) for the binding + `enabledFor` shape.
- For the async action, model a lightweight `tea.Cmd` returning a result message
  (a new message type in `internal/ui/messages.go`); a status-line render for it.
  The single small write does NOT need the job registry's progress/cancel
  machinery.
- Pass the acting VM explicitly from the command registry, consistent with the
  transfer verbs' wrong-VM fix (never read `m.detail`).

## Input Dependencies
- Task 3: the `PasteImage` orchestration core.

## Output Artifacts
- The `v` verb registration, its async command, and the status-line result
  message/rendering in `internal/ui`.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

- `internal/ui/commandreg.go`: add an entry beside `u`/`g`/`S`. Reuse the
  running-VM `enabledFor` predicate the shell/transfer verbs use.
- Action: return a `tea.Cmd` (closure) that runs `PasteImage(ctx, prov, scope,
  v.Name)` off the UI goroutine and yields a `pasteResultMsg{vm, result, err}`.
  Handle that message in `Update` by setting the status-line text; do not change
  `m.view`.
- Status-line text: success → `staged image on <vm> — press S then Ctrl-V`;
  no-image → `no image on clipboard`; error → the error string.
- Verify against the existing commandreg tests (e.g. how the `S`/`u` entries are
  asserted) and add one case for the new verb: key `v`, help `paste image`,
  enabled only when Running.
- Confirm no new `tmux` reference is introduced (the feature must keep
  `internal/lima/attach.go` as the sole tmux touchpoint).
</details>
