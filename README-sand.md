# sand — interactive TUI

`sand` is a [Bubble Tea](https://github.com/charmbracelet/bubbletea) terminal
UI for managing this project's disposable Claude Code [Lima](https://lima-vm.io/)
VMs, with full CRUD: list and inspect instances, create new ones, run lifecycle
actions (start / stop / restart), and delete or recreate them.

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
   outside any checkout), `sand` materializes the playbook fileset embedded in
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

## Keybindings

The bindings below come from `internal/ui/keys.go` and the per-view handlers
(`list.go`, `detail.go`, `form.go`, `progress.go`). `ctrl+c` quits from anywhere,
except while a build is running on the progress view, where it cancels the build
instead. The active view's keys are also shown in the help bar at the bottom of
the screen.

### List view

| Key | Action |
|-----|--------|
| `↑` / `↓` (also `k` / `j`) | Move the selection |
| `enter` | Open the selected VM's detail view |
| `S` | Open an interactive shell in the selected VM (must be running) |
| `n` | Open the create-VM form |
| `s` | Start the selected VM |
| `x` | Stop the selected VM |
| `r` | Restart the selected VM |
| `d` | Delete the selected VM (opens a confirmation) |
| `f` | Toggle the filter: show all VMs ↔ only sand instances (managed + base) |
| `/` | Incremental name search — type to filter the list by name; `esc` clears and exits, `enter` keeps the filter |
| `q` | Quit |

Pressing `S` suspends the TUI and hands your terminal to `limactl shell <name>`;
the TUI resumes when you exit the shell.

The **Managed** column marks which VMs `sand` created: `yes` for a managed
clone, `base` for a base image other VMs are cloned from (e.g. `claude-base`), and
`no` otherwise. See [Managed VMs and safety](#managed-vms-and-safety) below.

### Delete / recreate confirmation (on the list)

Pressing `d` on the list opens an inline prompt. The `[r] recreate` option appears
**only for managed VMs**, and names the base it would clone from, e.g.
`Delete "claude"?  [y] yes   [r] recreate from claude-base   [n] cancel`.

| Key | Action |
|-----|--------|
| `y` (also `d`) | Confirm: delete the VM |
| `r` | Recreate: open the pre-filled reset form to re-clone the VM from its base image (managed VMs only) — see [Reset a VM](#reset-a-vm) |
| `n` (also `esc`) | Cancel |

### Detail view

| Key | Action |
|-----|--------|
| `u` | **Upload** a host file/directory into the VM (must be running) — see [Moving files in and out](#moving-files-in-and-out) |
| `d` | **Download** a guest file/directory to the host (must be running) |
| `s` | Open the **secrets panel** (masked list) — see [Secrets](#secrets) |
| `esc` / `backspace` | Back to the list |
| `enter` | Back to the list |
| `q` | Quit |

### Secrets panel

Opened with `s` from the detail view.

| Key | Action |
|-----|--------|
| `a` | Add/edit a secret (VM-global, directory-scoped, or GitHub token) |
| `r` | Refresh a GitHub token — persists the new value to the host store and applies it live to the VM |
| `esc` / `backspace` | Back to the detail view |

See [Secrets](#secrets) for the full model.

### File browser (upload/download source)

One `bubbles/list` browser is used for **both** the host and guest sides. It
opens when you press `u`/`d` on the detail view.

| Key | Action |
|-----|--------|
| `↑` / `↓` (also `k` / `j`) | Move the selection |
| `enter` | Enter the highlighted directory (or, on a file, select it) |
| `ctrl+s` | **Select** the highlighted entry — a directory is copied recursively |
| `/` | Fuzzy-filter the current directory |
| `esc` | Back to the detail view |

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
| `esc` | Cancel, back to the list |

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
| `ctrl+c` (while running) | Cancel the build — kills the underlying `limactl` and returns a *Canceled* result (may leave a partial VM) |
| `q` | Quit (only after the run finishes; inert while a build is running) |
| `esc` / `backspace`, `enter` | Return to the list (after the run finishes) |

The slow lifecycle steps — building the base image, cloning, and booting — stream
their `limactl` output live (with `==>` phase banners), so a first-ever creation
(which builds the base image before your VM is cloned) shows continuous progress
instead of a silent spinner.

## Moving files in and out

Open a VM with `enter`, then press `u` (**Upload**, host → guest) or `d`
(**Download**, guest → host) on the detail view. Both require the VM to be
**Running** (the copy rides Lima's SSH transport); on a stopped VM the action
explains why and does nothing.

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

Pressing `r` on the delete/recreate confirmation (managed VMs only) opens the
create form **pre-filled** with the VM's recorded settings, titled *Reset VM*.
The `Name` is locked to the VM being reset; every other field is editable, so a
reset doubles as the way to change a VM's CPUs, memory, disk, hostname, git
identity, or clone URL. Submit with `ctrl+s` to delete the VM and re-clone it
from the base with the edited settings (the new settings are then recorded, so
the next reset defaults to them).

Two **preserve toggles** follow the fields (space/enter flips the focused one;
both default off):

- **Preserve Claude Code settings** — keeps `~/.claude` and `~/.claude.json`
  (your Claude login and history) across the recreate.
- **Preserve project .env + checkout** — keeps the cloned repo directory
  (and any legacy per-project `.env`) across the recreate, skipping the
  re-clone. It is disabled when the VM cloned no repo. Secrets, including
  GitHub tokens, are **not** affected by this toggle — they live in the host
  secrets store, independent of the VM, and are re-rendered into the VM on
  every create *and* reset regardless of whether this toggle is on. See
  [Secrets](#secrets) and [GitHub
  Authentication](README.md#github-authentication).

Enabling the "Preserve Claude Code settings" toggle copies that data out of
the VM to a private temp dir on your host and restores it after the
recreate. The form warns that this moves your Claude login off the VM: **do
not preserve if you suspect the VM is compromised.** See the main [Security
Model](README.md#security-model).

**Disk sizing.** The list shows two disk columns — `Max Disk` (the VM's maximum
virtual size) and `Disk Used` (its real allocated blocks); the detail view names
them `Maximum Disk Size` and `Disk Used (allocated)`. `Disk Used` sits well below
`Max Disk` because qcow2 disks are sparse — only written blocks are allocated. A
VM's `Max Disk` can **grow** from the base floor (`20GiB`) but cannot **shrink**
below the current base's virtual size — qcow2 cannot shrink a live disk — so the
form enforces a minimum of the floor. A base built before per-VM sizing keeps its
old (larger) size and clones can't go under it; delete `claude-base` to rebuild
the base at the floor on the next create/reset.

## Secrets

`sand` manages per-VM secrets on the **host**, then renders them into the VM.
The host store is the single source of truth: it is re-rendered into the VM
on every `sand create` **and** every reset, so recreating a VM never requires
you to regenerate secrets by hand. This resolves GitHub issue #3 (rotating a
GitHub token without a VM rebuild).

**Store location.** `${XDG_DATA_HOME:-~/.local/share}/sandbar/secrets/<vm>.json`,
one file per VM, mode `0600`.

**Two scopes:**

- **VM-global** environment variables — available everywhere in the VM.
- **Directory-scoped** secrets — environment variables (or GitHub tokens)
  that only apply under a given repo/subtree, e.g. per-org checkouts.

**GitHub auth is file-backed, not an env var.** A GitHub token is rendered as
a dedicated git credential store, selected per-directory via git's
`includeIf "gitdir:~/<scope>/"`. This means:

- **Multiple GitHub tokens can coexist in one VM** — `git`/`gh` auto-select
  the right token based on the repo directory you're in. This is the
  intended way to work across orgs/clients in a single VM (including
  porting code between repos that need different tokens), rather than
  spinning up a separate VM per context.
- **Rotating a token takes effect on the next `git`/`gh` call** — no new
  shell, no VM restart.

**Generic environment-variable secrets** (global or directory-scoped) behave
like normal shell env vars: **a new shell is required** to pick up a changed
value — already-running processes and shells keep the old value.

**direnv** is still used for directory-scoped generic env vars, but it is
fully **managed by `sand`** — `sand` runs `direnv allow` for you when it
renders a directory's secrets. You never need to run `direnv allow`
yourself.

### CLI

All subcommands require `--vm <name>`. Secret **values are never passed as a
CLI argument** — `set` always reads the value from stdin (or prompts on
stderr if stdin is a terminal), so values never appear in `ps` output or
shell history.

```
printf 'ghp_xxx\n' | sand secret set TOKEN --vm dev --github
printf 'ghp_yyy\n' | sand secret set TOKEN --vm dev --github --dir github.com/acme
printf 'v\n'       | sand secret set VAR   --vm dev --dir some/dir
sand secret list --vm dev
sand secret list --vm dev --reveal
sand secret rm VAR --vm dev --dir some/dir
sand secret sync --vm dev
```

- `sand secret set <NAME> --vm <name> [--dir <relpath>] [--github]` — routing:
  no `--dir` → VM-global env var; `--dir` without `--github` →
  directory-scoped env var; `--github` → GitHub token (scope = `--dir`;
  an empty `--dir` sets the VM's default token).
- `sand secret list --vm <name> [--reveal]` — masked by default; `--reveal`
  prints cleartext values.
- `sand secret rm <NAME> --vm <name> [--dir <relpath>] [--github]` — same
  category routing as `set`.
- `sand secret sync --vm <name>` — re-renders the host store's current
  secrets into an **already-running** VM (git/GitHub credentials apply
  immediately; env vars still need a new shell). Does not restart the VM.

### TUI

From the VM detail view, press `s` to open the **secrets panel** (a masked
list of the VM's stored secrets). From the panel, press `a` to add or edit a
secret, or `r` to refresh a GitHub token — the refresh both persists the new
value to the host store and applies it live to the running VM. See
[Secrets panel](#secrets-panel) in Keybindings.

`sand create --clone-url <url> --clone-token <T>` still works exactly as
before from the caller's perspective: the token is now recorded as a
github-scoped secret in the host store instead of being written to a
per-org `.env` loaded by direnv.

## Managed VMs and safety

`limactl` lists **every** Lima instance on your machine, not just the ones this
tool created — your `default` template VM, a Colima docker VM, and so on all show
up in the list. You can safely list, inspect, start, stop, restart, and (with a
confirmation) delete any of them; those are ordinary `limactl` operations.

**Recreate is different**: it deletes the instance and re-clones it from a Claude
base image, so pointing it at an unrelated VM would replace that VM with a sandbox.
To prevent this, `sand` records the instances **it** creates and:

- marks them in the **Managed** column (and the detail view), and
- offers **recreate only for managed VMs** (`f` filters the list down to
  sand's own instances — managed clones and base images).

Base images (the heavy, identity-free images each VM is cloned from, such as
`claude-base`) are shown as `base` in the **Managed** column and labelled "base
image (clone source)" in the detail view, so they stand out from the disposable
VMs cloned from them.

The index of managed VMs is a small JSON file at
`${XDG_DATA_HOME:-$HOME/.local/share}/sandbar/managed-vms.json`. It is updated
when you create, recreate, or delete a VM from the TUI or `sand create`, and
reconciled against `limactl list` on each refresh so an instance deleted
outside `sand` stops being flagged managed. VMs created outside `sand` (e.g.
directly via `limactl`) are treated as unmanaged; delete still works, recreate
does not.

The index also stores each managed VM's create configuration (CPUs, memory, disk,
hostname, git identity, …) so **recreate pre-fills the reset form faithfully**
instead of resetting it to defaults (see [Reset a VM](#reset-a-vm)). The **clone
token is deliberately never written to the index** (secrets never touch disk); how
that plays out on reset — re-supply the token unless *Preserve project .env +
checkout* is enabled — is covered in
[GitHub Authentication](README.md#github-authentication).

Creating a VM whose name already exists is refused with a clear message rather than
colliding — delete it, or recreate it to reset it.

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
