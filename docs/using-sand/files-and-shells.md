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

## Uploading and downloading files

`u` (upload) and `g` (download) on a focused tile open a file-transfer pane.
Download is bound to `g`, not `d` — `d` is always delete; see the
[Board](tui.md#keybindings) page for the full keybinding table.

- **Upload (`u`)** copies a file or directory from this machine into the
  guest. You browse the host for a source starting at your current working
  directory, then pick a destination directory in the guest.
- **Download (`g`)** copies a file or directory out of the guest onto this
  machine. You browse the guest for a source, then pick a destination on
  the host.

Both directions require the VM to be running, and neither is offered while
a build or a reset is in progress on that VM — starting a transfer against
a VM mid-reset would stream files into an instance that's about to be
destroyed.

For the flags behind the equivalent CLI subcommands, see the
[CLI Reference](cli-reference.md).
