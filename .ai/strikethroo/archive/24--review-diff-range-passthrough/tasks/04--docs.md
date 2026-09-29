---
id: 4
group: "review-range"
dependencies: [2, 3]
status: "completed"
created: 2026-09-29
models:
  anthropic: "claude-haiku-4-5"
  openai: "gpt-6-luna"
effort: "low"
skills:
  - technical-writing
---
# Document the review range

## Objective
Document the CLI pass-through and TUI prompt in the user docs and `AGENTS.md`.

## Skills Required
`technical-writing` (MkDocs Markdown).

## Acceptance Criteria
- [ ] `docs/using-sand/review.md` has a "review only the last few commits" section with CLI (`-- HEAD~2`, `-- HEAD~2...HEAD`, `-- --staged`) and TUI (`R`) examples, how it interacts with a saved review and `--clean`, and that the automatic base and file-count guard apply only with no range.
- [ ] `AGENTS.md`'s `landreview` entry notes that `Session.DiffArgs` replaces the resolved base and skips the size guard, and that the resume probe is separate.
- [ ] Verify: `uvx --with-requirements docs/requirements.txt mkdocs build --strict` succeeds.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
Match the existing docs' tone; no nautical terms; do not reference plans in the text.

## Input Dependencies
Final CLI and key behaviour from tasks 2 and 3.

## Output Artifacts
Updated `docs/using-sand/review.md` and `AGENTS.md`.

## Implementation Notes
<details>

Read `docs/using-sand/review.md` first and add the section in its existing structure. Keep the `AGENTS.md` note to a few sentences.
</details>
