---
id: 5
group: "docs"
dependencies: [4]
status: "completed"
created: 2026-06-26
skills:
  - technical-writing
---
# Document the TUI (`tui/README.md` + root README subsection)

## Objective
Write user-facing documentation for the new Go TUI: a `tui/README.md` covering
build/run/prerequisites and the CRUD keybindings, plus a short subsection in the
root `README.md` pointing to it and clarifying that `scripts/new-vm.sh` remains the
scripted/CI path.

## Skills Required
- **technical-writing**: clear, accurate developer documentation.

## Acceptance Criteria
- [ ] `tui/README.md` documents: what the tool is, prerequisites (`limactl`/Lima, and `ansible` reachable for provisioning), how to build (`cd tui && go build -o claude-vm ./cmd/claude-vm`), how to run, and a keybindings table for all CRUD actions (list/detail, create, start/stop/restart, delete/recreate, quit).
- [ ] Root `README.md` gains a short "Interactive TUI (`tui/`)" subsection linking to `tui/README.md` and stating that `new-vm.sh` is unchanged and still the curl|bash / CI entry point.
- [ ] Docs match the actual keybindings and commands implemented in task 04 (verify against the code, do not invent).
- [ ] No broken relative links.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Markdown only. Cross-check every command and keybinding against the task-04 implementation before writing.

## Input Dependencies
- Task 04: the implemented UI (for accurate keybindings) and `cmd/claude-vm` (for the build/run commands).

## Output Artifacts
- `tui/README.md`
- Updated `README.md` (root) with the new subsection.

## Implementation Notes

<details>
<summary>Detailed implementation steps</summary>

1. Read `tui/internal/ui/model.go` (and the help bar) to capture the exact
   keybindings, then write the `tui/README.md` keybindings table from the code, not
   from assumptions.

2. Suggested `tui/README.md` outline:
   - **Overview**: a Bubble Tea TUI to manage the project's disposable Claude Code
     Lima VMs (full CRUD), reimplementing `scripts/new-vm.sh`'s orchestration in Go.
   - **Prerequisites**: `limactl` (Lima) on PATH; the playbook checkout (run from
     within the repo so the provisioner can locate `site.yml`).
   - **Build & run**: `cd tui && go build -o claude-vm ./cmd/claude-vm && ./claude-vm`.
   - **Keybindings** table (mirror the implemented keys: navigate, `enter` detail,
     `n` new, `s`/`x`/`r` start/stop/restart, `d` delete/recreate, `q` quit).
   - **Relationship to `new-vm.sh`**: same base-image/clone/finalize model, same
     security posture (ephemeral VM, read-only playbook mount, secrets in tmpfs).

3. In the root `README.md`, add a brief subsection under the Lima quick-start (after
   the "Base image and clones" area) titled **Interactive TUI (`tui/`)** with one or
   two sentences and a link to `tui/README.md`; explicitly note the bash script is
   unchanged.

4. Keep it concise and accurate; do not document features that were not built.
</details>

### Meaningful Test Strategy Guidelines
Documentation task — no automated tests. Verify accuracy by cross-referencing the
task-04 code.
