# The Board (TUI)

Running `sand` with no arguments opens a terminal UI: a **tile board**, one
tile per sand-managed VM. There is no table view and no per-VM detail
screen — both were deliberately removed. Every verb fires straight from the
tile under the focus ring.

## The header

The header band shows a live readout of the **host**, not the VMs: CPU and
memory currently in use (fed by a guest heartbeat), free disk on the volume
that holds your VMs, and the build's version. It does not count base images
or unmanaged VMs — the board only ever shows sand-managed clones, and the
header doesn't either. Manage a base image with `limactl` directly.

## The tile board

Tiles are sorted alphabetically by name and stay there — a VM changing
status never reorders the board. An empty slot on the board is a "ghost
tile" inviting you to press `enter` to create a VM.

Builds stream their output into a progress pane, but **they keep running in
the background if you navigate away**. Leaving the progress screen does not
cancel a build in progress; the job keeps going in the registry, and you can
reopen its log later (`l`) to see how it finished — including one that
failed while you weren't looking.

## Keybindings

### Board-level

These act on the board itself, regardless of which tile is focused.

| Key | Action |
| --- | --- |
| `↑` `↓` `←` `→` | Move the focus ring between tiles |
| `enter` (on the ghost tile) | Create a new VM |
| `n` | Create a new VM |
| `/` | Search / filter tiles by name |
| `X` | Stop all VMs |
| `?` | Show the keys screen |
| `q` | Quit |

### On the focused tile

These fire on whichever tile the focus ring is currently on. Not every verb
is offered on every tile — for example `s` (start) only appears when the VM
isn't already running, and `S` (shell), `u` (upload), and `g` (download) all
require the VM to be running.

| Key | Action | What it does |
| --- | --- | --- |
| `s` | Start | Boot the VM. Its host-stored secrets are written into the guest as it comes up. |
| `x` | Stop | Shut the VM down cleanly. Its disk and its secrets are kept. |
| `r` | Restart | Stop the VM and start it again, applying any secrets you've changed since it booted. |
| `R` | Reset | Delete this VM and clone it fresh from its base image, keeping its name and sizing. Everything inside the guest is lost; the create form opens pre-filled so you can change the settings first. Only offered for VMs sand created. |
| `S` | Shell | Attach a shell to the guest's persistent tmux session. Work keeps running after you detach (`C-a d`) or close the terminal. See [Files and Shells](files-and-shells.md). |
| `d` | Delete | Delete the VM and its disk, after a confirmation. Its host-stored secrets go with it. **Irreversible.** |
| `u` | Upload | Copy a file or directory from this machine into the guest. You pick the source, then the destination directory. See [Files and Shells](files-and-shells.md). |
| `g` | Download | Copy a file or directory out of the guest onto this machine. See [Files and Shells](files-and-shells.md). |
| `e` | Secrets | Edit this VM's secrets. Saving writes them into a running guest immediately; a stopped one gets them on its next start. See [Secrets](secrets.md). |
| `l` | Log | Reopen the log of this VM's last build or file transfer — including one still running, or one that failed. |

`d` is always delete, on every screen — the most destructive key never
changes meaning under your fingers. Download deliberately does **not** use
`d`; it's bound to `g` instead.

For the full set of `sand` subcommands and flags (including `sand shell`),
see the [CLI Reference](cli-reference.md).

## Keybinding sources

The tables above are transcribed directly from the code, not copied from
older docs (which disagree with each other and with the code on more than
one binding):

- Board-level bindings: `internal/ui/keys.go:39-70` (`newKeyMap`) and
  `internal/ui/board.go:51-60` (`boardMove`, `ghostEnter`).
- Per-tile verbs, their gating, and their "what it does" sentences: the
  `vmCommands` registry in `internal/ui/commandreg.go:89-267`. In
  particular, the download binding is `internal/ui/commandreg.go:230-243`
  (key `g`), and delete is `internal/ui/commandreg.go:191-210` (key `d`) —
  confirming that download is `g`, not `u`/`d` as older docs claimed.
