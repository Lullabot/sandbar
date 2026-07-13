# sand — interactive TUI

`sand` is a [Bubble Tea](https://github.com/charmbracelet/bubbletea) terminal
UI for managing this project's disposable Claude Code [Lima](https://lima-vm.io/)
VMs, with full CRUD: list and inspect instances, create new ones, run lifecycle
actions (start / stop / restart), edit their secrets, and delete or reset them.

It provisions the heavy, identity-free install once into a stopped **base
image**, then makes each VM a fast `limactl clone` of that base, and runs a
light **finalize** pass on the clone (hostname, git identity, `apt upgrade`,
optional repo clone). Creating and recreating a VM streams the provisioner's
output into a scrollable progress pane.

`sand` ships in two modes from the same binary:

- **`sand`** (no subcommand) opens the interactive Bubble Tea TUI documented
  below.
- **`sand create`** drives the same provisioner headlessly — no TUI, no
  prompts — for scripting and CI. See
  [Headless mode (`sand create`)](#headless-mode-sand-create).

## Prerequisites

- **Lima** (`limactl`) on your `PATH`, new enough to support `limactl clone`.
  Both the TUI and `sand create` run a preflight check on startup and exit with
  an install link if it's missing or too old. (See
  [Lima installation](https://lima-vm.io/docs/installation/).)
- You do **not** need Ansible on your host, and you do **not** need a checkout
  of this repository. The base image's Lima config installs `ansible` (and
  `rsync`) inside the guest, and the playbook is then run in the VM with
  `--connection=local`. See
  [Playbook resolution](#playbook-resolution-working-tree-first-embedded-fallback)
  for where the playbook itself comes from.

## Build & run

Installed via Homebrew (`brew install lullabot/sandbar/sand`), just run `sand`
or `sand create`. To build from a checkout of this repository instead:

```bash
go build ./cmd/sand
./sand
```

## Playbook resolution: working-tree-first, embedded-fallback

The Ansible playbook (`site.yml`, `roles/`, `group_vars/`) is embedded into the
`sand` binary at build time (see `playbook_embed.go`), so a Homebrew-installed
`sand` needs no separate checkout to provision a VM. At startup `sand` resolves
which copy of the playbook to mount into the VM in two tiers:

1. **Working tree first.** If `sand` is run from inside a git checkout of this
   repository (detected by walking up from the current directory to a toplevel
   containing `site.yml`), it mounts that working tree. This is the mode for
   hacking on the playbook: uncommitted edits take effect on the next
   provision, with no rebuild required.
2. **Embedded fallback.** Otherwise (e.g. a Homebrew-installed binary run
   outside any checkout), `sand` extracts the playbook fileset embedded in
   the binary to a fresh private temp dir and mounts that instead.

## Headless mode (`sand create`)

`sand create` drives the exact same base-image / clone / finalize provisioner
as the TUI, with no prompts — useful for scripting and CI. Flags mirror what
the TUI's create form asks for:

```bash
sand create --name claude --git-name "Your Name" --git-email you@example.com
```

Common flags (run `sand create --help` for the full list and defaults):

| Flag | Purpose |
|------|---------|
| `--name` | Lima instance name (default `claude`) |
| `--git-name` / `--git-email` | git identity written into the VM (required) |
| `--cpus` / `--memory` / `--disk` | VM sizing (defaults mirror the TUI form) |
| `--clone-url` / `--clone-token` | Optional private repo to clone into the VM |
| `--recreate` | If the named instance exists and is sand-managed, delete and re-clone it |
| `--rebuild` | Delete and rebuild the base image first, then create |

`--ref` (pinning a playbook git ref) is deliberately not a flag: with the
playbook embedded in the binary, there is no separate ref left to pin — see
[Playbook resolution](#playbook-resolution-working-tree-first-embedded-fallback).

## Shells (`sand shell`)

`sand shell` attaches a shell to a running VM's **persistent tmux session**, from
any terminal:

```bash
sand shell claude
```

**Closing the terminal does not kill your work.** That is the headline. The
session and everything in it — a Claude Code job, a build, a long script — keeps
running in the guest when you detach, close the terminal, or shut the laptop.
Run `sand shell` again and it is all still there. (The VM itself must stay
running: a detached session survives a closed terminal, not a stopped VM.)

You are in tmux, so the prefix key is `C-a`:

| Key | Action |
|-----|--------|
| `C-a c` | Open a new window |
| `C-a \|` | Split into two panes, side by side |
| `C-a S` | Split into two panes, one above the other |
| `C-a d` | Detach (leaves everything running) |

**`C-a` is the tmux prefix now, so it no longer jumps the cursor to the start of
the line** the way readline binds it. That is the one thing worth knowing before
it surprises you; press `C-a C-a` to send a literal `C-a` through to the shell.

Run `sand shell` in a **second** terminal and you get the same set of windows but
your **own** current window, so two terminals can sit on two different windows of
the same VM — neither follows the other's window switches, and neither is
squeezed to the size of the smaller terminal. Detaching that second terminal
cleans up after itself; the first session (`main`) is the one that holds your
work and is never torn down automatically.

This is the supported way to get more than one shell into a VM. Reaching for
`limactl shell <name>` directly gets you a bare, throwaway shell that dies with
its terminal, and bypasses the session everything else attaches to.

## The board

`sand`'s home surface is a **board**: a scrolling grid of tiles, one per VM,
with a focus ring you move with the arrow keys. There is no table and no list
view — the board replaced them, and there is no key that brings a table back.

The board shows **sand-managed clones only, always** — no unmanaged Lima
instance, no base image, and **no toggle** to bring either back (the old `f`
filter is gone). That has a real, deliberate cost: a base image or someone
else's Lima VM becomes invisible and unmanageable from the TUI — manage those
with `limactl` directly. (The header used to carry a "1 base, 2 external hidden"
count for exactly this reason; it was dropped in favour of the live host
readout, so the board no longer tells you anything is out of view.) A VM mid-build, and a VM
whose last build **failed**, still get a tile even before (or without ever)
being recorded managed — otherwise a build in progress, or a failed one,
would have nowhere to report on.

**Provisioning no longer takes over the screen.** Creating or resetting a VM
opens a progress view, but `esc`/`enter` return you to the board immediately
and the build keeps running in the background — its tile shows a live
progress bar. You can create a second VM, or act on an unrelated one, while
the first is still building. `q` only quits outright when nothing is in
flight; with a build or transfer running, it asks you to confirm abandoning
it first. `ctrl+c` remains the unconditional cancel for whichever run you're
currently looking at.

Tile order is **alphabetical and stable** — a VM changing state never moves
its tile — and focus tracks the VM's **identity**, not its slot, so a refresh
or a state change landing mid-keypress can never shift a destructive key onto
the wrong VM.

The bindings below come from the single command registry
(`internal/ui/commandreg.go`) and `internal/ui/keys.go`; the help bar at the
bottom of the screen always shows exactly the verbs that apply to the tile
under the ring, and can never advertise one that would do nothing.

### Board keys

| Key | Action |
|-----|--------|
| `↑` `↓` `←` `→` | Move the focus ring (arrows only — no vim keys; `l` and `g` are per-VM verbs below) |
| `enter` | Open the focused VM's screen — or, with the ring on the empty slot, open the create-VM form |
| `n` | Open the create-VM form |
| `/` | Incremental name search — type to filter the tiles by name; `esc` clears and exits, `enter` keeps the filter |
| `X` | Stop all — every sand-managed VM currently running (see below) |
| `?` | **Keys**: every command with a one-sentence description, including the ones that don't apply to the focused VM right now. `↑`/`↓` scrolls, `esc` closes |
| `q` | Quit (confirms first if a build or transfer is in flight) |

Per-VM verbs — `s`/`x`/`r`/`R`/`S`/`d`/`u`/`g`/`e`/`l` — act on the **focused
tile** directly from the board;
`enter` is not required first. Each is shown in the footer, and fires, only
when it applies to that VM (see [Per-VM verbs](#per-vm-verbs) for what each one
does and when it's offered).

### Stop all (`X`)

`X` stops every VM that is sand-managed **and** currently running, after a
confirmation naming them. Unmanaged Lima instances and base images are never
touched, so an instance you run for unrelated work is safe. VMs hidden by an
active `/` search are still stopped — `X` means "stop every managed VM", not
"stop what the current filter happens to show". Stopping is sequential; if
one VM refuses to stop, the others still stop and the failure is reported by
name.

### Per-VM verbs

Every verb below fires on the **focused tile**, straight from the board. There is
no VM screen to open first — the tile shows everything one would have (its state,
its live cpu/memory, its disk, its allocated cores on the cpu gauge's own label),
so it was removed. `enter` on a VM tile does nothing; on the empty slot it creates
a VM.

The footer offers a verb **only when it applies** to the focused VM, and the key
does nothing when it is not offered:

| Key | Action |
|-----|--------|
| `s` | Start the VM (offered when it isn't already running) |
| `x` | Stop the VM (offered while it's running) |
| `r` | Restart the VM (always offered — stop-then-start, whatever the current state) |
| `R` | **Reset**: open the pre-filled form to re-clone the VM from its base image (managed VMs only) — see [Reset a VM](#reset-a-vm) |
| `S` | Attach a shell to the guest's persistent tmux session (offered while it's running) — see [Shells](#shells-sand-shell) |
| `d` | **Delete** the VM (opens a confirmation; withheld while the VM is mid-build) |
| `u` | **Upload** a host file/directory into the VM (offered while it's running) — see [Moving files in and out](#moving-files-in-and-out) |
| `g` | **Download** a guest file/directory to the host (offered while it's running) |
| `e` | Edit the VM's **secrets** — offered whether the VM is running or stopped; saving while it's running applies the change to the live guest immediately (not just "on next start") |
| `l` | Reopen the VM's last build or transfer **log** — offered only when a run (finished or still in flight) is retained to show |
| `esc` / `backspace` / `enter` | Back to the board (lands back on the same tile) |

Pressing `S` attaches you to the VM's persistent tmux session — the same one
`sand shell` reaches, so **closing the terminal detaches rather than killing what
you were running**, and you can pick it up again from any terminal. The tmux
prefix is `C-a`; see [Shells](#shells-sand-shell) for the keys that matter.

Normally sand suspends itself while you are in the shell and resumes when you
detach (`C-a d`) or exit. The exception: if you launched the TUI from **inside a
host tmux session**, `S` opens the shell in a new *host* window instead and does
not suspend at all — so the board stays on screen beside it, still streaming any
builds in flight.

`d` is bound to delete on every screen: it is the most destructive key, so its
meaning never changes under your fingers. Download took the rename to `g` (get).

### Delete confirmation

Pressing `d` opens an inline prompt: `Delete "claude"?  [y] yes   [n] cancel`.
Only `y` confirms — a second press of `d` does nothing, so an accidental
double-tap cannot destroy a VM. Reset is a separate action (`R`), not a branch of
the delete prompt.

### Secrets editor

Pressing `e` opens a `KEY=VALUE` editor for the VM's secrets, seeded from the
host store. It does **not** require the VM to be running — that is the point:
secrets live on the host and are written into the guest on the VM's next start.

Secrets are optionally grouped into **scopes** — a scope is a safe
home-relative directory path (e.g. `github.com/acme`); the empty scope is
**global**. The buffer renders the global scope first, headerless, then each
directory scope under its own `[scope]` header:

```
EDITOR=vim

[github.com/acme]
GH_TOKEN=ghp_your_token_here
```

- One `KEY=VALUE` per line within a section. Blank lines and `#` comments are ignored.
- A line splits on its **first** `=`, so a value may contain `=`.
- A `[scope]` header line starts a new section; `[]` (or the region before any header) is the global scope.
- Keys must be valid environment variable names (`[A-Za-z_][A-Za-z0-9_]*`). A bad
  key, a bad scope, or a duplicate key within a scope aborts the save naming the
  offending line — nothing is written until the whole buffer is valid.
- `ctrl+s` saves. On a **running** VM the change is pushed into the live guest
  immediately (the status line says so, and reports any failure); on a **stopped**
  one it applies on its next start. `esc` discards.

Values are shown in cleartext. See [Where secrets live](README.md#where-secrets-live)
for the storage and trust model — they are stored **unencrypted** on the host.

**Where scopes land in the guest:** the global scope renders into
`~/.config/sandbar/secrets.env`, sourced by both login and interactive shells;
a scope `foo/bar` renders into `~/foo/bar/.env`, auto-loaded by direnv. Naming
a token `GH_TOKEN` and scoping it (e.g. `[github.com/acme]`) is a
**convention**, not a storage feature: `sand` recognizes a small,
delivery-layer table of token names and, for a non-empty scope, additionally
wires `git`/`gh` credentials for that subtree so they authenticate under
`~/<scope>/`. The secrets store itself knows nothing about GitHub — it only
ever holds `(scope, KEY, VALUE)` triples.

### File browser (upload/download source)

One `bubbles/list` browser is used for **both** the host and guest sides. It
opens when you press `u`/`d` on the focused tile.

| Key | Action |
|-----|--------|
| `↑` / `↓` (also `k` / `j`) | Move the selection |
| `enter` | Enter the highlighted directory (or, on a file, select it) |
| `ctrl+s` | **Select** the highlighted entry — a directory is copied recursively |
| `/` | Fuzzy-filter the current directory |
| `esc` | Back to the board |

`..` navigates to the parent directory. Enter (navigate into) and `ctrl+s`
(select for copy) are deliberately distinct, so choosing a directory as a
recursive-copy source never collides with entering it.

### Destination prompt

| Key | Action |
|-----|--------|
| typing / paste | Edit the destination **directory** (the selection is placed inside it) |
| `ctrl+s` | Confirm and start the transfer (switches to the progress view) |
| `esc` | Back to the browser |

The prompt is pre-filled with a sensible default (the guest project checkout for
uploads, the host working directory for downloads) and accepts typed, pasted, or
**drag-and-dropped** paths — a dropped path is un-escaped automatically
(backslash-escaped spaces, surrounding quotes, and an optional `file://` prefix
are stripped).

### Create form

| Key | Action |
|-----|--------|
| `↑` / `↓` (also `tab` / `shift+tab`) | Move between fields |
| `enter` | Move to the next field (it does **not** create) |
| `ctrl+s` | Create the VM (validates, then switches to the progress view) |
| `esc` | Cancel, back to the board |

Typing edits the focused field and `backspace` deletes a character; `q` is a
literal character here, so only `ctrl+c` quits. Help for the focused field — its
default, whether it is required, and (for the token) where to create a GitHub
fine-grained token with the recommended scopes — is shown beneath the form.

Most fields default the same way `sand create`'s flags do when left blank
(hostname → the instance
name, user → your host username, CPUs → half the host's cores, memory → `8GiB`
**or half your host's RAM, whichever is less**, disk → `100GiB`). **`Name` is
required** and starts empty — it does not silently default. `GitHub repo URL` and
`GitHub token` are optional and used only to clone a private repo into the VM.

If the requested **disk** is larger than the free space on the volume backing
Lima's instance store (`$LIMA_HOME`, else `~/.lima`), the form shows a
non-blocking warning: qcow2 disks are sparse, so the VM still builds, but it may
fail to grow to its full size once the volume fills.

### Progress / streaming view

| Key | Action |
|-----|--------|
| `↑` / `↓`, `pgup` / `pgdn` | Scroll the provisioner output |
| `ctrl+c` (while running) | Cancel **this** build — kills the underlying `limactl` and returns a *Canceled* result (may leave a partial VM) |
| `esc` / `backspace`, `enter` | Back to the board — **immediately**, even while the run is still going. The run keeps going in the background; its tile shows a live progress bar |


The slow lifecycle steps — building the base image, cloning, and booting — stream
their `limactl` output live (with `==>` phase banners), so a first-ever creation
(which builds the base image before your VM is cloned) shows continuous progress
instead of a silent spinner.

Leaving this screen no longer abandons the run: `esc` was the "cancel and go
back" key on the old list-blocking TUI, and it is not any more. Only `ctrl+c`
cancels a run; `esc`/`enter` just stop watching it. Reopen it any time with
`l` on the VM's tile.

## Moving files in and out

Press `u` (**Upload**, host → guest) or `g` (**Download**, guest → host) on the
focused tile. Both require the VM to be **Running** (the copy rides Lima's SSH
transport); on a stopped VM neither verb is offered and the key does nothing.

Each transfer is a short, sequential wizard:

1. **Pick a source** in the file browser — one `bubbles/list` widget used for
   both host and guest, with a built-in fuzzy filter (`/`). `enter` navigates
   into directories; `ctrl+s` selects the highlighted file **or** directory (a
   directory is copied recursively).
2. **Confirm a destination directory** in a prompt pre-filled with a sensible
   default. The destination is always a **directory** and the selected item is
   placed inside it, so the result is identical whether Lima's `rsync` or `scp`
   backend runs. Typed, pasted, and drag-dropped paths are accepted.
3. **Watch progress stream** in the same progress pane as provisioning;
   `ctrl+c` cancels.

This is the in-posture replacement for the old manual `limactl copy`: every
transfer is a discrete, user-initiated copy, so **nothing leaves the VM** by
default — there is no writable host mount, no standing share, no new network or
credential, and `limactl delete` still removes everything.

For v1 a transfer moves **one file or one directory at a time** in a single
direction; a dual-pane layout, multi-select, and overwrite prompts are
deliberately deferred.

## Reset a VM

Pressing `R` on the focused tile (managed VMs only) opens the create form
**pre-filled** with the VM's recorded settings, titled *Reset VM*. The `Name` is
locked to the VM being reset; every other field is editable, so a reset doubles
as the way to change a VM's CPUs, memory, disk, hostname, git identity, or clone
URL. Submit with `ctrl+s` to delete the VM and re-clone it from the base with the
edited settings (the new settings are then recorded, so the next reset defaults
to them).

Up to two **preserve toggles** follow the fields (space/enter flips the focused
one; both default off):

- **Preserve Claude Code settings** — keeps `~/.claude` and `~/.claude.json`
  (your Claude login and history) across the reset.
- **Preserve ~/&lt;host&gt;/&lt;org&gt;** — named for the exact directory it
  protects, e.g. `Preserve ~/github.com/lullabot`. It keeps the per-org directory
  (the cloned repo and its `.env`). When enabled it **skips the re-clone**, so you
  do **not** need to re-supply a token to reset a VM that had cloned a private
  repo. Otherwise the clone token must be re-supplied on reset — see
  [GitHub Authentication](README.md#github-authentication) for the full token
  lifecycle. **This toggle is hidden entirely** when the VM cloned no repo, since
  there would be nothing to preserve.

Enabling either toggle copies that data out of the VM to a private temp dir on
your host and restores it after the reset. The form warns that this moves your
Claude login and `.env` token off the VM: **do not preserve if you suspect the VM
is compromised.** See the main [Security Model](README.md#security-model).

**Disk sizing.** Each tile's `disk` gauge shows real allocated blocks against
the VM's maximum virtual size — allocated blocks over maximum size. `Disk Used`
(the left figure) sits well below
`Max Disk` because qcow2 disks are sparse — only written blocks are allocated. A
VM's `Max Disk` can **grow** from the base floor (`20GiB`) but cannot **shrink**
below the current base's virtual size — qcow2 cannot shrink a live disk — so the
form enforces a minimum of the floor. A base built before per-VM sizing keeps its
old (larger) size and clones can't go under it; delete `claude-base` to rebuild
the base at the floor on the next create/reset.

## Managed VMs and safety

`limactl` knows about **every** Lima instance on your machine, not just the
ones this tool created — your `default` template VM, a Colima docker VM, and
so on. The board does **not** show them: it is sand-managed clones only,
always, with no toggle to widen it (see [The board](#the-board)). Manage an
unmanaged instance, or a base image, with `limactl` directly — `sand` deliberately
gives them no tile.

**Reset is different**: it deletes the instance and re-clones it from a Claude
base image, so pointing it at an unrelated VM would replace that VM with a sandbox.
To prevent this, `sand` records the instances **it** creates and:

- shows them (and only them, plus one currently mid-build or whose last build
  failed) on the board,
- offers **reset (`R`) only for managed VMs**, and
- restricts **stop all (`X`)** to running managed VMs, so it never stops an
  unrelated Lima instance.

Base images (the heavy, identity-free images each VM is cloned from, such as
`claude-base`) are never on the board — they are a clone source, not a
workspace — and are managed via `limactl` (e.g. `limactl start claude-base`,
`limactl delete claude-base`) rather than through `sand`.

The index of managed VMs is a small JSON file at
`${XDG_DATA_HOME:-$HOME/.local/share}/sandbar/managed-vms.json`. It is updated
when you create, reset, or delete a VM from the TUI or `sand create`, and
reconciled against `limactl list` on each refresh so an instance deleted
outside `sand` stops being flagged managed. VMs created outside `sand` (e.g.
directly via `limactl`) are treated as unmanaged; delete still works, but reset
and stop-all skip them.

The index also stores each managed VM's create configuration (CPUs, memory, disk,
hostname, git identity, …) so **reset pre-fills its form faithfully** instead of
resetting it to defaults (see [Reset a VM](#reset-a-vm)). The index carries a
schema `version`; an older unversioned file is migrated on load.

The **clone token is never written to this index** — `managed-vms.json` stays
secret-free. Secrets live in a *separate* file, `secrets.json`, stored
**unencrypted** at mode `0600`; see
[Where secrets live](README.md#where-secrets-live). A token supplied on the
create form is recorded there as `GH_TOKEN` in the VM's global scope, so it
survives in the VM's environment across restarts and can be edited (or
rescoped, e.g. under `[github.com/acme]`) at any time with `e`.

Note the one thing the store does **not** do: a reset re-clones the repo during
its finalize pass, which runs *before* the stored secrets are written into the
guest. So resetting a VM that cloned a **private** repo still requires
re-supplying the clone token on the reset form — unless you enable the
*Preserve ~/&lt;host&gt;/&lt;org&gt;* toggle, which skips the re-clone entirely.

Creating a VM whose name already exists is refused with a clear message rather than
colliding — delete it, or reset it.

## Why the `limactl` CLI (not a Go API)

Lima is written in Go, but it does **not** publish a stable public Go API:
its `pkg/…` packages are internal, change between releases, and importing them
would pull Lima's whole dependency tree in and pin us to a single Lima version.
The supported, documented integration surface is the `limactl` CLI with
structured output — `--format json` for `list` and `--format '{{ .Field }}'`
templates for single values — which is what this tool uses (see
[`internal/lima`](internal/lima)).

Because `limactl` logs to **stderr** (logrus `time=… level=… msg=…` lines) and
writes its JSON/template output to **stdout**, the runner captures the two
streams separately: only stdout is parsed, and stderr is surfaced as diagnostics
on failure. The list parser also skips any line that is not a JSON object, so a
stray notice on stdout degrades to "ignored" rather than failing the listing.

## Relationship to the original bash provisioner

`sand` (both the TUI and `sand create`) is a from-scratch Go port that replaced
this project's original bash provisioner script. It keeps the same model and
security posture:

- **Base / clone / finalize**: provision the heavy work once into a stopped base
  image, clone each VM from it, then run a light finalize pass. The split is
  driven by the `provision_phase` variable (`base` / `finalize` / `full`).
- **Ephemeral VMs**: the only mount is the playbook, **read-only** — there is no
  writable host mount, so `limactl delete <name>` provably removes everything a VM
  produced. Move files in or out with the TUI's **Upload**/**Download** actions
  (see [Moving files in and out](#moving-files-in-and-out)) — discrete,
  user-initiated copies, so no writable share or standing network is introduced.
- **Secrets in tmpfs**: per-phase Ansible vars (which may carry a clone token) are
  streamed into the guest's tmpfs and removed on exit; they never land in argv or
  on the persistent disk.

The bash script has been retired: `brew install lullabot/sandbar/sand` (or
`go build ./cmd/sand` from a checkout) is now the sole install path, and both
`sand create` (scripting/CI) and the interactive TUI go through the same Go
provisioner described above — see
[Playbook resolution](#playbook-resolution-working-tree-first-embedded-fallback)
and [Headless mode](#headless-mode-sand-create).
