---
id: 6
group: "sand-paste-image"
dependencies: [2, 3, 4, 5]
status: "pending"
created: 2026-07-17
model: "haiku"
effort: "low"
skills:
  - documentation
---
# Documentation: `sand paste-image` workflow and contract

## Objective
Document the new feature for users and future maintainers: the command, the TUI
verb, the copy-image → `sand paste-image` → Ctrl-V workflow, the image-only
guarantee, and the guest shim mechanism.

## Skills Required
- `documentation` — user-facing README/docs plus AI-facing notes.

## Acceptance Criteria
- [ ] User docs (README and/or `docs/`) describe `sand paste-image <vm>`, the TUI
      `v` "paste image" verb, and the end-to-end workflow, stating explicitly that
      it transfers **images only** and never clipboard text.
- [ ] The guest shim + single-slot path (`~/.sand/clip/latest.png`) and its
      install location are noted where the provisioning role is documented.
- [ ] `AGENTS.md` (or the relevant AI-facing note) records the invariant: the
      paste path is image-only by contract — do not add a text fallback to the
      clipboard seam or the guest shim.
- [ ] The known limitation (a Linux host clipboard holding only a non-PNG image
      is treated as "no image" in v1) is documented.
- [ ] If docs build via mkdocs, `mkdocs build` (or the repo's doc check) succeeds.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Match the existing documentation structure (README sections, `docs/` pages,
  `mkdocs.yml` nav if a new page is added).
- Keep the security framing accurate: host clipboard is read one-shot,
  image-only, on the machine running sand; nothing auto-syncs; text can never
  transit.

## Input Dependencies
- Tasks 2–5: the shipped command, verb, delivery, and shim being documented.

## Output Artifacts
- Updated README / `docs/` pages, provisioning-role doc note, and `AGENTS.md`
  invariant.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

- Pull the exact behavior from the plan's Executive Summary and the shipped code;
  do not restate implementation internals users don't need.
- Include a short "How it works / security" note: sand reads the clipboard image
  on your workstation (macOS/Linux), writes just that image into the guest, and a
  guest shim serves it to Claude Code's Ctrl-V. It never reads clipboard text.
- Cross-link the command and the TUI verb so both discovery paths land on the
  same explanation.
</details>
