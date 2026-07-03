---
id: 7
group: "documentation"
dependencies: [5]
status: "completed"
created: 2026-07-03
skills:
  - markdown
---
# Document the in-TUI Upload/Download workflow (replace the `limactl copy` guidance)

## Objective
Replace the current "move files in or out with `limactl copy`" guidance with the
new in-TUI Upload/Download workflow across the three places that mention it, and
reinforce that this is the in-posture replacement for `limactl copy` (no writable
share; isolation preserved).

## Skills Required
- **markdown**: concise user-facing docs; one one-line comment edit in a Go source file.

## Acceptance Criteria
- [ ] `README.md` — the line that currently tells users to "move files in or out with `limactl copy`" (around line 99) is replaced with a short pointer to the TUI's Upload/Download actions, reaffirming the security posture (this is the in-posture replacement; no writable share, no standing network/credential/mount).
- [ ] `tui/README.md` — the overlay-comment excerpt that references `limactl copy` (around line 219) is updated, AND a short subsection documents the Upload/Download actions: they live on the VM **detail** view, require a **running** VM, use one `bubbles/list` file browser (with fuzzy filter) for both host and guest, choose a **destination directory** into which the selected file/dir is placed, support typed/pasted/drag-dropped destination paths, and stream progress in the existing progress pane. Add the `u`/`d` keys to the keybindings documentation.
- [ ] `tui/internal/provision/overlay.go` — the `overlayHeader` comment's final sentence "Move files in or out with `limactl copy`." is updated to point at the TUI's Upload/Download actions instead (keep it a single tidy comment line; do not change the overlay YAML itself).
- [ ] The docs reflect the shipped v1 boundary: single file-or-directory per transfer, sequential single-pane; note that dual-pane, multi-select, and overwrite prompts are deferred (brief mention, not a roadmap).
- [ ] `cd tui && go build ./...` still passes (the `overlay.go` edit is comment-only) and no Markdown links are broken.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Documentation/prose only, plus one comment-line change in `overlay.go`. No behavioural code changes.
- Match the existing tone and structure of `README.md` / `tui/README.md`. The keybindings section in `tui/README.md` is generated-by-hand from `keys.go` and per-view handlers — add `u` (upload) and `d` (download) under the detail-view keys.
- Do not hard-code the module path or binary name in a way that would break under plans 06/07 (which rename to `sand` / `github.com/lullabot/sandbar`); refer to "the TUI" / "the detail view" rather than pinning the current binary name where avoidable.

## Input Dependencies
- Task 5: the finished Upload/Download UX and its keys (`u`/`d`), so the docs describe real, shipped behaviour.

## Output Artifacts
- `README.md` — updated file-movement guidance.
- `tui/README.md` — Upload/Download subsection + keybindings.
- `tui/internal/provision/overlay.go` — updated header comment sentence.

## Implementation Notes

<details>
<summary>Detailed implementation steps</summary>

1. `README.md` ~line 99: find the sentence ending "…Move files in or out with
   `limactl copy`." and replace the `limactl copy` guidance with something like:
   "Move files in or out from the TUI's **Upload**/**Download** actions on a VM's
   detail view — each transfer is a discrete, user-initiated `limactl copy` under
   the hood, so there is still no writable host mount or standing share and
   `limactl delete` provably removes everything."

2. `tui/README.md`:
   - Update the overlay excerpt near line 219 the same way.
   - Add a short subsection (e.g. under the keybindings or a new "Moving files
     in and out" heading): the two actions are on the **detail** view (open a VM
     with Enter, then `u`/`d`); both require the VM to be **Running**; the file
     browser is one `bubbles/list` widget used for both host and guest with
     built-in fuzzy filtering (`/`); Enter navigates into directories and a
     distinct select key chooses the highlighted file or directory (a directory
     is copied recursively); the destination is always a **directory** (the
     selection is placed inside it) entered in a prompt pre-filled with a sensible
     default and accepting typed/pasted/drag-dropped paths; progress streams in
     the existing progress pane and `ctrl+c` cancels.
   - Add `u`/`d` to the detail-view keys in the keybindings section.

3. `tui/internal/provision/overlay.go`: in the `overlayHeader` const, change the
   trailing sentence "Move files in or out with `` `limactl copy` ``." to point at
   the TUI Upload/Download actions (e.g. "Move files in or out with the TUI's
   Upload/Download actions."). Keep it inside the comment block; the change is
   prose only, so `go build` is unaffected.

4. Keep it tight and factual; reaffirm the posture ("nothing leaves the VM" is
   preserved — no share/network/credential/mount) without re-litigating the
   plan-09 comparison.
</details>
