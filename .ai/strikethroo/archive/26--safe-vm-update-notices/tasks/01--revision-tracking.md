---
id: 1
group: "vm-updates"
dependencies: []
status: "completed"
created: 2026-10-02
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
skills: ["go", "provisioning"]
complexity_score: 8
complexity_notes: "Truthful cross-provider provenance and preservation of user-owned data require careful integration."
---
# Track truthful VM base and setup revisions

## Objective
Implement additive provenance revisions, truthful successful-build capture and shared read-only update comparison for all providers. Own internal/provider, internal/manage and revision logic in internal/provision; do not edit preserve.go or home guard files. Expose an API the UI and CLI can call off the render loop.

## Skills Required
go, provisioning.

## Acceptance Criteria
- [x] Tests cover stale base, stale setup, simultaneous, current, unknown, failures, golden source and published source; go test ./internal/provider ./internal/manage ./internal/provision passes.
- [x] Preserve existing VMs, unknown history and user modifications; no automatic provisioning.

## Technical Requirements
Implement additive provenance revisions, truthful successful-build capture and shared read-only update comparison for all providers. Own internal/provider, internal/manage and revision logic in internal/provision; do not edit preserve.go or home guard files. Expose an API the UI and CLI can call off the render loop.

## Input Dependencies
Plan 26 and completed dependency tasks [].

## Output Artifacts
Implementation, meaningful tests and evidence recorded here.

## Implementation Notes
<details><summary>Execution guidance</summary>
Read PRE_TASK_EXECUTION.md and follow RED → GREEN → REFACTOR for meaningful business logic. Inspect current provider source semantics before selecting revisions. Keep host state isolated in tests. Write a few tests, mostly integration: test custom business logic, critical user workflows, edge cases and component boundaries; do not test frameworks, trivial getters or obvious static configuration. Combine related scenarios and verify the far side of file/process boundaries. Run the acceptance commands and record exact outcomes. Do not commit other tasks' changes. Set status in-progress then completed after checks. Implement additive provenance revisions, truthful successful-build capture and shared read-only update comparison for all providers. Own internal/provider, internal/manage and revision logic in internal/provision; do not edit preserve.go or home guard files. Expose an API the UI and CLI can call off the render loop.
</details>

## Execution evidence

- RED: `TestCheckUpdatesIndependentStates` failed to compile before the update state API existed; `TestSetupVersionTracksFinalizeFilesOnly` failed before the setup hash existed.
- GREEN: `go test ./internal/provider ./internal/manage ./internal/registry` passed; `go test ./internal/provision` passed. Focused revision, marker persistence, failed-finalize, golden-source, and preservation-variable tests also passed.
- The provider records base revision inside the clone lock and setup revision before the finalize guest run, publishing the latter only after success. Legacy and failed builds retain unknown history. Local/remote Lima and Proxmox golden templates carry only their recorded source revision; PVE stores it on the template without a managed tag.
- `provider.CheckUpdates` is read-only and returns independent `current`, `update_available`, or `unknown` values. Desired setup reads the checkout or embedded fileset without extracting a temp directory. No update check starts a guest or applies provisioning.

### Phase 2 integration correction

The TUI's asynchronous progress publisher can run after a provider's completion write. An optional `provider.ProgressProvenancer.MarkProgress` now serializes progress with `MarkManaged` and `Unmark` on each real provider, preserves the existing in-flight base revision, and ignores a progress event when the stored marker is ready. The explicit method also covers zero progress at the first phase banner. Lima and Proxmox marker tests cover early writes, revision retention, late progress, and a new clone's in-flight marker; filesystem and HTTP barriers cover progress reads interleaved with completion. `go test ./internal/provider` and `go test -race ./internal/provider -run 'Test(Lima|Proxmox)Progress' -count=1` passed.

### Coverage follow-up

The combined internal coverage profile showed the four real provider `Desired*Revision` methods and `CurrentPlaybookFS` untouched. Added tests drive local and remote Lima's published and legacy base identity, setup edits, unreadable configured files, and Proxmox's pinned/custom image identity through `CheckUpdates`; separate tests exercise checkout and embedded playbook sources without extraction. `TMPDIR=/var/tmp/s26 go test ./internal/provider ./internal/provision` passed. A focused coverage profile covers Lima `Desired*Revision` and `CurrentPlaybookFS` completely, and the normal Proxmox paths (the remaining failure arms require changing immutable embedded data or a production seam). Relative to the previous full profile, these tests cover 32 previously uncovered statements.
