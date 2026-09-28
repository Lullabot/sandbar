---
id: 4
group: "documentation"
dependencies: [2, 3]
status: "pending"
created: 2026-09-28
models:
  anthropic: "claude-haiku-4-5"
  openai: "gpt-6-luna"
effort: "low"
skills:
  - technical-writing
  - mkdocs
---
# Document onboarding and release updates

## Objective
Document the shipped onboarding workflow, Warp warning, release indicator, browser fallbacks, persisted files, and architectural invariants for users and future coding agents.

## Skills Required
Concise technical writing and MkDocs Material navigation/link validation.

## Acceptance Criteria
- [ ] `docs/using-sand/tui.md` explains first-run onboarding, dismissal, help-based reopening, link/keyboard behavior, Warp warning, daily check, and header update notice without duplicating the detailed tmux guide.
- [ ] `docs/reference/files-and-state.md` lists onboarding state, release cache, and cache lock paths with contents, XDG fallbacks, permissions/deletion consequences, and concurrency purpose.
- [ ] `docs/reference/troubleshooting.md` gives concise Warp compatibility and default-browser failure guidance with appropriate links.
- [ ] `AGENTS.md` records the pre-board child-view invariant, origin-aware return behavior, async daily throttle and failure policy, header shedding priority, trusted-link boundary, and XDG/browser/HTTP test isolation.
- [ ] `uvx --with-requirements docs/requirements.txt mkdocs build --strict` exits 0.
- [ ] Documentation matches implemented keys, file names, and behavior, and `git diff --check` exits 0.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
Update existing pages only; do not move long-form prose into `README.md` or create a redundant tmux guide. Keep user-facing wording aligned with the actual UI and cross-link `docs/using-sand/files-and-shells.md` for tmux depth.

## Input Dependencies
Tasks 2 and 3's final UI keys, messages, state/cache paths, warning text, and header behavior.

## Output Artifacts
Updated MkDocs user/reference/troubleshooting documentation and `AGENTS.md` maintenance guidance.

## Implementation Notes
<details>
<summary>Execution guidance</summary>

Inspect the completed code and tests before naming keys or file semantics. Explain that deleting onboarding state re-shows the screen and deleting cache permits a new check, without implying VM data loss. State that failed checks are quiet and consume the daily interval. Use existing relative-link and admonition conventions, then run the strict build.

</details>
