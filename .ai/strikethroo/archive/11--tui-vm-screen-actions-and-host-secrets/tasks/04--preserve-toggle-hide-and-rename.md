---
id: 4
group: "tui-reset-form"
dependencies: []
status: "completed"
created: 2026-07-09
model: "sonnet"
effort: "medium"
complexity_score: 5
complexity_notes: "Small surface, but the focus-cycle state machine has an off-by-one hazard: a hidden toggle must never be reachable, and toggleFocus must never rest on index 1 when the project toggle is not rendered."
skills:
  - go
  - bubbletea
---
# Hide the preserve toggle when unusable; name the directory it preserves

## Objective

In the reset form, stop rendering the project-preserve toggle at all when the VM
cloned no repository (today it renders greyed out with "(no project cloned)" and
can never be selected). When it *is* shown, label it with the concrete directory
it protects — `Preserve ~/github.com/lullabot` — rather than the abstract
"Preserve project .env + checkout".

## Skills Required

- **go** — exporting an internal helper without duplicating its logic.
- **bubbletea** — the form's focus state machine (`resetFocusNext` /
  `resetFocusPrev`) and `View` rendering.

## Acceptance Criteria

- [ ] `provision.cloneOrgRelDir` is exported as `provision.OrgRelDir` (same
      signature: `func OrgRelDir(cloneURL string) (string, bool)`), and every
      existing internal caller is updated. `internal/ui` calls the exported form
      rather than re-deriving the path.
- [ ] `internal/ui/form.go` renders the project-preserve toggle **only** when
      `OrgRelDir(cfg.CloneURL)` returns `ok == true`.
- [ ] When shown, the toggle's label is exactly `Preserve ~/` + the returned
      relative directory. For `https://github.com/lullabot/sandbar` that is
      `Preserve ~/github.com/lullabot`.
- [ ] `toggleRow`'s disabled rendering path (the grey `(no project cloned)`
      suffix) is **removed** — a disabled toggle no longer reaches the screen.
- [ ] `resetFocusNext` and `resetFocusPrev` never let `toggleFocus` rest on `1`
      when the project toggle is hidden. With it hidden, forward focus from
      `fCloneToken` lands on the Claude toggle (`toggleFocus == 0`) and then wraps
      to `fHostname`; backward focus from `fHostname` lands on the Claude toggle.
- [ ] The existing `m.preserveProject && m.projectToggleEnabled` guard in
      `submitReset` is **retained** as a second line of defence.
- [ ] `provision.Reset`'s existing no-org fallback (when `PreserveProject` is set
      but `cloneOrgRelDir` returns false, it falls back to a normal clone) is
      **not** removed — the headless `sand create` path still relies on it, even
      though the TUI can no longer reach it.
- [ ] Verification: `go test ./internal/ui/... ./internal/provision/... -v` passes,
      including new focus-cycle tests. Specifically:
      ```
      go test ./internal/ui/... -run 'ResetFocus|PreserveToggle' -v
      ```
      Expected `PASS`, with a test that walks `resetFocusNext` a full cycle in the
      toggle-hidden configuration, asserts the cycle returns to `fHostname`, and
      asserts `toggleFocus == 1` is never observed; a mirrored test for
      `resetFocusPrev`; and a render test asserting `formView()` output for a
      no-clone VM contains neither `Preserve ~/` nor `no project cloned`, while for
      `https://github.com/lullabot/sandbar` it contains the literal string
      `Preserve ~/github.com/lullabot`.
- [ ] Verification: `go build ./... && go vet ./...` succeed, and
      `grep -rn "no project cloned" internal/` returns no matches.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Files: `internal/provision/staging.go` (export the helper),
  `internal/ui/form.go` (render + focus), plus their tests.
- `cloneOrgRelDir` is at `internal/provision/staging.go:27`. It returns
  `("", false)` for an empty URL or a URL with no org segment (a bare `repo` whose
  `path.Dir` is `"."`). `CheckoutRelDir` already calls it — update that call site.
- `m.projectToggleEnabled` (set in `openResetForm`) is the existing gate. Keep the
  field; change what it drives (visibility, not enabled-ness).
- Store the rendered label. `openResetForm` should compute the org dir once and
  stash it on the model (e.g. `m.projectToggleLabel string`), rather than calling
  `OrgRelDir` from `View` on every frame.
- The model is passed **by value** through `Update`; any new field must be a
  plain scalar or string. A `string` is fine.

## Input Dependencies

None. Runs in parallel with tasks 1, 2, 3, and 5. It touches `form.go` and
`staging.go`; task 5 touches `keys.go`, `list.go`, `detail.go`, and `model.go`.
There is no file overlap.

## Output Artifacts

- `provision.OrgRelDir`, exported — also usable by any later caller that needs the
  preserved path.
- A reset form whose toggles are all selectable and self-describing.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Read first:** `internal/ui/form.go` lines 211–240 (`openResetForm`), 255–307
(`resetFocusNext` / `resetFocusPrev`), 485–513 (`updateResetForm`), 515–534
(`toggleRow`), and 536–596 (`formView`). Also `internal/provision/staging.go`
lines 20–60.

**Step 1 — export the helper.** Rename `cloneOrgRelDir` → `OrgRelDir` in
`staging.go`, keeping the doc comment (update the name in it). Update the two
internal callers: `CheckoutRelDir` and `Reset` (which calls it in its
`PreserveProject` branch, around `provision.go:280` and `:333`). Do not add a
wrapper — a single name.

**Step 2 — compute the label once.** In `openResetForm`:

```go
orgRel, ok := provision.OrgRelDir(cfg.CloneURL)
m.projectToggleEnabled = ok
m.projectToggleLabel = ""
if ok {
    m.projectToggleLabel = "Preserve ~/" + orgRel
}
```

This replaces the current `m.projectToggleEnabled = cfg.CloneURL != ""`, which is
subtly wrong today: a URL like `https://github.com/repo` (no org segment) is
non-empty but yields no org dir, so the toggle would render enabled and then
silently do nothing. Exporting `OrgRelDir` and asking it directly fixes that bug
as a side effect. Add a test for that URL shape.

**Step 3 — focus cycle.** The current `resetFocusNext` already has a
`m.projectToggleEnabled` branch, but `resetFocusPrev`'s wrap-around sets
`toggleFocus = 1` only when enabled — read both carefully. The invariant to hold:

- toggle shown: cycle is `fHostname … fCloneToken → toggle0 → toggle1 → fHostname`
- toggle hidden: cycle is `fHostname … fCloneToken → toggle0 → fHostname`

and the reverse in `resetFocusPrev`. The existing code is close; verify rather
than assume. The `default:` arm of `resetFocusNext` currently wraps from "last
toggle", which is correct for both configurations only because `toggleFocus == 0`
with the project toggle disabled falls through to `default`. Confirm that reading
holds after your change, and pin it with the tests below.

**Step 4 — `updateResetForm`.** Its toggle-flip switch has
`case m.projectToggleEnabled: m.preserveProject = !m.preserveProject` as the
fallthrough for `toggleFocus == 1`. With the toggle hidden, `toggleFocus` can
never be 1, so this is now unreachable-but-harmless. Leave the guard.

**Step 5 — `toggleRow` and `formView`.** Drop `toggleRow`'s `enabled` parameter
and its `!enabled` branch entirely. In `formView`:

```go
b.WriteString(toggleRow("Preserve Claude Code settings", m.preserveClaude, m.toggleFocus == 0) + "\n")
if m.projectToggleEnabled {
    b.WriteString(toggleRow(m.projectToggleLabel, m.preserveProject, m.toggleFocus == 1) + "\n")
}
```

The compromise warning below it (`Preserving copies your Claude login and the
.env token out of the VM to your host…`) stays; it is still accurate when either
toggle is on.

**Testing philosophy.** Write a few tests, mostly integration. Meaningful tests
verify custom business logic, critical paths, and edge cases specific to this
application — test *your* code, not the framework or library.

Write tests for: custom business logic and algorithms; critical user workflows
and data transformations; edge cases and error conditions for core functionality;
integration points between components; complex validation logic or calculations.

Do NOT write tests for: third-party library functionality; framework features;
simple CRUD operations without custom logic; trivial getters/setters or static
configuration; obvious functionality that would break immediately if incorrect.

Here that means: the focus-cycle state machine is exactly the "complex validation
logic / edge case" that earns tests, in both configurations and both directions.
The render assertions are cheap and catch the label regression. Do not test
`lipgloss` styling.

`internal/ui/model_test.go` already builds models for testing — read it for the
existing construction helper and reuse it rather than inventing a second one.

</details>
