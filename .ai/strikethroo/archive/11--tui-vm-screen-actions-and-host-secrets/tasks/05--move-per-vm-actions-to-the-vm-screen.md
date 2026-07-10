---
id: 5
group: "tui-screen-split"
dependencies: []
status: "completed"
created: 2026-07-09
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Touches the keymap, both screens' key routing, and the model's message handling at once. The detail view's stale-snapshot problem and the delete-returns-to-list exception are easy to get subtly wrong."
skills:
  - go
  - bubbletea
---
# Move per-VM actions to the VM screen

## Objective

Establish one rule: the list screen selects a VM, the VM screen acts on it. Move
start, stop, restart, shell, and delete off the list and onto the VM (detail)
screen, alongside the upload and download that already live there. Define the
**complete final keymap** in this task — including bindings whose handlers arrive
in later tasks — so no downstream task has to touch `keys.go`.

Make the VM screen survive its own actions: its displayed record must track the
VM's real state after each one, and delete must return to the list because the
record it displayed no longer exists.

## Skills Required

- **go** — struct/method refactoring across a package.
- **bubbletea** — `Update` message routing, `key.Matches` dispatch, the
  value-passed model, `tea.Cmd` sequencing.

## Acceptance Criteria

- [ ] `internal/ui/keys.go` defines the **final** keymap. `Download` rebinds from
      `d` to `g` (help text `"g", "download"`). New bindings are added:
      `StopAll` (`X`, "stop all"), `Reset` (`R`, "reset"), and `Secrets`
      (`e`, "secrets"). The obsolete `Recreate` binding is removed.
- [ ] `viewHelp()` returns, for `viewList`: `Enter`, `New`, `Filter`, `Search`,
      `StopAll`, `Quit` — and nothing else. For `viewDetail`: `Start`, `Stop`,
      `Restart`, `Reset`, `Shell`, `Delete`, `Upload`, `Download`, `Secrets`,
      `Back`, `Quit`.
- [ ] `updateList` no longer handles `Start`, `Stop`, `Restart`, `Shell`, or
      `Delete`. Those keys fall through to the table's own `Update`, where they are
      inert. It still handles `Enter`, `New`, `Filter`, `Search`, `Back`
      (clear-filter), and `Quit`.
- [ ] `updateDetail` handles `Start`, `Stop`, `Restart`, and `Shell` against
      `m.detail.Name`, reusing `beginAction` and the existing `startCmd` /
      `stopCmd` / `restartCmd` / `shellCmd`.
- [ ] The `Shell` guard survives the move: pressing `S` on a non-`Running` VM sets
      a status line telling the user to start it, and dispatches nothing.
- [ ] After a `vmsLoadedMsg`, when `m.view == viewDetail`, `m.detail` is re-seeded
      from `m.vms` by name so the rendered `Status` (and CPUs/Memory/Disk) reflect
      reality. If the VM is no longer present in `m.vms`, the model returns to
      `viewList` rather than rendering a zero-value record.
- [ ] `actionDoneMsg` no longer unconditionally implies a return to the list: the
      user stays on whichever screen dispatched the action. **Exception:** an
      `actionDoneMsg` whose `action == "delete"` sets `m.view = viewList`.
- [ ] The existing registry bookkeeping on delete (`m.reg.Remove(msg.name)`) and
      the `shell` action's empty-status special case are preserved.
- [ ] Verification: `go test ./internal/ui/... -v` passes, including new tests:
      ```
      go test ./internal/ui/... -run 'ListKeys|DetailActions|DetailRefresh|DeleteReturns' -v
      ```
      Expected `PASS`, with tests asserting (a) pressing `s` on `viewList`
      dispatches **no** command and leaves the view on `viewList`; (b) pressing `s`
      on `viewDetail` sets `m.acting` and dispatches a command; (c) a
      `vmsLoadedMsg` carrying the detail VM with `Status: "Running"` updates
      `m.detail.Status` while `m.view` stays `viewDetail`; (d) an
      `actionDoneMsg{action: "delete"}` dispatched from `viewDetail` leaves
      `m.view == viewList`.
- [ ] Verification: `go build ./... && go vet ./...` succeed, and
      `grep -rn '"d", "download"' internal/ui/` returns no matches.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Files: `internal/ui/keys.go`, `internal/ui/list.go`, `internal/ui/detail.go`,
  `internal/ui/model.go`, and `internal/ui/model_test.go`.
- **Do not** implement stop-all (task 7), reset (task 6), or the secrets editor
  (task 8) here. Define their key bindings and help entries only. A binding whose
  handler does not exist yet is simply an inert key; that is acceptable for the
  duration of one phase and is what keeps `keys.go` single-authored.
- The confirm overlay (`m.confirming`, `m.confirmName`, `m.confirmBase`) is
  **left as-is** in this task — task 6 generalises it. Delete's confirmation
  therefore still lives on the list for now. To satisfy the "delete moves to the
  VM screen" criterion without pre-empting task 6, have `updateDetail`'s `d` set
  the existing `confirming`/`confirmName` fields and leave the overlay rendering
  where it is; task 6 moves the overlay itself.
- The model is passed **by value** through `Update`. Any field you add must be
  copy-safe (no `strings.Builder`, no mutex).

## Input Dependencies

None. Runs in parallel with tasks 1–4. Task 4 touches `form.go` and `staging.go`;
this task does not.

## Output Artifacts

- The final `keyMap` and `viewHelp()`, consumed unchanged by tasks 6, 7, 8, 9.
- A VM screen that owns per-VM lifecycle actions and stays consistent under them.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Read first, in full:** `internal/ui/keys.go`, `internal/ui/list.go` (especially
`updateList`, lines 118–253), `internal/ui/detail.go`, and `internal/ui/model.go`
(especially the `actionDoneMsg` and `vmsLoadedMsg` cases in `Update`, lines
187–248).

**Step 1 — `keys.go`.** The `Download` binding currently reads:

```go
Download: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "download")),
```

with a comment claiming `d` is free on the detail view. That comment becomes
false — delete moves here — so rebind and rewrite the comment:

```go
// 'd' stays delete on every screen: it is the most destructive key and its
// meaning must never change under the user's fingers. Download took the rename.
Download: key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "download")),
StopAll:  key.NewBinding(key.WithKeys("X"), key.WithHelp("X", "stop all")),
Reset:    key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "reset")),
Secrets:  key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "secrets")),
```

Delete the `Recreate` field. Keep `Confirm` and `Cancel` — task 6 reuses them.

Note `Confirm`'s help text is currently `"y", "delete"`, which is overly specific
now that the overlay will also serve stop-all. Change it to `"y", "confirm"`.

**Step 2 — `viewHelp()`.** Rewrite the `viewDetail` and `viewList` arms. The
`viewList` arm's `m.confirming` branch currently special-cases `confirmBase` to
offer recreate; strip that to just `Confirm, Cancel`. Leave the `m.searching`
branch alone.

**Step 3 — `updateList`.** Delete the `Start`, `Stop`, `Restart`, `Shell`, and
`Delete` cases. Do **not** replace them with anything — an unmatched key falls
through to `m.table.Update(msg)` at the bottom, which ignores it. `s`, `x`, `r`
are not table navigation keys, so they are inert. Verify that `d` and `S` are
likewise not bound by `bubbles/table` (they are not; the table binds arrows,
`j`/`k`, page keys, `g`/`G`, and `home`/`end`).

⚠️ **`g` is a `bubbles/table` binding** (`GotoTop`). On the *list* this is fine —
we want the table's `g`. On the *detail* view there is no table, so `g` is free
for download. But confirm that `updateDetail` matches `Download` **before** any
fallthrough, and that the list's `Download` binding is never consulted. Since
`viewHelp` scopes help per view and `updateList` never matches `Download`, this
holds — but state it in a comment so a future reader does not "fix" it.

**Step 4 — `updateDetail`.** Add the lifecycle cases. Model them on the ones you
deleted from `updateList`, but target `m.detail.Name` instead of `selectedName()`:

```go
case key.Matches(msg, m.keys.Start):
    m.status = "starting " + m.detail.Name + "…"
    return m, m.beginAction(startCmd(m.cli, m.detail.Name))
```

For `Shell`, port the running guard verbatim, reading `m.detail.Status` rather
than re-looking-up via `vmByName`:

```go
case key.Matches(msg, m.keys.Shell):
    if m.detail.Status != "Running" {
        m.status = m.detail.Name + " must be running to open a shell (press s to start it)"
        return m, nil
    }
    m.status = "opening a shell in " + m.detail.Name + " — the TUI resumes when you exit"
    return m, shellCmd(m.detail.Name)
```

For `Delete`, set the existing overlay fields (task 6 relocates the overlay):

```go
case key.Matches(msg, m.keys.Delete):
    m.confirming = true
    m.confirmName = m.detail.Name
    return m, nil
```

Note `updateDetail`'s existing `Back`/`Enter` case returns to the list — keep it,
but make sure it is matched **after** the action cases, since `Enter` must not be
swallowed by anything new.

**Step 5 — `vmsLoadedMsg` re-seeds the detail.** In `model.go`, after
`m.refreshRows()`:

```go
// The VM screen acts on the VM it displays, so its snapshot goes stale after
// every start/stop/restart. Re-seed it from the reloaded list; if the VM is
// gone (deleted, or removed outside the TUI), fall back to the list rather
// than rendering a zero-value record.
if m.view == viewDetail {
    if v, ok := m.lookupVM(m.detail.Name); ok {
        m.detail = v
    } else {
        m.view = viewList
    }
}
```

Add `lookupVM(name) (vm.VM, bool)` beside the existing `vmByName`, which returns
a zero-value `vm.VM{Name: name}` on miss and so cannot express "absent". Keep
`vmByName` for the list's `Enter` handler, or refactor it to call `lookupVM` —
your choice, but do not leave two subtly different lookups undocumented.

**Step 6 — `actionDoneMsg`.** It currently ends with an unconditional
`return m, listCmd(m.cli)`. The refresh must stay (every action changes state),
but the view must not implicitly reset. Since `Update` never changes `m.view` in
this case today, the "stay on the current screen" behaviour is already correct —
the only change needed is the delete exception:

```go
case msg.action == "delete":
    // The record the VM screen was displaying no longer exists.
    m.view = viewList
    if err := m.reg.Remove(msg.name); err != nil { ... }
```

Place the `m.view = viewList` assignment so it runs on the **error** path too?
No — on a failed delete the VM still exists and the user should stay on its screen
to see the error. Only set it in the success branch. The `switch` already
separates `msg.err != nil` first, so putting the assignment inside the
`case msg.action == "delete":` arm (which only runs when `err == nil`) is correct
by construction. Confirm that by reading the switch.

**Step 7 — tests.** `internal/ui/model_test.go` (912 lines) already has helpers
for building a model with a fake `lima.Client` and driving `tea.KeyMsg`s. Read it
and reuse them. Add the four tests named in the Acceptance Criteria. For (a),
assert the returned `tea.Cmd` is nil and `m.acting` is false after `s` on the list.

**Testing philosophy.** Write a few tests, mostly integration. Meaningful tests
verify custom business logic, critical paths, and edge cases specific to this
application — test *your* code, not the framework or library.

Write tests for: custom business logic and algorithms; critical user workflows
and data transformations; edge cases and error conditions for core functionality;
integration points between components; complex validation logic or calculations.

Do NOT write tests for: third-party library functionality; framework features;
simple CRUD operations without custom logic; trivial getters/setters or static
configuration; obvious functionality that would break immediately if incorrect.

Here that means: the four behavioural tests above are the critical paths — key
routing per screen, the stale-snapshot fix, and the delete exception. Do not test
`bubbles/table`'s navigation or `key.Matches` itself.

</details>
