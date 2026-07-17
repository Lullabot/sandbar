---
id: 1
group: "data-model"
dependencies: []
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "On-disk schema migration (v3→v4) with backward-compat guarantee; foundation every other task builds on."
skills:
  - go
  - json-schema
---
# Template Data Model and Registry v4

## Objective
Establish the persistent data model for golden templates: derive/validate reserved template instance names in the `vm` package, and extend the `registry` package to persist template records and per-VM template provenance behind an additive v3→v4 schema migration that loads existing `managed-vms.json` files unchanged.

## Skills Required
- **go**: types, JSON (un)marshalling, table-driven tests.
- **json-schema**: versioned on-disk envelope evolution with a backward-compatible migration.

## Acceptance Criteria
- [ ] `vm.TemplateInstanceName(userName string) string` returns a reserved, prefixed Lima instance name (e.g. `sandbar-tmpl-<slug>`), and `vm.ValidateTemplateName(userName string) error` rejects empty/invalid names and any name that would collide with the reserved base instance name.
- [ ] `registry` schema `currentVersion` is bumped to `4`; the on-disk envelope gains a `templates` array; `diskEntry` gains an omitempty `TemplateSource` provenance field.
- [ ] Loading a real v3 fixture file (VMs present, no templates) succeeds, preserves every existing VM entry unchanged, and yields an empty template set — no field is dropped or repurposed.
- [ ] New exported methods exist on `*Registry`: `AddTemplate`, `RemoveTemplate` (scoped variants where the existing API is scoped), `TemplatesInScope(scope) []Template`, `TemplateInScope(name, scope) (Template, bool)`, and `DependentsOfTemplate(scope, templateName) []string` (managed VM names whose `TemplateSource` equals the template).
- [ ] `go test ./internal/registry/... ./internal/vm/... -race` passes, including a new test that round-trips a template record through save/load and a test that migrates a v3 fixture and re-saves it as v4.
- [ ] `gofmt -l internal/registry internal/vm` prints nothing and `go vet ./internal/registry/... ./internal/vm/...` is clean.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Follow the existing registry conventions in `internal/registry/registry.go`: `versionProbe{Version}` is read first to select the shape; v3 is `fileSchema{Version int; VMs []diskEntry}`; the map key is `scopedKey{scope Scope; name string}`; writes go through the unexported atomic `save()` (temp + rename) with sorted keys.
- The `Template` in-memory type must carry at minimum: user-facing `Name`, owning `Scope`, `Source` (source VM name), `CreatedAt time.Time`, `PlaybookVersion string`, `ToolsetKey string`, and the secret-free `vm.CreateConfig` inherited from the source VM (whose `BaseName` is set to the template's own instance name so a clone/reset uses the template as clone source).
- The migration must be additive only: a `version < 4` file is read via the existing legacy/v3 path, templates default to empty, `needsSave` is set, and best-effort `save()` rewrites it as v4. A `version > currentVersion` file must keep the existing "please upgrade sand" error behavior.
- `vm.ValidateTemplateName` should reuse the same character discipline the project uses for VM names (lowercase alphanumeric + hyphens); if none is centralized, add a small regex helper in `internal/vm` and note it. It must reject the reserved base name from `vm.DefaultCreateConfig().BaseName`.

## Input Dependencies
None — this is the foundation task.

## Output Artifacts
- `vm.TemplateInstanceName` + `vm.ValidateTemplateName` (and any shared name regex) in `internal/vm`.
- `registry` v4 schema, `Template` type, `TemplateSource` provenance field, and the exported template CRUD/query methods consumed by tasks 2–5.

## Implementation Notes
The test philosophy applies: write meaningful tests for the migration and the template round-trip (custom persistence logic and a data-migration critical path), not for trivial getters.

<details>
<summary>Detailed implementation guidance</summary>

1. **vm package (`internal/vm/vm.go` or a new `internal/vm/template.go`):**
   - Add `const templateInstancePrefix = "sandbar-tmpl-"`.
   - `func TemplateInstanceName(userName string) string { return templateInstancePrefix + slug(userName) }` — reuse or add a `slug` helper (lowercase, `[a-z0-9-]`, collapse repeats, trim `-`).
   - `func ValidateTemplateName(userName string) error` — non-empty after slugging, matches `^[a-z0-9][a-z0-9-]*$`, and `TemplateInstanceName(userName) != DefaultCreateConfig().BaseName`. Return clear wrapped errors.
   - Add a focused test `template_test.go` covering valid/invalid names and the base-name collision.

2. **registry package (`internal/registry/registry.go`):**
   - Bump `const currentVersion = 4`.
   - Add on-disk `diskTemplate` struct mirroring the in-memory `Template` (Name, Provider, RemoteTarget for scope, Source, CreatedAt, PlaybookVersion, ToolsetKey, Config `vm.CreateConfig`). Add `Templates []diskTemplate json:"templates,omitempty"` to `fileSchema`.
   - Add `TemplateSource string json:"templateSource,omitempty"` to `diskEntry` and mirror it on the in-memory `entry`.
   - In-memory: add `templates map[scopedKey]Template` to `Registry`; populate it in `LoadFrom` from the v4 `Templates` array; write it back in `save()` (sorted by scope+name).
   - Migration: in the `version < currentVersion` branch of `LoadFrom`, after the existing legacy/v3 lift, leave `templates` empty and set `needsSave = true` so the file is rewritten as v4 with an (empty) `templates` array. Keep the `> currentVersion` upgrade error and the `.corrupt` rename path intact.
   - Methods (mirror the scoped style already present — e.g. `AddScoped`/`RemoveScoped`/`IsManagedInScope`):
     - `AddTemplate(t Template)` / `AddTemplateScoped` — key by `scopedKey{t.Scope, t.Name}`, then `save()`.
     - `RemoveTemplateScoped(scope Scope, name string) bool` — delete + `save()`.
     - `TemplatesInScope(scope Scope) []Template` — filtered, sorted by name.
     - `TemplateInScope(name string, scope Scope) (Template, bool)`.
     - `DependentsOfTemplate(scope Scope, templateName string) []string` — scan `vms` in scope where `entry.TemplateSource == templateName`, return sorted VM names.
   - Tests (`registry_test.go` or new `template_registry_test.go`):
     - Build a v3 fixture (JSON string with `"version":3` and a couple of `vms`), write to a temp file, `LoadFrom`, assert VMs intact and `TemplatesInScope` empty, then reload the rewritten file and assert `version` is now 4.
     - Round-trip: `NewEmpty()` → `AddTemplate` → reload from its path → `TemplateInScope` returns the record with all fields equal.
     - `DependentsOfTemplate` returns exactly the VMs whose `TemplateSource` matches.

3. Run `gofmt -w`, `go vet ./internal/registry/... ./internal/vm/...`, and `go test ./internal/registry/... ./internal/vm/... -race`. Paste the passing output into your report.
</details>
