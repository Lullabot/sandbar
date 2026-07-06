---
id: 4
group: "reset-ui"
dependencies: [1, 3]
status: "completed"
created: "2026-06-27"
skills:
  - go
complexity_score: 5
complexity_notes: "Bubble Tea form state-machine changes: a reset mode that pre-fills from the registry, a locked Name field, two navigable boolean toggles, a conditional warning, and dispatch to provision.Reset; spans form.go/list.go/model.go with model_test coverage."
---
# TUI reset flow: pre-filled editable form + preserve toggles + dispatch

## Objective
Turn the existing "recreate" action into a **Reset** flow: pressing recreate on a managed VM opens the create form pre-filled with that VM's last-used settings (Name locked), exposes two preserve toggles (Claude settings; project `.env` + checkout) with a compromise warning, and on submit dispatches `provision.Reset` with the chosen options. The reset records the edited config so the next reset defaults to it.

## Skills Required
- `go` (Bubble Tea model/update/view, key handling, tests)

## Acceptance Criteria
- [ ] In the list's delete-confirm overlay, `[r]` (recreate, managed VMs only) now opens the form in **reset mode** seeded from `registry.Config(name)` (falling back to host-derived defaults when no snapshot exists), instead of provisioning immediately.
- [ ] In reset mode the **Name** field is fixed to the target VM and not editable; all other fields are editable and pre-filled.
- [ ] Two boolean toggles are shown and navigable: "Preserve Claude Code settings" and "Preserve project .env + checkout"; the project toggle is disabled when the VM has no known `CloneURL`.
- [ ] A compromise warning is displayed whenever either toggle is enabled (do not preserve if the VM may be compromised).
- [ ] Submitting validates (including disk ≥ `vm.BaseDiskFloor`) and dispatches `m.prov.Reset` with a `provision.ResetOptions` built from the toggles; the edited config is recorded as managed on success.
- [ ] `model_test.go`/`form` tests cover: reset opens pre-filled with Name locked, toggling works, the warning shows only when a toggle is on, and submit dispatches Reset with the right options. `cd tui && go test ./...` passes.

## Technical Requirements
- Reuses the existing streaming progress view and the `provisionDone → reg.Add(m.provCfg)` path (set `m.provCfg` to the edited config so the recorded snapshot reflects the reset).
- `beginProvision` takes a `provisionFunc` (`func(ctx, cfg, out) error`); dispatch `Reset` by closing over the options.

## Input Dependencies
- Task 1: `vm.BaseDiskFloor` (disk-min hint/validation).
- Task 3: `provision.Reset`, `provision.ResetOptions`.

## Output Artifacts
- The complete reset UX in the TUI.

## Implementation Notes
<details>
<summary>Detailed implementation guidance</summary>

Files: `tui/internal/ui/form.go`, `tui/internal/ui/list.go`, `tui/internal/ui/model.go`, `tui/internal/ui/model_test.go` (and `keys.go` only if you add a toggle key).

**1. Model state — `model.go`:** add fields:
```go
resetMode       bool
resetName       string // locked Name when in reset mode
preserveClaude  bool
preserveProject bool
projectToggleEnabled bool // false when the VM has no CloneURL
toggleFocus     int  // -1 = focus is in the text inputs; 0/1 = a toggle is focused
```

**2. Open reset form — `form.go`:** add
```go
func (m *model) openResetForm(name string, cfg vm.CreateConfig) tea.Cmd
```
- `m.inputs = newInputs()` then overwrite each input's value from `cfg`: Name=cfg.Name, Hostname=cfg.Hostname, User=cfg.User, GitName/GitEmail, CPUs=strconv.Itoa(cfg.CPUs), Memory, Disk, DockerProxyHost, CloneURL. Leave CloneToken blank (not stored).
- Set `m.resetMode=true`, `m.resetName=cfg.Name`, `m.preserveClaude=false`, `m.preserveProject=false`, `m.projectToggleEnabled = cfg.CloneURL != ""`, `m.toggleFocus=-1`, `m.formErr=nil`, `m.view=viewForm`.
- Focus the first **editable** field. Since Name (`fName`) is locked in reset mode, start focus at `fHostname`. Set `m.focusIdx = fHostname` and `return m.inputs[fHostname].Focus()`.

**3. List wiring — `list.go` `updateConfirm`, case `"r"`:**
- Replace the current immediate `beginProvision(..., m.prov.Recreate, cfg)` with: resolve `cfg` exactly as today (registry snapshot, else minimal fallback), set `cfg.BaseName = m.confirmBase`, `m.confirming=false`, then `cmd := m.openResetForm(name, cfg); return m, cmd`.

**4. Navigation/locking — `form.go` `updateForm` & focus helpers:**
- In reset mode, navigation must skip `fName` and extend past the last input into the two toggles. Simplest model:
  - When `m.toggleFocus == -1`, Tab/Down advances through inputs starting at `fHostname`; advancing past the last input (`fCloneToken`) moves into toggles (`toggleFocus=0`, blur inputs). Shift+Tab/Up reverses, and from `toggleFocus=0` going up returns to `fCloneToken`; never land on `fName`.
  - When a toggle is focused, **space** or **enter** toggles the corresponding bool (`preserveClaude` for 0, `preserveProject` for 1 — ignore toggle 1 if `!projectToggleEnabled`).
  - `ctrl+s` (Submit) still submits from anywhere.
- Keep create mode behavior unchanged (resetMode=false → existing logic, focus may include fName).

**5. View — `form.go` `formView`:**
- Title: "Reset VM" when `resetMode`, else "New VM".
- Render `fName` as a static, dimmed line in reset mode (no input box): e.g. `Name: <resetName> (locked)`.
- After the inputs, render the two toggle rows with checkbox glyphs (`[x]`/`[ ]`), highlighting the focused toggle; gray out the project toggle when disabled with a hint ("(no project cloned)").
- When `preserveClaude || preserveProject`, render a warning line (use `errStyle` or a dedicated warn style): "Preserving copies your Claude login and the .env token out of the VM to your host. Do NOT preserve if you suspect this VM is compromised."
- Show a disk hint near the Disk field info or as a footer: "Disk can only grow from the base floor (min " + vm.BaseDiskFloor + ")."

**6. Submit — `form.go` `submitForm`:**
- In reset mode, build the config with `cfg.Name = m.resetName` (ignore the locked field's editing). Reuse `buildConfig` then override Name; keep `BaseName` from the stored config (thread it through — store it on the model when opening, or re-read from registry in submit). Easiest: stash the opened `cfg.BaseName` on the model (`resetBaseName string`) and set it back here.
- Validate via `cfg.Validate()`; additionally enforce disk ≥ floor: parse both the requested disk and `vm.BaseDiskFloor` to bytes (add a tiny `parseSize` helper, or reuse `humanizeBytes`'s inverse if available — check `format.go`) and set `m.formErr` if smaller. If parsing is awkward, at minimum reject a disk that is empty.
- On success:
  ```go
  opts := provision.ResetOptions{PreserveClaude: m.preserveClaude, PreserveProject: m.preserveProject && m.projectToggleEnabled}
  run := func(ctx context.Context, c vm.CreateConfig, out io.Writer) error { return m.prov.Reset(ctx, c, opts, out) }
  cmd := m.beginProvision("Resetting "+cfg.Name, run, cfg)
  return m, cmd
  ```
  `beginProvision` sets `m.provCfg = cfg`, so the existing `provisionDoneMsg` handler records the edited config via `reg.Add`. Reset `m.resetMode=false` after dispatch is not required (the view switches to progress), but clear it when returning to the list.

**7. Tests — `model_test.go`:** drive `Update` with synthetic key msgs:
- Seed a registry entry (or use the in-memory registry) with a known config; simulate selecting the VM, pressing `d` then `r`; assert `m.view==viewForm`, `m.resetMode`, the Hostname/Disk inputs hold the stored values, and Name is locked (`m.resetName` set, fName not focusable).
- Simulate focusing a toggle and pressing space; assert the bool flips and the warning text appears in `formView()` output.
- Verify the project toggle is disabled when the seeded config has empty `CloneURL`.
- Keep the dispatch test light: assert that submitting a valid reset form switches to `viewProgress` (don't run the real provisioner — the fake lima client is fine, or assert state transition only).

Reference existing patterns in `model_test.go`/`format_test.go` for how the suite constructs the model and feeds `tea.KeyMsg`.
</details>
