---
id: 11
group: "docs"
dependencies: [5, 8, 9, 10]
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "medium"
complexity_score: 5
complexity_notes: "Broad but conceptually straightforward rewrite of a new feature model across ~9 doc files; accuracy matters, so above the trivial-docs tier."
skills:
  - technical-writing
---
# Update all documentation for Connection Profiles and the env-var removal

## Objective
Rewrite and update the docs so they describe the fleet/profiles model, the removal
of the env-var surface, and the secrets-store scope dimension — matching the
shipped behavior from the implementation tasks.

## Skills Required
- `technical-writing` — user + AI-facing docs, mkdocs nav.

## Acceptance Criteria
- [ ] `docs/using-sand/remote-hosts.md` is **rewritten** into a profiles-centric page ("Connection Profiles"): what a profile is; Local vs Remote SSH types; the `profiles.yaml` location/shape (secret-free, hand-editable, shareable across clients); managing profiles in the TUI; enabled/disabled/error states; and that **all enabled profiles are active at once**. The env-var instructions are **removed** and the removal is documented.
- [ ] `mkdocs.yml` nav is updated if the page is renamed (e.g. "Remote Lima Hosts" → "Connection Profiles").
- [ ] `docs/using-sand/tui.md` describes the per-tile profile label, the multi-band status bar + profile banners, the profile management screen, and the create-form profile selector.
- [ ] `docs/using-sand/cli-reference.md` documents `sand create --profile` and `sand shell` cross-profile behavior.
- [ ] `docs/reference/files-and-state.md` documents the new `profiles.yaml` config file (alongside the managed-VM index and secrets store) and notes the secrets store's new connection-scope dimension (schema bump v2→v3).
- [ ] `docs/using-sand/secrets.md` is updated if same-name-across-profiles changes how secrets are addressed/displayed per VM.
- [ ] `README.md` and `AGENTS.md` describe the fleet/profiles model, the env-var removal, and the per-operation host-access binding that replaced the `provision` process-global.
- [ ] `CHANGELOG.md` has an entry for the feature and the env-var BC break.
- [ ] `grep -rn "SAND_PROVIDER\|SAND_REMOTE_" docs README.md AGENTS.md` returns no stale usage instructions (only removal/changelog notes if any).
- [ ] If the docs build has a checker (`mkdocs build` in CI), it passes.

## Technical Requirements
- Files: `docs/using-sand/remote-hosts.md`, `mkdocs.yml`, `docs/using-sand/tui.md`, `docs/using-sand/cli-reference.md`, `docs/reference/files-and-state.md`, `docs/using-sand/secrets.md`, `README.md`, `AGENTS.md`, `CHANGELOG.md` (all confirmed present).

## Input Dependencies
- Tasks 5, 8, 9, 10: the CLI, management screen, create selector, and tile/status-bar behavior must be final so the docs describe what shipped.

## Output Artifacts
- Updated docs + changelog reflecting profiles, env-var removal, and the secrets schema bump.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- Read the final implementations (tasks 1/5/7/8/9/10) before writing so the docs
  match the actual keybindings, flag names, `profiles.yaml` shape, and UI wording.
- Emphasize the secret-free, shareable-across-clients nature of `profiles.yaml`
  (the multi-client laptop+desktop requirement) and that deleting a profile never
  changes the remote server.
- Keep the distinction between the secrets store's pre-existing **directory scope**
  and the new **connection scope** clear in `files-and-state.md`/`secrets.md` so
  readers are not confused (same caution as the code).
- Respect the memory rule: do **not** edit files under `.ai/` or skills dirs — only
  the product's own docs listed above.
</details>
