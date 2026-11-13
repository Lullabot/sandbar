---
id: 6
group: "docs"
dependencies: [3, 4, 5]
status: "completed"
created: 2026-11-13
skills:
  - technical-writing
complexity_score: 3
---
# Documentation and AGENTS.md

## Objective
Document the VS Code workflow for users and the new seams for agents.

## Skills Required
Technical writing (MkDocs Material).

## Acceptance Criteria
- [x] User docs on opening a VM in VS Code: `c` / `sandbar code`, prerequisites (VS Code + Remote-SSH extension, `code` command), the managed file and one-time Include, per-backend notes (local Lima tracks port changes; remote Lima authorizes which public key, re-added after reset, needs guest `AllowTcpForwarding`; Proxmox IP refresh — press `c` again), troubleshooting.
- [x] `cli-reference.md` documents `sandbar code`; `tui.md` lists `c`; `files-and-state.md` lists the managed `ssh_config` and the `~/.ssh/config` edit.
- [x] AGENTS.md: `EditorSSH` on the provider seam, `internal/sshconfig` (Match-all / prepend rule, lock discipline), `internal/vscode`, and the `c` ↔ `sandbar code` parity pair.
- [x] `uvx --with-requirements docs/requirements.txt mkdocs build --strict` exits 0.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
Any new page goes in `mkdocs.yml` nav. No nautical metaphors.

## Input Dependencies
Tasks 3, 4, 5 (final behaviour).

## Output Artifacts
Updated docs and AGENTS.md.

## Implementation Notes
<details>

Keep prose concise and match the existing pages' tone. Verify every fact (paths, messages, key, flags) against the implemented code rather than the plan.

</details>
