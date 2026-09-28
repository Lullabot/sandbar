---
id: 11
group: "cleanup"
dependencies: [10]
status: "pending"
created: 2026-09-12
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
complexity_score: 7
complexity_notes: "Touches the persisted registry and golden-template records; the rubric's migration risk floor requires capable models and high effort."
skills:
  - go
  - bubbletea
---
# Remove base-tool selection while preserving per-VM agent choices

## Objective

Delete the three shared-base dependency choices (DDEV, Go, and Java) and everything behind them because every published base image contains them. Preserve the four coding-agent choices (Claude Code, Codex, OpenCode, and Pi), which `main` now installs per VM during finalize and remembers through `internal/agentprefs`. Migrate registry records, including golden-template configs, that still carry the retired base-tool fields.

## Skills Required

`go` for the model, CLI and migration; `bubbletea` for the TUI form changes and the affected golden snapshot tests.

## Acceptance Criteria

- [ ] `CreateConfig.WithDDEV`, `WithGo`, and `WithJava`, plus `ToolPtrs`, `ToolsetKey`, and `ApplyToolset`, are removed. `WithClaude`, `WithCodex`, `WithOpenCode`, `WithPi`, and `AgentPtrs` remain.
- [ ] The CLI flags and TUI toggles for DDEV, Go, and Java are removed. The four `--with-<agent>` flags and agent toggles remain and continue adopting/saving `agentprefs.Selection`.
- [ ] `formToolsetCmd`, `kickFormToolsetLoad`, and `provision.BaseToolset` are removed; the golden-template Source selector and its delete interaction remain intact.
- [ ] Base-only `toolset_ddev`, `toolset_go`, and `toolset_java` Ansible plumbing is removed. Agent `toolset_claude`, `toolset_codex`, `toolset_opencode`, and `toolset_pi` variables remain because the agent roles run during finalize.
- [ ] A registry migration detects retired base-tool fields in both `vms[].config` and `templates[].config`, emits a one-time notice explaining that published images include the base dependencies, and rewrites without losing any VM/template provenance or agent selections.
- [ ] Verification: `go build ./...`, `go vet ./...` and `go test ./...` pass, including the TUI golden snapshot tests (regenerate them deliberately and review the diff — do not blind-accept). Paste the output.
- [ ] Verification: `sand create --help` lists no `--with-ddev`, `--with-go`, or `--with-java`, and still lists all four agent flags.
- [ ] Verification: write a config file containing old toolset fields, run `sand`, and confirm the warning appears once, the run succeeds, and the on-disk config no longer contains those fields. Paste the before/after config and the warning.
- [ ] Verification: run `sand` a second time against the migrated config and confirm the warning does **not** reappear. Paste the output.
- [ ] Verification: `grep -rn "ToolsetKey\|ApplyToolset\|ToolPtrs\|WithDDEV\|WithGo\|WithJava" internal/ cmd/` returns no hits outside migration compatibility code; agent fields and `toolset_<agent>` remain.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- The migration must be **safe against partial writes**: never truncate a config in place. Write to a temp file and rename, so an interrupted migration cannot destroy saved state.
- The migration must be tolerant of a config that has *already* been migrated, and of one that never had the fields — neither should warn or rewrite.
- Unknown/extra fields in the config must not be dropped by the migration beyond the toolset ones. If the config is decoded into a struct and re-encoded, confirm nothing else is silently lost — this is the classic way a migration eats user data.
- Coding agents are per-VM software on `main`, not base dependencies. Do not bake them, make them unconditional, remove their flags, or bypass `agentprefs.LoadOrMigrate` / `agentprefs.Save`.
- Reset flows replay recorded base and agent state. Remove only the retired base-tool parts while leaving agent selection and Preserve Agents / Preserve project behavior intact.
- `provision.BaseToolset`, read asynchronously to pre-fill the form, becomes meaningless — remove it and its call sites.
- The managed registry is schema v4 and now stores golden templates as well as VMs. The migration must preserve `TemplateSource`, template records, and every unrelated `CreateConfig` field.

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

**Agents remain per VM.** Since this plan was drafted, `main` generalized the coding-agent lifecycle: Claude Code, Codex, OpenCode, and Pi are installed during finalize, selected independently, and remembered outside the shared base stamp. That architecture already solves the staleness and image-size problem that the old lazy-shim task was trying to address. Preserve it.

**Golden snapshot tests.** The TUI form changes will break golden tests under `internal/ui`. Regenerate them, then read the diff. The expected change is removal of the three base-tool toggles; the Source row, four agent choices, and Rebuild base image toggle remain. Explicitly inspect `TestTUIFormSourceSelectorGolden` and template-delete goldens so a broad regeneration cannot erase the newly landed template UX.

**Do not remove the `--rebuild` flag.** It is in `createToggles()` alongside the tool checkboxes but it is not a tool selector; it survives as the explicit rebuild-from-image path task 10 preserved.

</details>
