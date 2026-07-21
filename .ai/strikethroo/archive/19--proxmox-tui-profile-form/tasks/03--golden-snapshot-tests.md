---
id: 3
group: "testing"
dependencies: [1, 2]
status: "completed"
created: 2026-07-20
model: "sonnet"
effort: "medium"
skills:
  - teatest
  - bubbletea-tui
---
# testing: golden snapshots for the type picker and the Proxmox form

## Objective

Add `teatest/v2` golden-snapshot tests that pin the visual layout of the new
type picker and the Proxmox field form (including the insecure checkbox in both
states), complementing the behavioural tests written in tasks 1 and 2.

## Skills Required

`teatest` (golden snapshots, the `-update` flow) and `bubbletea-tui`.

## Acceptance Criteria

- [ ] New golden test(s) exist and pass:
      `go test ./internal/ui/ -race -run 'Proxmox|ProfileTypePicker' -v` is green.
- [ ] A golden captures the **type picker** (showing "Remote SSH" and "Proxmox").
- [ ] A golden captures the **Proxmox form** with all fields visible and the
      insecure checkbox rendered — and the checkbox appears in its toggled state
      in at least one snapshot (either a second golden or the same walk after a
      space press), proving `[x]` vs `[ ]` renders.
- [ ] The golden files under `internal/ui/testdata/` are committed and match the
      current render: re-running with `-update` produces **no** diff
      (`git diff --stat internal/ui/testdata` is empty after a plain run).
- [ ] Existing goldens (e.g. `TestTUIProfilesScreen.golden`) are unchanged — the
      new views must not alter the list screen.

## Technical Requirements

- Use the existing harness in `internal/ui/teatest_test.go`
  (`newTeaProgram`/`newTeaProgramSized`, `finalScreen`, `waitForText`) and the
  `-update` convention documented in `profilesview_golden_test.go`.
- Add the goldens in the same change as the views (tasks 1–2 already landed), and
  **review the generated files by eye** rather than blindly accepting them.
- Do not duplicate the behavioural coverage from tasks 1–2; these are layout
  snapshots.

## Input Dependencies

Tasks 1 (the Proxmox form + checkbox) and 2 (the type picker).

## Output Artifacts

New golden tests in `internal/ui/profilesview_golden_test.go` and golden files
under `internal/ui/testdata/`.

## Implementation Notes

<details>

Model the tests on `TestTUIProfilesScreen` (profilesview_golden_test.go ~:17).
Drive the program to the picker (`p` then `n`) for one golden, and into the
Proxmox form (`p`, `n`, select Proxmox) for another. To capture the checkbox
toggled, either take a second snapshot after a space press or assert both states
in one walk with two `RequireEqualOutput` checkpoints against two goldens.

Regenerate with:

```
go test ./internal/ui/ -run 'Proxmox|ProfileTypePicker' -update
```

Then open the generated `.golden` files and confirm they show, respectively, the
two-item picker and the 8-field Proxmox form with `[ ] Insecure` (and `[x]
Insecure` in the toggled snapshot). A golden that is visually wrong is a failing
gate even if the test "passes" — the point is the human-verified layout.

### Test philosophy: "write a few tests, mostly integration"

Meaningful tests verify custom business logic, critical paths, and edge cases
specific to this application. Test *your* code, not the framework.

- **When TO**: custom business logic, critical user workflows, edge/error cases,
  integration points, complex validation.
- **When NOT**: third-party/framework behaviour, trivial getters, static config,
  obvious code that would break immediately if wrong.
- **Rules**: combine related scenarios into one test; favour integration and
  critical-path coverage over per-method units; avoid one test per CRUD op;
  question whether a simple function needs its own test.

Applied here: two or three golden checkpoints (picker, form, toggled checkbox)
plus the behavioural walks from tasks 1–2 — not a golden per field or per key.

</details>
