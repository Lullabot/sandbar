---
id: 4
group: "documentation"
dependencies: [1, 2]
status: "completed"
created: 2026-07-20
model: "haiku"
effort: "low"
skills:
  - technical-writing
---
# docs: reflect that the TUI now creates Proxmox profiles

## Objective

Update the documentation to reflect that a Proxmox profile can now be created in
the TUI (via `p` → `n` → Proxmox), removing the caveat that says it cannot.

## Skills Required

`technical-writing` — accurate, consistent with the existing docs voice.

## Acceptance Criteria

- [ ] `uvx --with-requirements docs/requirements.txt mkdocs build --strict`
      succeeds (no broken links, nav intact) — the runnable verification.
- [ ] The Step 6 admonition in `docs/using-sand/proxmox.md` that says the TUI
      **cannot** create proxmox profiles is removed, and the text now presents the
      `p` → `n` → Proxmox flow as an alternative to hand-editing `profiles.yaml`
      (the YAML example stays as the reference/automation path).
- [ ] `docs/using-sand/connection-profiles.md`'s "Managing profiles → In the TUI"
      section mentions that creating a profile now offers a type choice
      (Remote SSH / Proxmox).
- [ ] `AGENTS.md` no longer implies the profile form is remote-ssh-only; it notes
      the type picker and that the form now has a non-text (checkbox) input.
- [ ] `grep -rn "cannot create\|hand-edit" docs/using-sand/proxmox.md` shows the
      caveat is gone (or reworded to "you can also edit the YAML directly",
      never "the TUI can't").

## Technical Requirements

- Edit `docs/using-sand/proxmox.md` (Step 6 note), `docs/using-sand/connection-profiles.md`
  (Managing profiles → In the TUI), and `AGENTS.md` (the profile-form description).
- Do **not** touch the `pveum` Proxmox-side setup (that is unchanged) or the
  token-file instructions (also unchanged).

## Input Dependencies

Tasks 1 and 2 (the feature must exist before the docs claim it).

## Output Artifacts

Updated `docs/using-sand/proxmox.md`, `docs/using-sand/connection-profiles.md`,
`AGENTS.md`.

## Implementation Notes

<details>

The current Step 6 note in `docs/using-sand/proxmox.md` reads (paraphrase): "The
TUI's profile screen can create `local` and `remote-ssh` profiles but **not**
`proxmox` ones yet — add a Proxmox profile by hand-editing `profiles.yaml`."
Replace it with something like: creating a profile in the TUI (`p` → `n`) now
offers a type choice including **Proxmox**; the `profiles.yaml` form shown here is
the equivalent hand-edit / automation path. Keep it short — one small note or a
sentence, matching the surrounding Material admonition style.

In `connection-profiles.md`, the "Managing profiles → In the TUI" section
currently walks `n`/`enter`/`t`/`d`. Add that `n` now opens a **type picker**
(Remote SSH or Proxmox) before the field form.

In `AGENTS.md`, the profile form was described as branching only on
`TypeRemoteSSH`; update to say the form supports local, remote-ssh, and proxmox,
reached via a type picker on create, and that the proxmox form includes a boolean
**checkbox** input (`insecure`) — the one place the profile form is not purely
text inputs — so future agents do not assume homogeneity.

Verify with `uvx --with-requirements docs/requirements.txt mkdocs build
--strict` and remove the generated `site/` directory afterwards (do not commit
it).

</details>
