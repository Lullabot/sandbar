---
id: 5
group: "tui"
dependencies: [1, 2, 3]
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Touches the board's most-tested code (command registry, new-VM form, job progress, golden snapshots); integration across UI state and provider calls."
skills:
  - go
  - bubbletea
---
# TUI: Snapshot Verb, Template Picker, and Delete

## Objective
Add first-class board integration: a per-VM "snapshot to template" verb that prompts for a name and runs as a tracked job with progress, a source selector in the new-VM form offering the base plus in-scope templates (with size/age/staleness), and a template delete affordance whose confirmation shows the disk reclaimed and any dependent VMs. Update golden snapshot tests accordingly.

## Skills Required
- **go**: Bubble Tea model/update wiring, provider/registry calls from commands.
- **bubbletea**: command registry, form fields, job-backed progress streams, golden snapshot tests.

## Acceptance Criteria
- [ ] A new `vmCommand` (id `snapshot`, a key binding, `about` text) is registered in `internal/ui/commandreg.go`, enabled for VMs where a clean clone is possible (running or stopped, not mid-build via `notBuilding`), that prompts for a template name and starts a job showing stopping→cloning→restoring progress.
- [ ] The new-VM form (`internal/ui/form.go`) gains a source selector listing "base" plus `registry.TemplatesInScope(scope)`, showing each template's size/age/staleness inline; choosing a template creates the VM via the task-3 template path.
- [ ] A template delete affordance runs `Provider.DeleteTemplate` + `registry.RemoveTemplateScoped` behind a confirmation that displays the disk size reclaimed and lists `registry.DependentsOfTemplate` (warn-and-allow).
- [ ] Golden snapshot tests are regenerated and committed; new/updated `testdata/*.golden` reflect the verb, the form selector, and the delete confirmation.
- [ ] `go build ./... && go vet ./internal/ui/... && go test ./internal/ui/... -race` passes; `gofmt -l internal/ui` empty.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- `vmCommand` shape (from `internal/ui/commandreg.go`): `{ id string; binding key.Binding; about string; enabledFor func(m model, v boardVM) bool; action func(m *model, v boardVM) tea.Cmd }`. Add the entry to the `vmCommands` slice; gate with a predicate like `notBuilding`.
- Long-running work goes through the job/progress machinery in `internal/ui/progress.go` + `jobs.go`: use `beginProvision`/`beginStream`/`beginJob` patterns (they set up an io.Pipe → `jobRegistry` → viewport → spinner and call `markProvision`). The snapshot job should stream `Provider.SnapshotTemplate` output and, on success, write the registry `Template` record.
- The new-VM form state lives on `model` (`internal/ui/model.go`): input fields indexed by the `fName..fCloneToken` consts, toggles via `createToggles()`. Add a source selection (an index or a small enum) and render it in the form view; when a template is selected, disable/hide the "Rebuild base image" toggle (mutually exclusive, mirroring the CLI).
- Golden tests use `github.com/charmbracelet/x/exp/golden` (`golden.RequireEqual`); regenerate with `go test ./internal/ui/ -run <TestName> -update`. Snapshot the new states deterministically (avoid embedding the wall-clock date directly in a golden — format ages/sizes from injected fixtures).

## Input Dependencies
- Task 1 (registry template queries), Task 2 (provider snapshot/delete/disk methods), Task 3 (create-from-template path).

## Output Artifacts
- TUI verb, form source selector, and delete flow; updated `internal/ui/testdata/*.golden`.

## Implementation Notes
This is the board's most-tested surface — follow the existing command-registry and job patterns exactly and extend golden tests alongside each change rather than after. Keep new tests to golden snapshots of the new states plus one behavioral test that the snapshot verb starts a job; do not unit-test framework behavior.

<details>
<summary>Detailed implementation guidance</summary>

1. **Snapshot verb (`commandreg.go`):** add
   ```go
   { id: "snapshot", binding: key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "snapshot→template")),
     about: "Snapshot this VM into a reusable golden template",
     enabledFor: notBuilding,
     action: func(m *model, v boardVM) tea.Cmd { return m.openSnapshotPrompt(v) } },
   ```
   Pick a key not already bound in `keys.go`/`vmCommands` (verify: s/x/r/R/S/d/u/g/e/l are taken — `t` is free). `openSnapshotPrompt` collects a name (reuse the existing text-prompt/form mechanism), validates via `vm.ValidateTemplateName`, then starts a job that calls `binding.Prov.SnapshotTemplate` and on success `reg.AddTemplate(...)`.

2. **Job wiring:** model the snapshot job on `beginProvision`/`beginJob` in `progress.go`. If the job kind enum (`kindProvision`/`kindTransfer`) doesn't fit, either reuse `kindProvision` or add a `kindSnapshot`; whichever, ensure `deriveStatus`/`Building` handle it. Stream output into the viewport as existing jobs do.

3. **Form source selector (`form.go`, `model.go`, form view):** add `formSource` state to `model` (e.g. `formTemplateName string` where `""` == base). Render a selectable line above the toggles listing base + `reg.TemplatesInScope(m.formScope)` with `size · age · [stale]`. On submit, if a template is chosen set the create options' `TemplateSource` and persist provenance (task 3/4 semantics). Hide the "Rebuild base image" toggle when a template is selected.

4. **Delete affordance:** either a new command on template rows or an action in a lightweight template view. Confirmation text: `Delete template "X" (<size>)? N VM(s) were cloned from it: a, b`. On confirm call `binding.Prov.DeleteTemplate` + `reg.RemoveTemplateScoped`.

5. **Golden tests:** find the existing UI golden tests (e.g. `*_golden_test.go` under `internal/ui`), add cases that render the new verb help, the form with the selector, and the delete confirmation from fixed fixtures, then run `go test ./internal/ui/ -run <TheseTests> -update` and commit the resulting `testdata/*.golden`. Re-run without `-update` to confirm green.

6. Build/verify: `go build ./...`, `go vet ./internal/ui/...`, `go test ./internal/ui/... -race`, `gofmt -l internal/ui`. Paste passing output. If you can drive the TUI headlessly (teatest), capture a short transcript; otherwise rely on the golden coverage.
</details>
