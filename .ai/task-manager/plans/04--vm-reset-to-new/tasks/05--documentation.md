---
id: 5
group: "docs"
dependencies: [1, 3, 4]
status: "pending"
created: "2026-06-27"
skills:
  - technical-writing
complexity_score: 2
complexity_notes: "Documentation-only; depends on the final behavior of the sizing, preserve, and UI tasks."
---
# Documentation: reset flow, disk sizing, and the staging security note

## Objective
Document the new Reset flow and the per-VM disk sizing model so users understand how to reset a VM to its settings (editing e.g. the disk), what the preserve options do, the compromise caveat, and the one-time base-rebuild migration for existing bases.

## Skills Required
- `technical-writing` (Markdown docs)

## Acceptance Criteria
- [ ] `README.md` TUI section documents Reset: editing previous settings (incl. disk), and the two preserve options.
- [ ] `README.md` documents the small-base-floor / grow-on-clone disk behavior and the one-time base rebuild needed for pre-existing large bases (delete `claude-base`; the next TUI create/reset rebuilds it at the floor).
- [ ] `README.md` Security Model section notes the scoped staging exception (preserve copies the Claude login and `.env` token to a host temp dir, deleted after restore) and the "don't preserve a compromised VM" warning.
- [ ] `tui/README.md` documents the reset keys/flow, the preserve toggles, the compromise warning, and the disk-sizing behavior/limitation (can grow from the floor; cannot shrink below it without rebuilding the base).
- [ ] Docs are consistent with the shipped behavior (TUI-only; `new-vm.sh` unchanged).

## Technical Requirements
- Accurately reflect the final implementation from tasks 1, 3, and 4 (floor value `20GiB`, preserve scope = `~/.claude/` + `~/.claude.json` and the per-org `.env` + checkout, restore ordering re-applies `settings.json`).

## Input Dependencies
- Tasks 1, 3, 4 (final behavior and constants).

## Output Artifacts
- Updated `README.md` and `tui/README.md`.

## Implementation Notes
<details>
<summary>Detailed implementation guidance</summary>

- **README.md**
  - In "Interactive TUI (`tui/`)": add a short "Reset a VM" paragraph — from the list, choose recreate on a managed VM; the create form opens pre-filled with the VM's last settings (Name locked); edit any field (e.g. a smaller disk); optionally toggle "Preserve Claude Code settings" and/or "Preserve project .env + checkout"; submit.
  - In/near "Base image and clones": explain that the base is built at a small virtual-disk floor (`20GiB`) and each clone is grown to the requested size, so disk size is chosen per-VM and an effective shrink is just a fresh clone grown to a smaller number. Note the one-time migration: existing bases built at the old (large) size won't shrink below that until rebuilt — delete `claude-base` and the next TUI create/reset rebuilds it at the floor. State the TUI-only scope (`new-vm.sh` keeps its current behavior).
  - In "Security Model": add a bullet that preserve is an opt-in, deliberate exception to "nothing leaves the VM" — the selected data (Claude login under `~/.claude` + `~/.claude.json`, and the per-org `.env` which holds `GH_TOKEN`, plus the checkout) is copied to a private host temp dir and back, then deleted; do not use preserve if you suspect the VM is compromised.
- **tui/README.md**
  - Document the reset action keys (recreate on a managed VM → pre-filled form), the two preserve toggles and the compromise warning, and that "preserve project" skips the re-clone (so no token is needed for a private repo on reset).
  - Document disk sizing: can grow from the `20GiB` floor; cannot shrink below the current base's size without rebuilding the base.
- Keep edits concise and match the existing tone/structure; do not duplicate large blocks between the two READMEs — link or keep each scoped to its audience.
</details>
