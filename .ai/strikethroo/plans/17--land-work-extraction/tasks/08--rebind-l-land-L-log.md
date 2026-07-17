---
id: 8
group: "landing-pane"
dependencies: [7]
status: "completed"
created: 2026-07-17
model: "sonnet"
effort: "medium"
complexity_score: 4
complexity_notes: "One atomic keybinding rebind touching commandreg, help/keys screen, and tile footer; the risk is a missed reference, so it is verification-heavy but logically simple."
skills:
  - go
  - bubbletea
---
# Rebind `l` = land, `L` = log (clean break)

## Objective
Rebind `l` to open the Landing pane (task 7) and move the existing **log** verb
to `L`, as one atomic change touching every reference — the `vmCommands`
binding, the `?` keys screen, and the tile footer — with no transitional
behavior. `enterTarget`'s building→log routing continues to reach log by its id,
unaffected by the key change.

## Skills Required
- **go** — the keybinding definitions and `vmCommands` wiring.
- **bubbletea** — the help/keys screen and footer rendering.

## Acceptance Criteria
- [ ] `l` is bound to **land** (opens the Landing pane from task 7); the **log**
      verb is bound to `L`.
- [ ] Every reference is updated in one change: `vmCommands`/keys in
      `internal/ui/commandreg.go` + `internal/ui/keys.go`, the `?` keys screen
      (`internal/ui/help.go`), and the tile footer.
- [ ] `enterTarget`'s building→log routing still reaches **log** via its id (not
      its key), so mid-build `enter` behavior is unchanged.
- [ ] No transitional/back-compat behavior remains (clean break, per plan
      clarification).
- [ ] `go test ./internal/ui/... -race` passes, including help/footer render
      (golden) tests asserting the new mapping (`l` = land, `L` = log).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Treat this as a single atomic edit; the primary risk is a **missed reference**.
  Grep the whole `internal/ui` tree (and docs are handled in task 10) for the old
  `l`→log binding and the log verb's help text.
- Verify the `?` screen and footer actually render the new mapping (golden test),
  not just that the binding constant changed.

## Input Dependencies
- Task 7: the Landing pane must exist for `l` to open it.

## Output Artifacts
- Updated keybindings + help/footer, with golden tests locking in `l` = land /
  `L` = log.

## Implementation Notes
<details>
<summary>Detailed implementation guidance</summary>

1. Find the current `l`→log binding in `internal/ui/keys.go` /
   `internal/ui/commandreg.go` (`vmCommands`). Rebind `l` to the land command
   (task 7's pane opener) and add/point `L` to the log verb.
2. Ensure `enterTarget` (commandreg.go) still routes building→log by the verb's
   **id**, so it does not depend on the key. Verify the log verb keeps working
   from `enter` mid-build.
3. Update the `?` keys screen (`internal/ui/help.go`) and the tile footer text.
4. Grep `internal/ui` for any other place the old mapping is hardcoded (status
   lines, tests' expected strings) and update consistently.
5. Golden/render tests: assert the help screen and footer show `l` = land and
   `L` = log. Update any existing golden fixtures that encoded the old mapping.
</details>
