---
id: 17
group: "provider-wiring"
dependencies: [8, 9, 10, 11]
status: "pending"
created: 2026-09-28
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
complexity_score: 8
complexity_notes: "Changes persisted golden-template provenance across both providers and registry schema v4; incorrect lineage silently labels old templates current or makes template-backed resets rebuild from the wrong source."
skills:
  - go
  - data-migration
  - testing
---
# Carry baked-image provenance through golden VM templates

## Objective

Update the golden-template feature already present on `main` so template freshness and clone lineage use the baked image version rather than the retired playbook/toolset stamp, while preserving template-backed create and reset behavior across Lima and Proxmox.

## Skills Required

`go` for the provider, provisioning, registry, CLI, and TUI paths; `data-migration` for schema-safe conversion of existing template records; `testing` for assertions that cross the registry/provider boundary.

## Acceptance Criteria

- [ ] `provision.SnapshotResult` and `registry.Template` carry a provider-neutral baked-image version; `PlaybookVersion` and `ToolsetKey` are removed from the live template model.
- [ ] A snapshot inherits the image version of the VM it actually captures. A VM cloned from a user template inherits that template's version recursively; it is not stamped with whatever image the current binary happens to pin.
- [ ] Lima and Proxmox derive the same result without assuming the transport is Lima/SSH. Provider-specific metadata lookup remains behind the existing provider/provenance seams.
- [ ] Template status in `sand template list` and the TUI Source selector compares the recorded image version with the currently pinned manifest version. Missing or legacy metadata renders `unknown`, never falsely `current`.
- [ ] Existing registry v4 template records with `playbook_version` / `toolset_key` load safely and migrate without losing `Scope`, `Source`, `CreatedAt`, or `Config`. A legacy playbook hash is not reinterpreted as an image version.
- [ ] Creating from a golden template and resetting a template-backed VM continue cloning the reserved template instance directly; neither path downloads, rebuilds, or substitutes the shared baked base.
- [ ] Deleting a template still warns about dependent VMs, and those dependents retain their recorded source even though recreation will fail once the source is gone.
- [ ] Verification: focused tests cover base-derived snapshots, template-derived snapshots, unknown legacy provenance, template create, and template reset for both provider shapes.
- [ ] Verification: `go test ./internal/provision ./internal/provider ./internal/registry ./cmd/sand ./internal/ui` and `go test ./... -race` pass.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- The current implementation computes template freshness from `provision.PlaybookVersion` in `cmd/sand/template.go`, `internal/ui/form.go`, `internal/provision/template.go`, and `internal/provider/proxmoxtemplate.go`; all four must move to the manifest image version together.
- A normal managed VM does not carry its base stamp under its own instance name. Resolve lineage from recorded provenance/config and the actual clone source rather than falling back to the current binary's manifest version.
- Do not collapse published base images and user-created golden templates into one type or command surface. They are different layers: the published image seeds the shared provider base; a golden template captures mutable user VM state above it.
- Preserve the registry's locked `mutate` discipline. Any schema migration must re-read under lock before saving and must retain concurrent updates.
- Preserve the current snapshot power-state contract and cleanup semantics; this task changes metadata, not stop/clone/restart behavior.

## Input Dependencies

- Task 08's Lima baked-base version stamp.
- Task 09's Proxmox baked-base template version.
- Task 10's removal of playbook/toolset base-staleness semantics.
- Task 11's registry migration for removed base-tool selections.

## Output Artifacts

- Image-version-aware golden-template provenance and migration across the registry, providers, CLI, and TUI.
- Regression tests proving template-backed create/reset bypass the shared base path.

## Implementation Notes

The landed golden-template implementation predates baked images. It records `PlaybookVersion` and `ToolsetKey`, computes “current” by hashing the embedded playbook, and may fall back to the current build's hash when the source VM has no stamp. That fallback becomes actively wrong after this plan: a snapshot taken from a VM built on image A while the binary pins image B must remain stamped A.

Treat the manifest image version as lineage, not as a property inferred from the current executable. The CLI and TUI already know the source VM's recorded `CreateConfig.BaseName`; the provider provenance seam covers cases recovered from backend metadata. Use those facts to trace the actual clone source. If a legacy record cannot prove its image version, retain the template and display `unknown`.

The strongest test begins with a VM recorded against old image A, switches the test manifest to B, snapshots that VM, and asserts the new template records A. A test that snapshots only against the current version cannot catch the fallback bug.
