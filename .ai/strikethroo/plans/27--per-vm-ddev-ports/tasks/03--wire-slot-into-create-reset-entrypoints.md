---
id: 3
group: "entrypoints"
dependencies: [1]
status: "pending"
created: 2026-11-05
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
skills:
  - go
  - go-testing
complexity_score: 7
complexity_notes: "Touches CLI create/reset/recreate and the TUI create/reset paths; each rebuilds CreateConfig field by field, so a missed carry silently changes a VM's ports."
---
# Allocate and carry the ddev slot through every create/reset entrypoint

## Objective
Make every create and reset path allocate a slot for eligible VMs before provisioning and carry an already-recorded slot unchanged, and print the router ports after a successful CLI create/reset.

## Skills Required
Go (cmd/sandbar CLI, internal/ui Bubble Tea model), Go testing.

## Acceptance Criteria
- [ ] `adoptRecordedConfig` (`cmd/sandbar/create.go`) copies `rec.DDEVPortSlot`, so `sandbar reset` (`resetConfigFromRecord`) and `sandbar create --recreate` keep a recorded slot.
- [ ] The TUI reset path keeps a recorded slot: `openResetForm` captures it (new model field beside `resetWithDDEV`) and `buildConfig` replays it in reset mode.
- [ ] `sandbar create` (new VM, including `--template`), `sandbar create --recreate`, `sandbar reset` (including `--from-backup`), and the TUI's create (`submitForm`) and reset (`submitReset`) call `manage.AssignDDEVSlot` with the target profile's type, its provenance markers and `reg.ManagedInScope(scope)` BEFORE provisioning starts, so the in-flight marker written at clone time already carries the slot. A recorded slot is never re-probed.
- [ ] Inputs: the CLI calls the provider's `Provenancer.Provenance(ctx)` once (tolerating an error by falling back to registry-only); the TUI uses its cached `mem.provenance` for the owning member — no new I/O on the TUI submit path.
- [ ] After a successful `sandbar create` or `sandbar reset` of a VM with a slot, the CLI prints one line naming the router HTTP and HTTPS ports and the URL form, e.g. `ddev: http://<project>.ddev.site:20120  https://<project>.ddev.site:20121`. No TUI rendering change; `go test ./internal/ui -run TestTUI` goldens unchanged.
- [ ] Tests prove a recorded slot survives each of: `resetConfigFromRecord`, `adoptRecordedConfig` (recreate), the TUI reset `buildConfig`, and a `reset.json` write/read; and that a create on a Lima profile with `WithDDEV` records a slot in the registry/marker while a Proxmox create or `WithDDEV=false` does not; and that a reset of a slot-less eligible VM allocates one that does not collide with another VM in the same scope.
- [ ] `go test ./...`, `go vet ./...` pass; `gofmt -l .` empty.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- `cmd/sandbar/create.go` (`runCreate`, `adoptRecordedConfig`, `doHeadlessCreate`), `cmd/sandbar/reset.go` (`resetConfigFromRecord`, reset/`--from-backup` flow), `internal/ui/form.go` (`openResetForm`, `buildConfig`, `submitForm`, `submitReset`), `internal/ui/model.go` (reset field).
- Profile type: CLI create has `profile profiles.Profile`; CLI reset has `target`; TUI member is `m.members[i].profile.Type` (see `memberIndex(m.formScope)`).
- Use `internal/providerfake.Provider` for provider-facing tests; isolate host state (`isolateHostState(t)` in `internal/ui`, `XDG_DATA_HOME`/`TMPDIR` temp dirs elsewhere). No `t.Parallel()`.

## Input Dependencies
Task 1: `vm.CreateConfig.DDEVPortSlot`, `vm.DDEVPortsForSlot`, `manage.AssignDDEVSlot`.

## Output Artifacts
Slot recorded in registry + provenance marker for new/reset eligible VMs; CLI port line (task 4's CI step may read it, task 5 documents it).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- Read AGENTS.md's "A reset keeps the target VM's identity and project" and the comments in `adoptRecordedConfig` and `buildConfig`: both deliberately start from `DefaultCreateConfig()` and copy chosen fields. The slot is identity, like the name and clone URL: it is always taken from the record and has no flag or form row.
- `--from-backup` decodes the config from `reset.json`; once task 1 serializes the field it carries through. Still call `AssignDDEVSlot` there so a slot-less manifest of an eligible VM gets one.
- Order on create: build cfg → `AssignDDEVSlot` → provision. The registry/marker record happens in `manage.RecordSuccess*` from the same cfg, so nothing else is needed for persistence. Check that the TUI create passes the same cfg value (with the slot) to both the provision job and the success bookkeeping (`provisionDoneMsg` handling / `beginJob`), and that `settleSecrets` (`cmd/sandbar/secrets.go`) mirror stays consistent.
- `AssignDDEVSlot` excludes `cfg.Name` from the held set, so a reset never collides with its own previous record.
- For the CLI, `p.(provider.Provenancer)` is already type-asserted in `runCreate` for RecordSuccess; reuse it. On error from `Provenance(ctx)`, print nothing extra and fall back to registry-only (pass a nil map).
- The port line: derive from `vm.DDEVPortsForSlot`. Keep it short; print only when the slot is set and WithDDEV is true. Use `<project>` literally — sandbar does not know the ddev project name.
- Test philosophy — write a few tests, mostly integration. Meaningful tests verify custom business logic, critical paths, and edge cases specific to this application; test our code, not the framework or library. Prefer one table test per entrypoint family over per-method tests.
- Verification: `go test ./cmd/sandbar/... ./internal/ui/... ./internal/manage/...`, then `go test ./...`, `go vet ./...`, `gofmt -l .`.
</details>
