---
id: 11
group: "cleanup"
dependencies: [10]
status: "pending"
created: 2026-09-12
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Touches a persisted on-disk config format with a migration; per the rubric's risk floor, data-migration tasks never go below sonnet + high. A bad migration corrupts saved user state."
skills:
  - go
  - bubbletea
---
# Remove the tool-selection surface and migrate saved configs

## Objective

Delete the five tool checkboxes and everything behind them — CLI flags, TUI toggles, the model fields and the Ansible vars — because every image now ships every tool, and migrate saved configs that still carry the old fields with a one-time warning.

## Skills Required

`go` for the model, CLI and migration; `bubbletea` for the TUI form changes and the affected golden snapshot tests.

## Acceptance Criteria

- [ ] `CreateConfig`'s `WithClaude`, `WithDDEV`, `WithGo`, `WithJava`, `WithCodex` (`internal/vm/vm.go:103-107`) and the helpers `ToolPtrs` (`:148-156`), `ToolsetKey` (`:188-200`) and `ApplyToolset` (`:165-169`) are removed.
- [ ] The five CLI flags `--with-claude/-ddev/-go/-java/-codex` (`cmd/sand/create.go:113-120`) are removed.
- [ ] The five TUI toggles in `createToggles()` (`internal/ui/form.go:528-568`) are removed, along with the `baseWideHelp()` "installs into the SHARED base image" warning, the `m.toolClaude/toolCodex/toolDDEV/toolGo/toolJava` model fields, their reset-replay counterparts (`:740-765`), and `formToolsetCmd`/`kickFormToolsetLoad` (`:297-335`).
- [ ] The `toolset_*` Ansible variables are removed from `BuildExtraVars` (`internal/provision/vars.go:85-99`), from `roles/base/defaults/main.yml:93-97`, and from the role gates in `site.yml:29-40` — the `claude-code` and `codex` roles now always run in the base phase.
- [ ] A migration detects the old toolset fields in a saved config, emits a **one-time** notice explaining that all images now include every tool, and rewrites the config without them. Existing VMs keep working untouched.
- [ ] Verification: `go build ./...`, `go vet ./...` and `go test ./...` pass, including the TUI golden snapshot tests (regenerate them deliberately and review the diff — do not blind-accept). Paste the output.
- [ ] Verification: `sand create --help` lists no `--with-*` flag. Paste the help output.
- [ ] Verification: write a config file containing old toolset fields, run `sand`, and confirm the warning appears once, the run succeeds, and the on-disk config no longer contains those fields. Paste the before/after config and the warning.
- [ ] Verification: run `sand` a second time against the migrated config and confirm the warning does **not** reappear. Paste the output.
- [ ] Verification: `grep -rn "ToolsetKey\|ApplyToolset\|ToolPtrs\|WithClaude\|toolset_" internal/ cmd/ roles/ site.yml` returns no hits outside the migration's own detection code. Paste it.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- The migration must be **safe against partial writes**: never truncate a config in place. Write to a temp file and rename, so an interrupted migration cannot destroy saved state.
- The migration must be tolerant of a config that has *already* been migrated, and of one that never had the fields — neither should warn or rewrite.
- Unknown/extra fields in the config must not be dropped by the migration beyond the toolset ones. If the config is decoded into a struct and re-encoded, confirm nothing else is silently lost — this is the classic way a migration eats user data.
- `roles/claude-code` and `roles/codex` are currently gated on `toolset_claude` / `toolset_codex` in `site.yml:29-40`. Removing the gates means Codex is now always installed, where it was previously opt-in and defaulted to false. That is the intended behaviour ("one image with all tools") — note it for the docs task.
- Reset flows replay a recorded selection (`internal/ui/form.go:740-765`); remove the tool parts while leaving the unrelated reset toggles (Preserve Claude Code settings, Preserve project) intact.
- `provision.BaseToolset`, read asynchronously to pre-fill the form, becomes meaningless — remove it and its call sites.

## Input Dependencies

- Task 10's removal of toolset version merging, which must land first so nothing still depends on `ToolsetKey` for staleness.

## Output Artifacts

- A simplified create surface and the config migration — consumed by task 12 (migration tests) and task 14 (documentation).

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

**Scope boundary with task 10.** Task 10 removed the *version-merging* consequences of toolsets (`mergeToolsetVersion`, `shrunkTools`, the de-selection advisory). This task removes the *user-facing selection* itself and the plumbing that carried it into Ansible. If task 10 was done properly, `ToolsetKey` should already have no staleness callers, and this task is a clean excision.

**Work through the layers in this order**, checking the build between each: Ansible vars and role gates → `BuildExtraVars` → `CreateConfig` and its helpers → CLI flags → TUI toggles and model fields → migration. Going the other way leaves the compiler unable to tell you what is still wired.

**The migration is the risky part** and is why this task carries a high effort tier. The failure mode to fear is not "the warning did not print" — it is "the user's saved connection profiles, disk sizes and project settings were silently dropped because the config round-tripped through a struct that no longer had fields for them." Before writing it, look at how the config is currently decoded and re-encoded. If it decodes into a typed struct and re-encodes from it, any field not on the struct is already being lost on every write, and you should verify that rather than assume it. If it preserves unknown fields, preserve that property.

Suggested approach: decode into a generic map (or the existing type plus a catch-all), delete only the known toolset keys, write to `<config>.tmp`, `os.Rename` into place. Emit the notice once — keyed off "the fields were present and we removed them", which is naturally once, because the second run finds nothing to remove. That is simpler and more robust than storing a "have I warned" flag.

**Codex becomes always-installed.** It is currently opt-in with a default of false (`DefaultCreateConfig`, `internal/vm/vm.go:111-140`). Under "one image with all tools" it ships to everyone. This is a deliberate product change following from the work order, not an oversight — but it does mean every VM now carries an OpenAI CLI. Flag it clearly in the task record so task 14 documents it, and so it is a visible decision rather than a side effect discovered later.

**Golden snapshot tests.** The TUI form changes will break golden tests under `internal/ui`. Regenerate them, then actually read the diff: you are expecting six checkboxes to become one (the "Rebuild base image" toggle survives, though task 10 may have changed its meaning — check). A golden diff that shows something *else* changing is a real bug, and blind-accepting regenerated goldens is how it would ship.

**Do not remove the `--rebuild` flag.** It is in `createToggles()` alongside the tool checkboxes but it is not a tool selector; it survives as the explicit rebuild-from-image path task 10 preserved.

</details>
