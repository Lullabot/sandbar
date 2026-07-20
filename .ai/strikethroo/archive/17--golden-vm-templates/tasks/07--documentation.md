---
id: 7
group: "documentation"
dependencies: [4, 5]
status: "completed"
created: 2026-07-17
model: "haiku"
effort: "low"
complexity_score: 2
complexity_notes: "Documentation of shipped surfaces; low reasoning, but must accurately reflect the real commands and schema."
skills:
  - markdown
  - technical-writing
---
# Documentation: Golden Templates

## Objective
Document the shipped feature accurately: a new templates page on the docs site, updates to the files-and-state and CLI/TUI references, the README feature list, the AGENTS.md registry note, and an explicit secrets-propagation caveat.

## Skills Required
- **markdown**: docs-site and repo Markdown.
- **technical-writing**: clear, accurate how-to and reference prose.

## Acceptance Criteria
- [ ] A new docs-site page explains the concept and the how-to for snapshot, create-from-template, list, and delete (both CLI and TUI), wired into the mkdocs nav (`mkdocs.yml`).
- [ ] `docs/reference/files-and-state.md` is updated for the registry **schema v4** (templates array + `TemplateSource` provenance), the reserved template instance naming, and the template version-stamp file.
- [ ] The CLI reference documents `sand template snapshot|list|delete` and `sand create --template`; the TUI/key reference documents the snapshot verb and the new-VM form's template picker.
- [ ] The README feature list mentions golden templates.
- [ ] `AGENTS.md` reflects the registry schema bump (v3 → v4, still auto-migrated on read) and the template lock discipline, if those notes exist there.
- [ ] The secrets-propagation caveat is documented: everything in the source guest disk except the per-VM identity (hostname, git identity) carries into clones; templates are per-host/per-scope and never exported.
- [ ] `mkdocs build --strict` succeeds (or the project's documented docs build), and internal links resolve.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Only edit the project's own documentation (`docs/`, `README.md`, `AGENTS.md`, `mkdocs.yml`); do not touch `.ai/` or skills directories.
- Match the exact command/flag names and behaviors shipped in tasks 4 and 5 — read those before writing. Verify the schema-v4 details against task 1.
- Keep prose consistent with the existing docs voice and structure.

## Input Dependencies
- Task 4 (CLI surface) and Task 5 (TUI surface) — document what actually shipped. Cross-check task 1 for schema details.

## Output Artifacts
- New templates docs page + nav entry; updated files-and-state, CLI/TUI reference, README, AGENTS.md.

## Implementation Notes
Accuracy over volume: every command, flag, and file path must match the code. Verify against the shipped source rather than this plan's prose where they could differ.

<details>
<summary>Detailed implementation guidance</summary>

1. Read the shipped `cmd/sand/template.go`, the `--template` flag in `cmd/sand/create.go`, and the TUI verb/form changes to capture exact names and output.
2. Add `docs/…/templates.md` (place it alongside the existing how-to/reference pages; check `mkdocs.yml` nav for the right section) covering: what a golden template is, snapshot (stop→clone→restore power state), create-from (`sand create --template` and the TUI picker), list, delete (warn-and-allow with dependents), and the fidelity/staleness note.
3. Update `docs/reference/files-and-state.md`: registry is now schema v4 (additive templates array + `TemplateSource`), reserved template instances named `sandbar-tmpl-<name>` under `${LIMA_HOME}`, and their playbook-version stamp under `${LIMA_HOME}/_sand/`.
4. Update the CLI reference page and the TUI key/verb reference page.
5. Add a bullet to the README feature list.
6. Update `AGENTS.md` registry line to say schema v4 (auto-migrated on read) and note template lock discipline if the surrounding text warrants it.
7. Run the docs build (`mkdocs build --strict` if mkdocs is available; otherwise state how you validated links) and paste the result.
</details>
