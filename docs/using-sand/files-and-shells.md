# Shells and Files

## Shells

`S` on a tile in the [Board](tui.md), and `sand shell NAME` from the command
line, both attach you to the same thing: the VM's **persistent tmux
session** inside the guest, prefixed with `C-a`. They share the same attach
path (`internal/lima.AttachArgv`), so they are two doors onto one session,
not two different mechanisms.

Because the session is persistent, detaching — with `C-a d`, or by just
closing your terminal — does **not** kill what's running in it. Attach
again later with either `S` or `sand shell NAME` and it's all still there.

Useful bindings once you're attached (tmux's own, with `sand`'s default
`C-a` prefix):

| Keys | Action |
| --- | --- |
| `C-a d` | Detach (leaves the session running) |
| `C-a c` | Open a new window |
| `C-a \|` | Split vertically |
| `C-a S` | Split horizontally |

A second terminal attaching to the same VM gets its own session grouped
against the first, sharing the same windows but tracking its own current
one — so two terminals can look at two different windows of the same VM at
once.

The VM must be running before you can shell into it; a stopped VM won't
offer `S` on its tile, and `sand shell` refuses cleanly with an error
telling you to start it first.

## Uploading and downloading files: data, not code

`u` (upload) and `g` (download) on a focused tile open a file-transfer pane.
Download is bound to `g`, not `d` — `d` is always delete; see the
[Board](tui.md#keybindings) page for the full keybinding table.

These move **data**, not code — think a SQL dump going in, or a screenshot,
video, or build artifact coming out. They are not a way to get code changes
onto or off of a VM; that path is git, and it's what [Landing](#landing)
below is for.

- **Upload (`u`)** copies a file or directory from this machine into the
  guest — for example, seeding a database dump or a fixtures file the guest
  needs but shouldn't fetch itself. You browse the host for a source
  starting at your current working directory, then pick a destination
  directory in the guest.
- **Download (`g`)** copies a file or directory out of the guest onto this
  machine — for example, pulling a screenshot, a recorded video, or a build
  artifact Claude Code produced, to look at on the host. You browse the
  guest for a source, then pick a destination on the host.

Both directions require the VM to be running, and neither is offered while
a build or a reset is in progress on that VM — starting a transfer against
a VM mid-reset would stream files into an instance that's about to be
destroyed.

For the flags behind the equivalent CLI subcommands, see the
[CLI Reference](cli-reference.md).

Files are one of two things that cross the VM boundary — for the other,
reaching a web server listening inside the guest, see
[Web Servers and Ports](web-servers.md).

## Pasting Images

`v` (paste image) on a running VM's tile, and `sand paste-image NAME` from
the command line, both stage the host clipboard's image on the guest so you
can press Ctrl-V inside Claude Code in the guest to attach it to your message.

### The workflow

1. Copy an image on your host (screenshot, photo, graphic, etc.).
2. In the `sand` TUI, press `v` on the VM's tile; or from a terminal, run
   `sand paste-image NAME` (where `NAME` is the VM's name).
3. You'll see a status message: **"staged image on NAME — press Ctrl-V in the
   guest"**.
4. In Claude Code inside the guest, press Ctrl-V to attach the image to your
   message.

The image is held in a single-slot clipboard on the guest, persisting until
you run `sand paste-image` again (overwriting it with a new image).

### How it's secure

The feature is designed to prevent clipboard **text** from leaking into the
guest. sand reads the clipboard **image-only** on your workstation, verifying
an image type is advertised before fetching any bytes. If you have text on
your clipboard instead, the command reports "no image on clipboard" and
nothing is staged. Inside the guest, the shims that serve the image to
Claude Code have no text-serving path at all — image-only by construction.

The image is read on the machine running `sand` (your workstation), not the
remote host (if you're targeting a remote Lima VM). Only the image bytes
themselves are sent across the network.

### Known limitation

A Linux *host* clipboard holding only a non-PNG image (e.g., JPEG without a
PNG variant) is treated as "no image" in v1. macOS always coerces images to
PNG, so this edge case applies to Linux hosts only. If it causes real-world
friction, it will be revisited.

The verb is only offered on running VMs. Stop or reset a VM and the verb
disappears from its tile until the VM runs again.

## Landing

`l` on a focused tile (or `sand land NAME [<path>]` from the command line)
opens the **Landing pane**: a listing of that VM's git checkouts, swept live
from the guest, with each one's branch, push state, and PR state. This is
how code — as opposed to the data `u`/`g` move above — leaves the VM: not by
copying files, but by pushing a branch and opening a PR against it, exactly
as you would from your own machine.

Each checkout lands in one of a few states, and the pane offers the action
that state calls for:

- **Pushed, no PR** — open a one-shot **draft PR** for that checkout's
  pushed branch.
- **PR already open** — open it in a browser.
- **Unpushed or dirty** — nothing to land yet; push (or commit) inside the
  VM first.
- **Local-only** (no recognized remote) — nothing Landing can act on.

Opening a draft PR uses the **workstation's own `gh`** — never the guest's
own push token — so the PR is created by you, not by whatever ran inside
the VM. See [Security Model](../reference/security-model.md) for why that
split matters. Without `gh` on the workstation, Landing falls back to
printing the branch's compare URL instead.

The pane's own ledger of what it did (which PR it opened, when) is
reopenable later with `L` (Log), the same key that reopens a build's or
transfer's log.

For `sand land`'s full CLI flags (`--pr`, `--web`), see the
[CLI Reference](cli-reference.md#sand-land-name).
