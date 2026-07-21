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
- **PR state unknown** — the branch is pushed, but sand could not confirm
  whether a PR exists, because host `gh` is unusable or the lookup failed.
  Opening a draft PR still works (it falls back to the compare URL).
- **Never pushed, unpushed, or dirty** — work that exists only in this VM:
  a branch you created in the guest and haven't pushed, commits ahead of the
  remote, uncommitted changes, or any combination. Acting on this row
  **commits and pushes it**: sand drops you into the guest with your editor
  open on `git commit -a`, and pushes the branch when you save (setting its
  upstream if it has none). Quit the editor without saving and nothing is
  committed or pushed.
- **Nothing to land** — the checkout is on its repo's default branch with
  nothing of its own on top. Every fresh clone starts here.
- **Local-only** — the checkout has no remote configured, so there is
  nowhere for Landing to push. This is the only state with nothing to offer.
  If such a checkout holds uncommitted or unpushed work, the row still says
  so (`local only · 2 uncommitted`) — there is nothing sand can do about it,
  but you should know it is there before deleting the VM.

The commit-and-push action is the only Landing action that runs inside the
VM, and it stays there: the commit and the push both happen in the guest,
using the guest's own least-privilege push token. No diff, patch, or working
tree ever reaches your machine — see
[Security Model](../reference/security-model.md).

Opening a draft PR uses the **workstation's own `gh`** — never the guest's
own push token — so the PR is created by you, not by whatever ran inside
the VM. See [Security Model](../reference/security-model.md) for why that
split matters. Without a usable `gh` on the workstation, Landing falls back to
printing the branch's compare URL instead.

### When Landing says `gh` isn't usable

The pane's header names which mode it's in, and distinguishes two different
problems:

- **`gh: not installed`** — no `gh` on your `PATH`.
- **`gh: not authenticated`** — `gh` is there, but `gh auth status` failed.
  Run `gh auth login`, or export `GH_TOKEN` in the environment you start
  `sand` from.
- **`gh: 1Password did not authorize`** — you use the 1Password `gh` shell
  plugin (below) and the vault did not hand over the token. Unlock 1Password
  and reopen the pane.

### 1Password shell plugin

The [1Password `gh` shell plugin](https://developer.1password.com/docs/cli/shell-plugins/github/)
is supported directly — no configuration needed. If you have it set up, sand
runs `op plugin run -- gh …` instead of bare `gh`, so your token comes from
the vault exactly as it does at your own prompt.

sand detects it by reading `~/.config/op/plugins.sh` (the file `op plugin init`
generates) and checking for `op` on your `PATH`. Detection is **file-only** —
sand never runs `op` to find out, precisely because that could pop an
authorization prompt underneath the full-screen UI.

Two things worth knowing:

- **`GH_TOKEN` wins.** If `GH_TOKEN` or `GITHUB_TOKEN` is set in sand's
  environment, sand uses plain `gh` and ignores the plugin entirely. That is
  the escape hatch if you would rather sand not touch `op`.
- **1Password may need to authorize.** The first Landing action in a while can
  require unlocking 1Password. sand gives the `op` process no terminal, so a
  prompt can never corrupt the display — but it does mean an authorization
  that can only be answered in the terminal will time out. Unlock 1Password
  first and reopen the pane.

Why any of this is needed: sand runs `gh` **directly, never through a shell**,
because the branch names and repo slugs it passes come from inside the VM and
must not be able to reach a shell interpreter. The plugin's usual `gh` alias
is therefore invisible to sand — but `op plugin run -- gh …` is a plain
command, not a shell trick, so supporting it costs nothing in safety.

The token needs **`Pull requests: write`** (fine-grained), or `repo` /
`public_repo` (classic) — sand resolves the base branch and the head commit's
message, then POSTs the draft PR. Note this is the **workstation's** token,
which is a different thing from the token you provisioned into the VM: the
guest's token only ever pushes branches and never needs pull-request
permission at all.

The footer names the action for the checkout you have selected — `commit +
push`, `push`, `open draft PR`, `open in browser` — rather than a generic
"act", so you can see what `enter` will do before pressing it. A row with
nothing to do offers no key at all.

The header shows how old the listing is (`scanned 2m ago`). The rows come from
a background sweep that runs about every 60 seconds, so after committing or
pushing inside the VM's own shell the pane can briefly be out of date. Press
**`r`** to rescan immediately.

The pane's own ledger of what it did (which PR it opened, when) is
reopenable later with `L` (Log), the same key that reopens a build's or
transfer's log.

For `sand land`'s full CLI flags (`--pr`, `--web`), see the
[CLI Reference](cli-reference.md#sand-land-name).
