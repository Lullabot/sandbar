---
id: 5
group: "docs"
dependencies: [3, 4]
status: "pending"
created: 2026-07-13
model: "haiku"
effort: "low"
skills:
  - technical-writing
  - markdown
---
# Document the tmux behavior change and `sand shell`

## Objective

`S` is a deliberate breaking change to a key the user's fingers already know,
with no escape hatch — so documentation *is* the mitigation, and it has to be
good. Teach the tmux essentials to users who have never used tmux, document
`sand shell` as the way to get a second terminal into a VM, and record the third
entrypoint in AGENTS.md so it does not drift from the TUI's `S`.

## Skills Required

- `technical-writing` — user-facing prose for people who do not know tmux.
- `markdown` — editing existing docs in their established voice.

## Acceptance Criteria

- [ ] **`README-sand.md`**: the board keybinding table (~line 167, `S` → "Open an
      interactive shell in the VM (offered while it's running)") and the prose
      (~lines 175-176, "Pressing `S` suspends the TUI and hands your terminal to
      `limactl shell <name>`; the TUI resumes when you exit the shell") are both
      **wrong after this plan** and are rewritten to describe: the tmux attach,
      the persistence semantics, and the host-`$TMUX` branch (when the TUI runs
      inside host tmux, `S` opens a new host window and the TUI does **not**
      suspend). Verify the line numbers rather than trusting them — the file has
      moved before.
- [ ] The README states the tmux essentials plainly, for a reader who has never
      used tmux: the prefix is **`C-a`**; `C-a c` opens a window; `C-a |` /
      `C-a S` split; `C-a d` detaches; and — most importantly — **closing the
      terminal no longer ends the session**, work keeps running. Note that `C-a`
      is readline's "start of line", so a user who does not know they are in tmux
      would find that key mysteriously broken; say so.
- [ ] A **`sand shell`** section is added alongside the existing "Headless mode
      (`sand create`)" section, documenting it as the supported way to get a
      second terminal into a VM, and noting that a second attach gets its own
      current window (a grouped session) rather than mirroring the first.
- [ ] **`README.md`**: the guest-tmux mention (~lines 238-239) is checked and does
      not contradict the new behavior. If it is fine, say so; do not pad it.
- [ ] **`AGENTS.md`**: the "Go package layout"/entrypoint note — which today says
      there is a headless `sand create` path and a TUI path and that they must not
      drift — is extended to name **`sand shell` as a third entrypoint**, and to
      record the **shared attach-command builder as the seam** that keeps it from
      drifting from the TUI's `S`, matching the existing `provision`/`registry`
      convention.
- [ ] **`CHANGELOG.md` is NOT hand-edited.** release-please generates it from
      commits, so the breaking behavior change must be carried by the commit
      message convention instead. Do not touch the file.
- [ ] Every command, key, and flag named in the docs is checked against the code
      as merged by tasks 3 and 4 — not against this plan's prose. If they
      disagree, the code wins and you say so.

## Technical Requirements

- Markdown only. No code changes in this task.
- Match each file's existing voice and formatting (the READMEs are written in a
  direct, second-person style; AGENTS.md is terse and rationale-first).

## Input Dependencies

- Task 3 (`sand shell` as actually implemented — its exact usage string and error
  messages) and task 4 (the `S` verb's final behavior, both branches, and the
  rewritten `about` sentence in the `?` screen).

## Output Artifacts

- Updated `README-sand.md`, `AGENTS.md`, and a verified-consistent `README.md`.

## Implementation Notes

<details>
<summary>What the reader must come away knowing</summary>

The plan's UX risk section is blunt about this: `S` becomes a breaking behavior
change with no escape hatch (an explicit, accepted decision — do not editorialize
about it or hint at a `--no-tmux` flag that does not exist). The tmux prefix
`C-a` is now live in every sand shell, and `C-a` is "move to start of line" in
readline. A user who does not know they are in tmux will find that key
mysteriously broken. Documentation is the whole mitigation.

Cover, in this order:

1. `S` lands you **in tmux, inside the VM** — not a bare shell.
2. The prefix is `C-a`. `C-a c` = new window, `C-a |` / `C-a S` = splits,
   `C-a d` = detach.
3. **Closing the terminal detaches; it does not kill your work.** A Claude Code
   job started in a sand shell survives the TUI resuming, the terminal closing,
   and the laptop sleeping. This is the headline benefit — lead with it.
4. `sand shell <name>` from any terminal attaches too, and gets its own current
   window rather than mirroring the first terminal.
5. If you run the TUI inside host tmux, `S` opens a new host window and the TUI
   keeps running beside it.

Users may not realize work is still running after they detach. Note it so it is
discoverable rather than mysterious — a VM busy with detached work will not look
idle on its tile, which is the existing signal.
</details>

<details>
<summary>The AGENTS.md entrypoint note</summary>

AGENTS.md already warns that the headless (`internal/manage`) and TUI paths must
not drift, "both go through the same `provision`/`registry` seams by design".
This plan creates the same hazard a third time and solves it the same way: one
shared attach-command builder, called by both `sand shell` and the TUI's `S`,
and **neither may construct a tmux command of its own**. Write that down in the
same voice — one or two sentences, naming the builder's actual package and
function as merged by task 2.
</details>
