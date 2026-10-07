# Shells and Files

## Shells

`S` on a tile in the [Board](tui.md), and `sandbar shell NAME` from the command
line, both attach you to the same thing: the VM's **persistent tmux
session** inside the guest, prefixed with `C-a`. They share one attach path,
so they are two doors onto one session, not two different mechanisms — and
that holds wherever the VM runs. A VM on another machine or on Proxmox is
reached over SSH instead, which `sandbar` sets up for you; nothing about the
session changes.

Because the session is persistent, detaching — with `C-a d`, or by just
closing your terminal — does **not** kill what's running in it. Attach
again later with either `S` or `sandbar shell NAME` and it's all still there.

Useful bindings once you're attached (tmux's own, with `sandbar`'s default
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
offer `S` on its tile, and `sandbar shell` refuses cleanly with an error
telling you to start it first.

### Copying text out of the guest

Select with the mouse, or `C-a [` then `v`/`y` in copy mode, and the text
lands on the clipboard of the terminal you're sitting at — not just in the
guest's tmux buffer. Because the mouse belongs to tmux (`mouse on` is part
of the shipped config), a drag no longer makes a terminal-native selection
you could copy yourself, so this is the path.

It works over [OSC 52](https://invisible-island.net/xterm/ctlseqs/ctlseqs.html#h3-Operating-System-Commands),
an escape sequence tmux writes back through the shell to your terminal
emulator, which then puts the text on your system clipboard. Two things have
to be true on your side for it to arrive:

- **Your terminal has to support OSC 52.** Most current ones do, but a few
  gate it behind a setting (iTerm2's "Applications in terminal may access
  clipboard", xterm's `allowWindowOps`) because a program that can write
  your clipboard can also clobber it. If a copy silently does nothing,
  that's the first thing to check.
- **If you run `sandbar` inside your own tmux**, configure the host tmux with
  `set -s set-clipboard on`. Its default, `external`, allows its own copies
  but blocks clipboard writes from the guest tmux. The host tmux also needs
  the clipboard capability for your terminal; see the configuration below.

A VM created before sandbar shipped this doesn't need rebuilding: the setting
is applied at attach time as well as baked into the guest's `~/.tmux.conf`,
so the next `S` or `sandbar shell` has it.

### Claude Code and host tmux settings

The guest configuration includes
[Claude Code's recommended tmux settings](https://code.claude.com/docs/en/terminal-config#configure-tmux)
for Shift+Enter, notifications and terminal progress updates. It also enables
focus events and recognizes a host tmux as an extended-key terminal. These
settings are applied on every `S` or `sandbar shell` attachment, including to
an existing VM's running tmux server, without restarting its sessions.

If you run sandbar inside tmux on your workstation, add the following to your
**host's** `~/.tmux.conf` too (tmux 3.3 or newer):

```tmux
set -g allow-passthrough on
set -s extended-keys on
set -as terminal-features ',xterm*:extkeys'
set -s focus-events on
set -s set-clipboard on
```

The outer terminal must support extended keys. If tmux does not detect them,
add an `extkeys` entry matching that terminal's `$TERM` outside tmux. For
clipboard support, check `tmux info` inside the host tmux: if `Ms` is
missing, add a matching clipboard feature. For example, for a terminal
advertising `xterm-256color` and supporting OSC 52:

```tmux
set -as terminal-features ',xterm-256color:clipboard'
```

See [tmux's nested clipboard instructions](https://github.com/tmux/tmux/wiki/Clipboard#terminal-support---tmux-inside-tmux).
Enabling `set-clipboard on` allows programs in the host tmux panes to write
your clipboard; it does not provide clipboard read access.

Apply the host configuration from a host shell:

```console
$ tmux source-file ~/.tmux.conf
```

Detach and reattach the host tmux client after changing terminal features,
then reattach to the guest. sandbar configures only guest tmux; it does not
modify your host configuration.

If the host also uses `C-a` as its prefix, ensure it has
`bind C-a send-prefix`. Press `C-a C-a` to send the prefix to the guest:
for example, `C-a C-a d` detaches the guest, while `C-a d` detaches the
host. With the usual host `C-b` prefix, guest `C-a` bindings work directly.

Shift+Enter and desktop notifications still depend on the terminal emulator
and both tmux layers. In Claude Code, `Ctrl+J` or `\` followed by Enter
inserts a newline if Shift+Enter is unavailable. If your terminal needs
Claude Code's `/terminal-setup`, run it on the workstation outside tmux,
where it can change the host terminal settings. Notification permissions
must likewise be enabled in the host terminal. Passthrough does not enable
nested control mode; the [`--cc` restriction](#why-you-cant-have-both-at-once)
still applies.

### What the board does while you're attached

If sandbar is itself running inside a host tmux session, `S` opens the shell in
a **new host window** and leaves the board on screen beside it, still live.
Otherwise `S` **suspends** the board for as long as you're attached, and it
returns on detach.

Either way, a build already in flight keeps building: sandbar buffers the output
of a board nobody is looking at rather than making the provisioner wait for a
screen to draw on. On detach the tile shows where that build has actually got
to — not a replay of every step it took while you were away.

### Native terminal tabs with `tmux -CC`

Some terminals can render a tmux session's windows as **native tabs** rather
than letting tmux draw them itself. You run tmux in *control mode*
(`tmux -CC`), tmux speaks a line protocol instead of painting a screen, and
the terminal turns each tmux window into a real tab. sandbar has two ways to use
that, and they are not interchangeable — one gives you a tab per **VM**, the
other a tab per **window inside one VM**.

#### Which terminals speak control mode

Nothing in sandbar looks at which terminal you are running. `--cc` starts an
ordinary `tmux -CC` client and the two recipes below are plain tmux commands,
so what you get is whatever your terminal does with the protocol. **This list
is not exhaustive** and support moves; at the time of writing:

| Terminal | Control mode |
| --- | --- |
| **iTerm2** | The reference implementation — control mode was written for it. The rest of this page describes iTerm2's behaviour. |
| **WezTerm** | Implemented, but a subset: WezTerm's own UI takes the keystrokes, so the tmux prefix does not reach tmux. Use WezTerm's new-tab rather than `C-a c`. |
| **Ghostty** | Not yet. Protocol parsing landed in 1.3.0 but is not wired to the UI, so you do not get tabs today. |
| **Alacritty, Windows Terminal, Terminal.app** | No. Each has an open feature request. |

A terminal that does not speak control mode prints the protocol into your
window as scrolling text rather than failing cleanly — sandbar cannot tell the
difference, so if that is what you see, drop `--cc` and attach normally.

#### A tab per VM, with the board still live

Start sandbar inside a control-mode tmux session:

```console
$ tmux -CC new-session -s sandbar sandbar
```

`$TMUX` is now set, so `S` takes the new-window path described above instead
of suspending — and because your terminal is rendering that tmux, the new
window arrives as a **native tab**. The board keeps running in its own tab beside
it, progress bars and all, and each `S` on a different VM opens another tab.

This is the setup to use if you spend the day in the board.

#### A tab per window inside one VM

`sandbar shell --cc NAME` attaches to the **guest's** tmux in control mode, so
the windows `C-a c` makes inside the VM become native tabs:

```console
$ sandbar shell --cc web
```

Run this from a plain terminal window, not from the board.

#### Why you can't have both at once

The two recipes stack a host tmux and a guest tmux, and only the outer one
can reach the terminal. Control mode announces itself with a DCS escape sequence,
and tmux strips DCS from its panes' output rather than forwarding it to its
client — so a guest `tmux -CC` running inside a host tmux window never
reaches the terminal at all. Turning on `allow-passthrough` does not change
this; that option forwards a specific `tmux;`-prefixed wrapper, not the
handshake tmux emits on its own.

Rather than print raw protocol into your pane, `sandbar shell --cc` refuses
when `$TMUX` is set:

```console
$ sandbar shell --cc web
sandbar shell: --cc does not work inside tmux — a tmux pane strips the control-mode
handshake before your terminal can see it; detach (C-a d) and run this from a
plain terminal window
```

So pick the layer that matters more to you. If you mostly want several VMs
open at once, take the first recipe. If you mostly want several windows
inside one VM, take the second.

!!! tip "The board will mention this once"

    The first time you press `S` outside a host tmux session, and only if
    tmux is installed on this machine, the board logs a one-line reminder of
    both commands to its Messages strip. It appears once per run of `sandbar`,
    not once per shell — quit and relaunch to see it again.

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

In the source picker, paste a path or drag a file or folder into the terminal
window to select it directly. Sandbar checks that it exists, then opens the
destination prompt. A pasted or dropped destination replaces the prefilled
path. Quoted paths, escaped spaces, and local `file://` URLs are accepted;
choose one file or folder at a time. Press **ctrl+s** to start the copy.

A drop from your desktop supplies a **local** path: use it for an upload source
or a download destination. For a download source or upload destination, paste
a path inside the guest instead. Sandbar does not translate local paths to guest
paths.

If your terminal sends drops as ordinary keystrokes rather than a bracketed
paste, press **ctrl+l** first: it opens a source path field or clears the
destination field. Type or drop the path, then press **enter** to select a
source, or **ctrl+s** to confirm the destination. **esc** cancels source path
entry and returns to browsing.

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

`v` (paste image) on a running VM's tile, and `sandbar paste-image NAME` from
the command line, both stage the host clipboard's image on the guest so you
can press Ctrl-V inside Claude Code, Codex, OpenCode, or Pi to attach it to
your message.

### The workflow

1. Copy an image on your host (screenshot, photo, graphic, etc.).
2. In the `sandbar` TUI, press `v` on the VM's tile; or from a terminal, run
   `sandbar paste-image NAME` (where `NAME` is the VM's name).
3. You'll see a status message: **"staged image on NAME — press Ctrl-V in the
   guest"**.
4. In your coding agent inside the guest, press Ctrl-V to attach the image to
   your message.

The image is held in a single-slot clipboard on the guest, persisting until
you run `sandbar paste-image` again (overwriting it with a new image).

### How it's secure

The feature is designed to prevent clipboard **text** from leaking into the
guest. sandbar reads the clipboard **image-only** on your workstation, verifying
an image type is advertised before fetching any bytes. If you have text on
your clipboard instead, the command reports "no image on clipboard" and
nothing is staged. Inside the guest, read-only command shims serve the image
to agents that probe `xclip`/`wl-paste`; a private headless X display serves
agents such as Codex that use the X11 API directly. Neither path exposes
clipboard text.

The image is read on the machine running `sandbar` (your workstation), never on
the machine the VM runs on. Only the image bytes themselves are sent across
the network.

### Known limitation

A Linux *host* clipboard holding only a non-PNG image (e.g., JPEG without a
PNG variant) is treated as "no image" in v1. macOS always coerces images to
PNG, so this edge case applies to Linux hosts only. If it causes real-world
friction, it will be revisited.

The verb is only offered on running VMs. Stop or reset a VM and the verb
disappears from its tile until the VM runs again.

## Landing

`l` on a focused tile (or `sandbar land NAME [<path>]` from the command line)
opens the **Landing pane**: a listing of that VM's git checkouts, swept live
from the guest, with each one's branch, push state, and PR state. This is
how code — as opposed to the data `u`/`g` move above — leaves the VM: not by
copying files, but by pushing a branch and opening a PR against it, exactly
as you would from your own machine.

The draft PR and browser actions use GitHub's `gh` CLI and are GitHub-only.
GitLab checkout push and fetch work through the guest's scoped
`GITLAB_TOKEN`; Landing does not create GitLab merge requests. The
drupal.org publishing action is a separate workflow described below.

The sweep finds repositories up to six directories below the guest's home
(skipping `node_modules`, `.cache`, `.cargo` and `.npm`), and then asks git
itself for each repository's linked worktrees — so a worktree is listed
however deep it sits, including outside the home directory entirely. A
repository cloned deeper than six levels below the home directory is not
found at all, and so has no row — `sandbar land NAME PATH` cannot reach one
either, since it selects from the same sweep.

Each checkout lands in one of a few states, and the pane offers the action
that state calls for:

- **Pushed, no PR** — open a one-shot **draft PR** for that checkout's
  pushed branch.
- **PR already open** — open it in a browser.
- **PR state unknown** — the branch is pushed, but sandbar could not confirm
  whether a PR exists, because host `gh` is unusable or the lookup failed.
  Opening a draft PR still works (it falls back to the compare URL).
- **Never pushed, unpushed, or dirty** — work that exists only in this VM:
  a branch you have never pushed, commits absent from all remote-tracking
  branches in the guest, uncommitted changes, or a combination. Acting on this row
  **commits and pushes it**: sandbar drops you into the guest with your editor
  open on `git commit -a`, and pushes the branch when you save (setting its
  upstream if it has none). Quit the editor without saving and nothing is
  committed or pushed.
- **Nothing to land** — the checkout is on its repo's default branch with
  nothing of its own on top. Every fresh clone starts here.
- **Local-only** — the checkout has no remote configured, so there is
  nowhere for Landing to push. This is the only state with nothing to offer.
  If such a checkout holds uncommitted or unpushed work, the row still says
  so (`local only · 2 uncommitted`) — there is nothing sandbar can do about it,
  but you should know it is there before deleting the VM.
- **On drupal.org** — the checkout's remote is a drupal.org project rather
  than GitHub. Either of drupal.org's two git hosts counts:
  `git.drupalcode.org` (HTTPS) or `git.drupal.org` (SSH, the host the
  "Show commands" panel on an issue fork hands you). Instead of a PR flow,
  the row reads
  `publish to drupal.org` when a workstation drupal.org PAT is on file:
  acting on it prompts for the drupal.org issue number this work belongs
  to, then resolves the destination and asks you to confirm before
  publishing anything. Without a PAT on file the row instead reads
  `on drupal.org · no PAT on file, publish disabled`
  and offers no action. See
  [Publishing to drupal.org](drupalorg-publishing.md), and
  [Setup](drupalorg-publishing.md#setup) for the token file.

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
  `sandbar` from.
- **`gh: 1Password did not authorize`** — you use the 1Password `gh` shell
  plugin (below) and the vault did not hand over the token. Unlock 1Password
  and reopen the pane.

### 1Password shell plugin

The [1Password `gh` shell plugin](https://developer.1password.com/docs/cli/shell-plugins/github/)
is supported directly — no configuration needed. If you have it set up, sandbar
runs `op plugin run -- gh …` instead of bare `gh`, so your token comes from
the vault exactly as it does at your own prompt.

sandbar detects it by reading `~/.config/op/plugins.sh` (the file `op plugin init`
generates) and checking for `op` on your `PATH`. Detection is **file-only** —
sandbar never runs `op` to find out, precisely because that could pop an
authorization prompt underneath the full-screen UI.

Two things worth knowing:

- **`GH_TOKEN` wins.** If `GH_TOKEN` or `GITHUB_TOKEN` is set in sandbar's
  environment, sandbar uses plain `gh` and ignores the plugin entirely. That is
  the escape hatch if you would rather sandbar not touch `op`.
- **1Password may need to authorize.** The first Landing action in a while can
  require unlocking 1Password. sandbar gives the `op` process no terminal, so a
  prompt can never corrupt the display — but it does mean an authorization
  that can only be answered in the terminal will time out. Unlock 1Password
  first and reopen the pane.

Why any of this is needed: sandbar runs `gh` **directly, never through a shell**,
because the branch names and repo slugs it passes come from inside the VM and
must not be able to reach a shell interpreter. The plugin's usual `gh` alias
is therefore invisible to sandbar — but `op plugin run -- gh …` is a plain
command, not a shell trick, so supporting it costs nothing in safety.

The token needs **`Pull requests: write`** (fine-grained), or `repo` /
`public_repo` (classic) — sandbar resolves the base branch and the head commit's
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

For `sandbar land`'s full CLI flags (`--pr`, `--web`), see the
[CLI Reference](cli-reference.md#sandbar-land-name).
