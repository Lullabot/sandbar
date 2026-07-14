---
id: 3
group: "docs-content"
dependencies: [1]
status: "completed"
created: 2026-07-14
model: "sonnet"
effort: "medium"
skills:
  - technical-writing
  - go
---
# Using sand: TUI board, secrets, files and shells

## Objective

Write the three task-oriented "Using sand" pages that are not the CLI reference — the TUI board and its keybindings, secrets management, and file transfer / shells — with every keybinding and path verified against `internal/ui/` and `internal/secrets/` rather than copied from the READMEs, which contradict each other and the code.

## Skills Required

`technical-writing` for the prose; `go` to read the TUI key handlers and the secrets store and confirm what the code actually binds and writes.

## Acceptance Criteria

- [ ] `docs/using-sand/tui.md`, `docs/using-sand/secrets.md`, and `docs/using-sand/files-and-shells.md` are written (no stubs remain).
- [ ] The keybinding table on `tui.md` is derived from the key handlers in `internal/ui/` and matches them exactly. In particular the download key is documented as **`g`** (not `u`/`d` — `README-sand.md` is wrong about this).
- [ ] `secrets.md` documents where secrets are stored on the host and where they land in the guest for each scope, and states plainly that the host store is **unencrypted** (mode `0600`).
- [ ] `uvx --with-requirements docs/requirements.txt mkdocs build --strict` exits 0 with no `WARNING`. Paste the output into your completion report.
- [ ] Report the file:line locations in `internal/ui/` from which you derived the keybinding table.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Source of truth: `internal/ui/` (key handling, board/tile model), `internal/secrets/secrets.go`, `internal/lima/` (attach/shell), `cmd/sand/`.
- No flag documentation here — that is task 4. Link to the CLI reference instead of restating it.

## Input Dependencies

Task 1: scaffold, nav, stubs.

## Output Artifacts

Three written pages under `docs/using-sand/`.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**The board.** Running `sand` with no arguments opens a Bubble Tea TUI (Charm v2 — `charm.land/bubbletea/v2`). The home surface is a **tile board**: one tile per sand-managed VM. There is no table view and no per-VM detail screen; both were deliberately removed. Per-VM verbs fire straight from the focused tile. The header shows live host CPU/memory use (fed by a guest heartbeat), free disk, and the build version. Builds stream into a progress pane but **keep running in the background** if you navigate away — say this explicitly, it is a real and non-obvious behaviour.

**Keybindings — verify every one of these against `internal/ui/` before writing them down.** At the time of the audit they were:

Board-level: arrow keys move the focus ring; `n` (or `enter` on the ghost tile) creates a VM; `/` searches; `X` stops all; `?` shows the keys screen; `q` quits.

On the focused tile: `s` start, `x` stop, `r` restart, `R` reset, `S` shell, `d` delete, `u` upload, `g` download, `e` secrets editor, `l` reopen last log.

`README-sand.md` claims the file-transfer pane opens on `u`/`d`. It does not — `d` is **delete**. Two other places in the current docs get this right; the code is the tiebreaker. Read the handler and write down what it actually binds.

Present the bindings as a markdown table with a "what it does" column, not as a bare list. Where a verb has semantics worth a sentence (Reset preserves some state and discards the rest; Delete is irreversible), give it that sentence.

**Secrets** (`internal/secrets/secrets.go`):

- Host store: `${XDG_DATA_HOME:-~/.local/share}/sandbar/secrets.json`, mode `0600`, **unencrypted**. Do not soften this. A reader deciding what to put in it needs to know it is plaintext on disk.
- Secrets are scoped `KEY=VALUE` triples. The **global** scope is written into the guest at `~/.config/sandbar/secrets.env`. A scope like `foo/bar` is written to `~/foo/bar/.env`, which direnv picks up — so a repo-scoped secret arrives as an `.env` in that repo's working directory.
- The editor opens with `e` on a tile.
- Values are streamed into guest tmpfs, never passed on argv. Worth one sentence.

**Files and shells** (`internal/lima/`, `cmd/sand/`):

- `S` on a tile, and `sand shell NAME` from the command line, both attach to a **persistent tmux session inside the guest** (prefix `C-a`). They share the same attach path, so they are the same thing with two doors. Because the session is persistent, detaching does not kill what is running in it.
- Upload (`u`) and download (`g`) open a file-transfer pane.

**Cross-linking.** These pages should link to the CLI Reference (task 4) for flags, to Secrets from the board page rather than re-explaining scopes, and to `reference/files-and-state.md` (task 5) for the on-disk paths rather than restating them. One home per fact.
</details>
