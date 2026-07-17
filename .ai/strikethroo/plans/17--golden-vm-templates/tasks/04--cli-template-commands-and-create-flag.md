---
id: 4
group: "cli"
dependencies: [1, 2, 3]
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "medium"
complexity_score: 6
complexity_notes: "New stdlib-flag subcommand group plus one create flag, orchestrating provider + registry; straightforward but touches user-facing behavior and destructive delete."
skills:
  - go
  - cli
---
# CLI: `sand template` Commands and `sand create --template`

## Objective
Expose the feature headlessly: a new `sand template` subcommand group (`snapshot`, `list`, `delete`) and a `--template` flag on `sand create`. These commands orchestrate the provider mechanics (task 2), the create-from branch (task 3), and the registry model (task 1), honoring the existing `--profile` selection so remote scopes work identically.

## Skills Required
- **go**: stdlib `flag` subcommands, wiring existing helpers.
- **cli**: consistent command/flag UX and clear error/warning output.

## Acceptance Criteria
- [ ] `sand template snapshot <source-vm> <template-name> [--profile P]` validates the name (`vm.ValidateTemplateName`), rejects collision with an existing template/VM/base in scope, calls `Provider.SnapshotTemplate`, writes the registry `Template` record, and prints the stop→clone→restore phases and a success line.
- [ ] `sand template list [--profile P]` prints each template in scope with name, disk size (human-readable, from `Provider.TemplateDiskBytes`), creation date, source VM, and a staleness marker when the template's playbook version differs from the current binary's.
- [ ] `sand template delete <template-name> [--profile P]` removes the template instance (`Provider.DeleteTemplate`) and its registry record; when dependent VMs exist it prints a warning listing them (via `registry.DependentsOfTemplate`) and proceeds (warn-and-allow, no `--force` gate).
- [ ] `sand create --template <template-name>` resolves the template in scope, sets `CreateOptions.TemplateSource` to the template instance name, records `TemplateSource` provenance in the new VM's registry entry, and is rejected as mutually exclusive with `--rebuild`/`--base-name`.
- [ ] `sand create --template does-not-exist` exits non-zero with an error naming the missing template.
- [ ] `go build ./cmd/sand && go vet ./cmd/sand/... && go test ./cmd/sand/... -race` passes; `gofmt -l cmd/sand` empty. Manually run `sand template --help`/`sand template list` against an empty registry and confirm graceful output (paste it).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Dispatch: extend the `switch os.Args[1]` in `cmd/sand/main.go` with a `"template"` case → `runTemplate(os.Args[2:])`; inside, switch on the next arg for `snapshot|list|delete`.
- Flags use the stdlib `flag` package with `flag.NewFlagSet(..., flag.ContinueOnError)`, matching `cmd/sand/create.go`. Reuse `bindingForProfileName(store, profile)` (in `cmd/sand/resolve.go`) to obtain the `Binding{Prov, Scope}` for the selected profile, and `profiles.Load()` for the store.
- Add `--template` to the create flag set in `create.go`; thread it through `runCreate` into the options passed to `doHeadlessCreate`/`CreateVMWithOptions`. Resolve the user name → instance name with `vm.TemplateInstanceName` and confirm the template exists in the registry scope before creating.
- All registry mutations go through the scope-aware methods from task 1; load the registry the same way create does today.

## Input Dependencies
- Task 1 (registry template CRUD + validation), Task 2 (provider snapshot/delete/disk methods), Task 3 (`CreateOptions.TemplateSource`).

## Output Artifacts
- `cmd/sand` template subcommands and the create `--template` flag; the primary headless surface the e2e test (task 6) drives.

## Implementation Notes
Follow the existing `runCreate`/`runShell` structure. Keep tests light: a unit test for name-validation/collision and mutual-exclusion rejection is enough — the deep mechanics are already tested in tasks 1–3. Use `providerfake.Provider` and `registry.NewEmpty()` for any command-level test.

<details>
<summary>Detailed implementation guidance</summary>

1. **`cmd/sand/template.go` (new):**
   - `func runTemplate(args []string) int` — if `len(args)==0` print usage; switch `args[0]`:
     - `snapshot`: parse a flagset with `--profile`; positional `<source>` `<template-name>`. Validate name; load registry; reject if `reg.TemplateInScope(name, scope)` exists or `reg.IsManagedInScope(name, scope)` or name maps to base. Call `binding.Prov.SnapshotTemplate(ctx, source, vm.TemplateInstanceName(name), out)`. On success build `registry.Template{Name, Scope, Source: source, CreatedAt: now, PlaybookVersion: res.PlaybookVersion, ToolsetKey: ..., Config: <source cfg with BaseName=TemplateInstanceName(name)>}` and `reg.AddTemplate(t)`.
     - `list`: load registry; for each `reg.TemplatesInScope(scope)` print a tab-aligned row; size via `binding.Prov.TemplateDiskBytes(vm.TemplateInstanceName(t.Name))` formatted with the project's existing human-bytes helper (search `internal/ui` / `internal/lima` for one; if none, format inline). Staleness = `t.PlaybookVersion != provision.PlaybookVersion(embeddedFS, t.ToolsetKey)` (use the same version function create uses).
     - `delete`: parse `--profile` + positional name. `deps := reg.DependentsOfTemplate(scope, name)`; if non-empty print a warning listing them. Call `binding.Prov.DeleteTemplate(ctx, vm.TemplateInstanceName(name), out)` then `reg.RemoveTemplateScoped(scope, name)`.
   - Return non-zero on any error; print errors to stderr.

2. **`cmd/sand/create.go`:** add `templateFlag := fs.String("template", "", "clone from a named golden template instead of the base image")`. In `runCreate`, if set: reject when `--rebuild` or a non-default `--base-name` is also set (mutually exclusive); resolve `inst := vm.TemplateInstanceName(*templateFlag)`; confirm `reg.TemplateInScope(*templateFlag, scope)` exists (else error); set the create options' `TemplateSource = inst` and set the persisted `cfg.BaseName = inst` + provenance `TemplateSource = *templateFlag` so reset re-clones from it. Thread the option through `doHeadlessCreate`.

3. **`cmd/sand/main.go`:** add the `case "template": os.Exit(runTemplate(os.Args[2:]))` (mirror how `create`/`shell` are dispatched).

4. **Tests (`cmd/sand/template_test.go`):** with `providerfake.Provider` (set `SnapshotTemplateFunc`/`DeleteTemplateFunc`/`TemplateDiskBytesFunc`) and `registry.NewEmpty()` on a temp path: assert snapshot rejects a colliding name; assert delete warns when a dependent VM entry exists; assert `create --template X --rebuild` is rejected. Keep it to these branches.

5. Build and run: `go build ./cmd/sand`, `go vet ./cmd/sand/...`, `go test ./cmd/sand/... -race`, then execute `./sand template list` (empty registry) and `./sand template --help`; paste output. `gofmt -l cmd/sand`.
</details>
