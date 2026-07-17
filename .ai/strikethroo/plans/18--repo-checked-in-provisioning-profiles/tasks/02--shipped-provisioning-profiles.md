---
id: 2
group: "shipped-profiles"
dependencies: [1]
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "high"
skills:
  - ansible
  - go
complexity_score: 8
complexity_notes: "Touches the embed/rsync/hash triple-pin and the base-image toolset stamping invariants (ToolsetKey, mergeToolsetVersion, BaseToolset); a mistake here churns every existing shared base or breaks dual-tier idempotency."
---
# Restructure the four optional dev tools into shipped provisioning profiles

## Objective

Re-express the existing optional dev tools (`claude`, `ddev`, `go`, `java`) as named shipped provisioning profiles, using the same manifest format Task 1 defines, living in a dedicated directory embedded alongside the playbook. Preserve all current behavior exactly: the shipped profiles must remain applicable at the base-image tier (today's path — fast clones, existing `ToolsetKey()`/base-stamp/union-merge staleness semantics, `--with-*` flags and TUI toggles keep working with current defaults) while also becoming reusable per-clone content that Task 3's finalize stage can apply when a repo declares one of these tools in its `toolset` group.

## Skills Required

Ansible (role composition, phase-agnostic/idempotent role authoring) and Go (the embed/rsync/hash triple-pin in `playbook_embed.go` and `internal/provision/provision.go`, and the toolset-stamping code in `internal/vm/vm.go` / `internal/provision/baseversion.go`).

## Acceptance Criteria

- [ ] The `claude-code` role and the `toolset_*`-conditional fragments of `roles/base` and `roles/dev-tools` (see `roles/base/tasks/main.yml:171-207,256-276` and `roles/base/defaults/main.yml:66-76`) are reorganized so each of the four tools maps cleanly onto a shipped profile manifest in Task 1's format, without changing what gets installed or when for the existing base-phase path.
- [ ] Each shipped profile's underlying role content is idempotent and phase-agnostic: it must produce the same correct result whether applied at base-build phase (today) or per-clone at finalize phase (new, via Task 3) — e.g. ddev's apt-repository registration (`roles/base/tasks/main.yml:171-207`) must not assume base-build-only context.
- [ ] `internal/vm/vm.go`'s `ToolPtrs()`, `ToolsetKey()`, and `ApplyToolset()` are unchanged in behavior (same four tool names, same key format, same union-only merge semantics via `mergeToolsetVersion` in `internal/provision/baseversion.go`).
- [ ] The `--with-claude/--with-ddev/--with-go/--with-java` CLI flags (`cmd/sand/create.go`) and the TUI create-form toggles (`internal/ui/form.go`) continue to work exactly as today — same defaults, same `BaseToolset()`-seeded behavior — with no new flags, form fields, or `vm.CreateConfig` fields added.
- [ ] `provision.BuildExtraVars` (`internal/provision/vars.go`) emits byte-identical output for the base phase compared to before this change (verify by running the existing `internal/provision/vars_test.go` and `internal/provision/toolset_packages_test.go` unchanged, or updating them deliberately if — and only if — the restructuring requires a documented, intentional change; do not delete these tests).
- [ ] New shipped-profile files are added to all three of: the `go:embed` directive in `playbook_embed.go`, the in-guest rsync allowlist filter (`internal/provision/provision.go`'s `inGuestScript`), and `TestGuestSyncCopiesOnlyThePlaybook` / its fixture in `internal/provision/playbooksync_test.go` — run `go test ./internal/provision/... -race -run TestGuestSync` and confirm it passes.
- [ ] `ansible-playbook --syntax-check site.yml` passes after the restructuring.
- [ ] `go test ./... -race -covermode=atomic -coverpkg=./internal/... -coverprofile=coverage.out` passes and coverage stays at or above the existing floor (87%, per `.github/workflows/test.yml`).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Read the plan's "Shipped Provisioning Profiles (Toolset Restructuring)" section for the authoritative requirements, including the "dual use makes idempotent, phase-agnostic role design a hard requirement" constraint.
- Do not modify `site.yml`'s base-phase gating logic (`toolset_claude|default(true)|bool` etc.) in a way that changes base-phase behavior — the restructuring is about *where the reusable content lives and how it's packaged*, not about changing when/how it applies at the base tier.
- The shared base's `PlaybookVersion` stamp format and staleness detection must be unaffected for existing bases — this is the plan's top-listed technical risk ("Unintended base churn").
- Coordinate the shipped-profile manifest's `toolset` naming with Task 1's validator known-name list (the four names: `claude`, `ddev`, `go`, `java`).

## Input Dependencies

Task 1's manifest schema and format (the shipped profiles must use the same declarative shape as repo profiles).

## Output Artifacts

- A shipped-profiles directory (embedded alongside the playbook) containing one manifest + role content per tool, reusable by Task 3's finalize stage for per-clone toolset reconciliation.
- Updated embed/rsync/hash triple-pin covering the new files.
- Unchanged base-phase behavior, proven by existing + updated seam tests.

## Implementation Notes

This task is explicitly named a top technical risk in the plan ("Unintended base churn": if any repo-specific value leaked into base-phase extra-vars or the playbook hash, every repo would invalidate every teammate's shared base). Before starting implementation work, read `AGENTS.md`'s section "The base image / clone / finalize provisioner (read before touching internal/provision)" in full.

Follow RED → GREEN → REFACTOR: before touching `roles/base`/`roles/dev-tools`/`roles/claude-code`, first run the existing `TestGuestSyncCopiesOnlyThePlaybook`, `internal/provision/vars_test.go`, and `internal/provision/toolset_packages_test.go` to confirm they pass on the current tree (RED/baseline), then make the reorganization in small increments, re-running after each file move/addition to catch triple-pin drift immediately rather than at the end.

Do not attempt Task 3's finalize-stage wiring in this task — this task only produces the reusable shipped-profile artifacts and preserves base-tier behavior. Task 3 consumes these artifacts for per-clone reconciliation.
