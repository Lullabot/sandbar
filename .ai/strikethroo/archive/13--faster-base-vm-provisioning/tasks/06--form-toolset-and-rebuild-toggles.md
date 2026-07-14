---
id: 6
group: "tier-1-toolset"
dependencies: [5]
status: "completed"
created: 2026-07-13
model: "sonnet"
effort: "medium"
skills:
  - go
  - bubbletea
---
# Surface the tool-set and a rebuild toggle in the create form

## Objective

Expose the base-image tool-set (DDEV / Go / Java) as toggles in the create form, plus a "Rebuild base image" toggle so a TUI user can actually reach the escape hatch the shrink advisory tells them to use. Today `--rebuild` is headless-only, and the form's toggle handling is hard-coded to reset mode.

## Skills Required

- **go** — `internal/ui/form.go`.
- **bubbletea** — the form's focus model and key handling.

## Acceptance Criteria

- [x] The form's toggle handling is **generalized** from the two hard-coded `preserve*` booleans into a toggle list, and **create mode** gains the same toggle focus path that reset mode has today.
- [x] Create mode shows four toggles: DDEV, Go, Java (defaulting **on**), and "Rebuild base image" (defaulting **off**).
- [x] Tab/Shift-Tab/Up/Down focus walks the text inputs and then the toggles, wrapping — matching the existing reset-mode idiom.
- [x] Space/Enter flips the focused toggle instead of navigating (matching `updateResetForm`'s behavior).
- [x] Reset mode's existing two `preserve*` toggles keep working exactly as before — no regression.
- [x] The per-field help text for the three tool toggles **states plainly that they configure the shared base image**, and that changing one re-converges (or, when a tool is removed, requires rebuilding) the base every future VM is cloned from. A base-wide effect must never be a surprise from a per-VM screen.
- [x] The "Rebuild base image" toggle maps to the existing `--rebuild` behavior.
- [x] The tool-set shrink advisory from task 5 points the user at this toggle.
- [x] `buildConfig` populates `WithDDEV` / `WithGo` / `WithJava` and the rebuild flag from the toggles.
- [x] `go vet ./...` and `go test ./...` are green, including a test that toggling Java off in create mode produces a `CreateConfig` with `WithJava: false`.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- `internal/ui/form.go` structure: an index const block (`fName…fCloneToken`), and **four parallel index-ordered slices** — `fieldLabels` (~:37-49), `fieldInfo` (~:55-73), and the `seeds` slice in `newInputs` (~:171-183). Adding a *text* field means editing all four plus `buildConfig` (~:345) and `openResetForm` (~:215-227). **The toggles avoid most of that** — they are not text inputs.
- `toggleRow(label, on, focused)` (~:580-590) renders `[x]` / `[ ]` + label. Reuse it.
- Reset mode: `resetFocusNext/Prev` (~:285-334) walk the inputs then step into `toggleFocus` 0 and 1; `updateResetForm` (~:545-573) flips on space/enter when `toggleFocus >= 0` and does not forward keys to inputs.
- Create mode: `focusNext/focusPrev` (~:269-279) currently wrap over the 11 text inputs only; `updateForm` (~:508-540) forwards everything else to inputs.
- Latent trap: `openForm` (~:201-209) does **not** reset `m.toggleFocus`, unlike `openResetForm` (~:249). Harmless today because create mode never reads it — but it becomes a real bug the moment create mode has toggles. **Reset it in `openForm`.**

## Input Dependencies

- Task 5: `CreateConfig.WithDDEV/WithGo/WithJava` and the shrink advisory exist.

## Output Artifacts

- A generalized toggle list usable by both form modes.
- Tool-set + rebuild toggles in create mode with base-wide help text.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**1. Generalize the toggles.** Replace the hard-coded pair with a small slice the form owns:

```go
type formToggle struct {
    label string
    help  string
    get   func(*model) bool
    set   func(*model, bool)
}
```

Build the active toggle list per mode (create vs reset) so `resetFocusNext/Prev` and the new create-mode focus walk share one implementation. Keep `toggleRow` as the renderer.

**2. Give create mode the toggle focus path.** Mirror `resetFocusNext/Prev`: after the last text input, step into `toggleFocus` 0..n-1, then wrap back to the first input. In `updateForm`, add the same guard `updateResetForm` uses:

```go
if m.toggleFocus >= 0 {
    switch msg.String() {
    case " ", "enter":
        t := m.toggles()[m.toggleFocus]
        t.set(m, !t.get(m))
        return m, nil
    }
    // Do NOT forward the key to the text inputs while a toggle is focused.
}
```

**3. Reset `toggleFocus` in `openForm`** (it currently isn't — see Technical Requirements). One line, prevents a stale cursor landing on a toggle.

**4. Help text — this is the point of the task, not decoration.** The tool toggles change the *shared* base, so say so:

```go
help: "Installs Java in the SHARED base image every VM is cloned from — " +
      "not just this VM. Changing this re-converges the base (removing a " +
      "tool requires a full rebuild).",
```

**5. Rebuild toggle:**

```go
{
    label: "Rebuild base image",
    help:  "Delete and rebuild the base image from scratch before creating. " +
           "Needed to actually remove a de-selected tool — Ansible cannot uninstall.",
    ...
}
```

Wire it to the same code path `--rebuild` uses (after task 7, that means passing the rebuild intent down to `ensureBaseStopped`, not deleting up-front).

**6. Test.** A focused unit test on the form model: open create form, walk focus to the Java toggle, send `space`, call `buildConfig`, assert `WithJava == false`. Do not build a comprehensive TUI test suite — one path through the new behavior is enough.

</details>
