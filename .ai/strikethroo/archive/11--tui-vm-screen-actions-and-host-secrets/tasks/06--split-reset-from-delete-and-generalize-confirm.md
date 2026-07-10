---
id: 6
group: "tui-screen-split"
dependencies: [5]
status: "completed"
created: 2026-07-09
model: "sonnet"
effort: "high"
complexity_score: 6
complexity_notes: "Replaces a three-field ad-hoc overlay with a reusable value while simultaneously removing the recreate branch. The overlay must serve two screens without either growing bespoke logic."
skills:
  - go
  - bubbletea
---
# Split reset out of delete; generalize the confirm overlay

## Objective

Promote "recreate from claude-base" out of the delete confirmation into a
first-class **Reset** action bound to `R` on the VM screen. Shrink the delete
confirmation to `[y] yes / [n] cancel` and move it to the VM screen. Replace the
ad-hoc three-field overlay (`confirming`, `confirmName`, `confirmBase`) with one
reusable value that can also serve stop-all (task 7) without either screen growing
bespoke overlay logic.

No user-facing string may say "recreate" for the TUI flow.

## Skills Required

- **go** — replacing scattered state with a cohesive value type.
- **bubbletea** — confirmation overlay rendering and key routing across two views.

## Acceptance Criteria

- [ ] A reusable confirmation value exists on the model (e.g. `confirm *confirmState`
      or a `confirmState` plus an `active bool`), carrying at minimum: the prompt
      text, and the `tea.Cmd` (or a closure producing it) to dispatch on `y`.
      `confirmBase` is deleted.
- [ ] Both `viewList` and `viewDetail` route keys to the overlay when it is active,
      and both render it. Neither contains delete-specific or stop-all-specific
      overlay logic.
- [ ] The delete prompt reads exactly `Delete "<name>"?  [y] yes   [n] cancel`.
      It offers **no** `[r]` branch.
- [ ] The stray `"d"` accelerator that currently confirms a delete in
      `updateConfirm` (`case "y", "d":`) is removed. Only `y` confirms. An
      accidental double-tap of `d` must not destroy a VM.
- [ ] `R` on the VM screen opens the reset form directly (`openResetForm`), gated
      to sand-managed VMs via `manage.RecreateBase`. Pressing `R` on an unmanaged
      VM sets a status line explaining why and dispatches nothing.
- [ ] The reset pre-fill logic is preserved verbatim from the old `updateConfirm`
      `"r"` branch: use `m.reg.Config(name)` when present, otherwise a minimal
      `vm.DefaultCreateConfig()` seeded with `hostUser()`, `hostGit("user.name")`,
      `hostGit("user.email")`; then set `cfg.BaseName` from `RecreateBase`.
- [ ] `provision.Recreate` is **not** modified or deleted — it backs
      `sand create --recreate` and is not what the TUI calls.
- [ ] Verification: `go test ./internal/ui/... -v` passes, including:
      ```
      go test ./internal/ui/... -run 'Confirm|ResetGate|DeleteNoRecreate' -v
      ```
      Expected `PASS`, with tests asserting (a) the rendered delete prompt contains
      `[y] yes` and `[n] cancel` and does **not** contain `[r]`; (b) pressing `d`
      twice on the VM screen does not dispatch a delete command; (c) pressing `R`
      on a managed VM sets `m.view == viewForm` and `m.resetMode == true`; (d)
      pressing `R` on an unmanaged VM leaves `m.view == viewDetail` and sets a
      non-empty `m.status`.
- [ ] Verification: `go build ./... && go vet ./...` succeed, and
      `grep -rni "recreate from" internal/ cmd/` returns no matches, and
      `grep -rn "confirmBase" internal/` returns no matches.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Files: `internal/ui/model.go`, `internal/ui/list.go`, `internal/ui/detail.go`,
  and `internal/ui/model_test.go`.
- `keys.go` is **already final** after task 5 — do not modify it. `Reset` (`R`),
  `Confirm` (`y`), and `Cancel` (`n`/`esc`) bindings exist.
- The model is passed **by value** through `Update`. A `tea.Cmd` is a func value
  and is copy-safe; a pointer to a small struct is copy-safe. Either works.
- `manage.RecreateBase(m.reg, name) (string, bool)` is the existing managed-VM
  gate, shared with the headless path. Use it; do not reimplement the check.

## Input Dependencies

- **Task 5**: the final `keyMap` (with `Reset` bound to `R` and `Recreate`
  removed), `viewHelp()`, and `updateDetail`'s action cases — including the `d`
  handler that currently sets the old `confirming`/`confirmName` fields, which
  this task rewrites to build a confirmation value.

## Output Artifacts

- A reusable confirmation overlay, consumed by task 7 (stop all).
- Reset as a first-class, discoverable action.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Read first:** `internal/ui/list.go` `updateConfirm` (lines 255–297) and
`listView`'s confirm branch (lines 307–314); `internal/ui/model.go`'s confirm
fields (lines 72–75); `internal/manage/manage.go` for `RecreateBase`.

**Step 1 — the overlay value.** Something like:

```go
// confirmState is a pending destructive action awaiting the user's `y`. It is
// screen-agnostic: both the list (stop all) and the VM screen (delete) raise it,
// and neither needs to know what the other confirms.
type confirmState struct {
    prompt string  // e.g. `Delete "web"?`
    run    tea.Cmd // dispatched on `y`
}
```

On the model: `confirm *confirmState` (nil = inactive). A pointer keeps the
value-passed model cheap and makes "is it active" a nil check.

⚠️ A `tea.Cmd` captured in a struct field is fine, but it must be **built at raise
time**, not at dispatch time, because the VM name it closes over comes from the
screen that raised it. Build it in the `d` handler.

**Step 2 — raise it from the VM screen's `d`:**

```go
case key.Matches(msg, m.keys.Delete):
    name := m.detail.Name
    m.confirm = &confirmState{
        prompt: fmt.Sprintf("Delete %q?", name),
        run:    deleteCmd(m.cli, name),
    }
    return m, nil
```

Note `run` holds the *command*, not the `beginAction` wrapper — apply
`beginAction` when you dispatch, so the spinner starts at the right moment.

**Step 3 — a single `updateConfirm`, called from both `updateList` and
`updateDetail` before anything else:**

```go
func (m model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    switch {
    case key.Matches(msg, m.keys.Confirm): // y
        run := m.confirm.run
        m.confirm = nil
        return m, m.beginAction(run)
    case key.Matches(msg, m.keys.Cancel): // n / esc
        m.confirm = nil
        return m, nil
    }
    return m, nil // swallow every other key while confirming
}
```

The old `case "y", "d":` is gone — only `Confirm` matches. This is the
double-tap fix.

Both `updateList` and `updateDetail` start with:

```go
if m.confirm != nil {
    return m.updateConfirm(msg)
}
```

**Step 4 — render it from both views.** Factor the prompt line:

```go
func (m model) confirmView() string {
    return errStyle.Render(m.confirm.prompt + "  [y] yes   [n] cancel")
}
```

`listView` already has a `switch { case m.confirming: … case m.status != "": … }`
— change the guard to `m.confirm != nil` and call `confirmView()`. Add the same
branch to `detailView`, which today only renders `m.status`.

**Step 5 — `R` on the VM screen.** Port the old `updateConfirm` `"r"` branch:

```go
case key.Matches(msg, m.keys.Reset):
    name := m.detail.Name
    base, ok := manage.RecreateBase(m.reg, name)
    if !ok {
        m.status = "reset is only available for sand-managed VMs"
        return m, nil
    }
    // Pre-fill from the VM's recorded config (sizing, hostname, identity) rather
    // than resetting to defaults. The clone token is not stored, so a VM that
    // cloned a private repo will need it re-supplied.
    cfg, found := m.reg.Config(name)
    if !found || cfg.Name == "" {
        cfg = vm.DefaultCreateConfig()
        cfg.Name = name
        cfg.User = hostUser()
        cfg.GitName = hostGit("user.name")
        cfg.GitEmail = hostGit("user.email")
    }
    cfg.BaseName = base
    return m, m.openResetForm(name, cfg)
```

Note the status string says **reset**, not recreate. Sweep for the other
occurrences: `updateConfirm`'s old `"recreate is only available for…"`, the
`listView` prompt's `[r] recreate from %s`, and the comments in `list.go` and
`registry.go` that describe the gate. Comments describing `provision.Recreate`
(the headless path) may keep the word — it is still that function's name.

**Step 6 — delete the old fields.** `confirming`, `confirmName`, `confirmBase`
all go. `grep -rn "confirmBase\|confirmName\|m.confirming" internal/` must come
back empty.

`openResetForm` already sets `m.view = viewForm` and returns a focus `tea.Cmd`;
it is unchanged by this task. Task 4 may have changed its body concurrently —
if there is a conflict, task 4's changes to `openResetForm` (the
`OrgRelDir`-derived label) are authoritative and this task only calls it.

**Testing philosophy.** Write a few tests, mostly integration. Meaningful tests
verify custom business logic, critical paths, and edge cases specific to this
application — test *your* code, not the framework or library.

Write tests for: custom business logic and algorithms; critical user workflows
and data transformations; edge cases and error conditions for core functionality;
integration points between components; complex validation logic or calculations.

Do NOT write tests for: third-party library functionality; framework features;
simple CRUD operations without custom logic; trivial getters/setters or static
configuration; obvious functionality that would break immediately if incorrect.

Here that means: the double-tap-`d` regression and the managed-VM reset gate are
the edge cases worth pinning. The prompt-string assertion is cheap insurance
against the `[r]` branch creeping back.

</details>
