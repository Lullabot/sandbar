---
id: 8
group: "docs"
dependencies: [3, 5, 6]
status: "pending"
created: 2026-07-09
model: "sonnet"
effort: "low"
skills:
  - technical-writing
---
# Documentation — Rewrite Token/Secrets Docs, Reference Issue #3

## Objective
Update the human-facing documentation to describe the new host-backed secrets manager: `sand secret` commands, VM-global vs directory-scoped secrets, managed (invisible) direnv, live GitHub token rotation, and multiple GitHub tokens per VM. Replace the current direnv/`GH_TOKEN` token-lifecycle narrative.

## Skills Required
- **technical-writing** — accurate, security-aware edits to `README.md` and `README-sand.md`.

## Acceptance Criteria
- [ ] `README.md`'s GitHub-token lifecycle section is rewritten: secrets are stored on the host, VM-global and directory-scoped secrets exist, direnv is managed by `sand` (no manual `direnv allow`), GitHub tokens rotate live, and multiple tokens per VM are supported. The old advice to use a "separate VM per org/context rather than juggling several tokens" is removed/updated.
- [ ] The doc clearly states the live-update boundary: git/GitHub rotations are immediate; plain environment variables require a new shell.
- [ ] `README-sand.md` documents the `sand secret set|list|rm|sync` surface, the host store location (`${XDG_DATA_HOME:-~/.local/share}/sandbar/secrets/<vm>.json`, mode `0600`), the create/reset re-render behaviour, and the TUI secrets panel.
- [ ] Issue #3 is referenced as delivered by the TUI "refresh GitHub token" action.
- [ ] `grep -n "direnv allow" README.md` shows no instruction telling the *user* to run `direnv allow` manually (it is now managed).

## Technical Requirements
- Edit `README.md` (currently ~lines 205–270 cover the token lifecycle) and `README-sand.md`.
- No AGENTS.md/CLAUDE.md exists at the repo root, so no AI-facing config file needs updating.
- Keep instructions consistent with the actual shipped CLI/TUI (tasks 3, 5, 6).

## Input Dependencies
- Task 3: final `sand secret` CLI surface.
- Task 5: `sand secret sync` behaviour and the live/deferred distinction.
- Task 6: the TUI secrets panel wording (issue #3).

## Output Artifacts
- Updated `README.md` and `README-sand.md`.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

This is a documentation task — mechanical relative to the implementation, but accuracy about the security model matters, so verify wording against the shipped commands rather than paraphrasing this plan. Rewrite the numbered token-lifecycle steps in `README.md` to reflect: (1) create/manage secrets with `sand secret` or the TUI; (2) they live on the host and are re-applied on recreate; (3) GitHub auth is file-backed and rotates live; (4) multiple org-scoped tokens coexist in one VM (call out the porting-between-repos use case that motivated it); (5) generic env vars need a new shell after a change. In `README-sand.md`, add a "Secrets" section covering the CLI, the store path/permissions, and the TUI panel. Reference issue #3 as resolved.
</details>
