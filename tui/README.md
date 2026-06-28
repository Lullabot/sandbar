# claude-vm — interactive TUI

`claude-vm` is a [Bubble Tea](https://github.com/charmbracelet/bubbletea) terminal
UI for managing this project's disposable Claude Code [Lima](https://lima-vm.io/)
VMs, with full CRUD: list and inspect instances, create new ones, run lifecycle
actions (start / stop / restart), and delete or recreate them.

It reimplements the orchestration in [`scripts/new-vm.sh`](../scripts/new-vm.sh)
in Go: the heavy, identity-free install is provisioned once into a stopped **base
image**, each VM is a fast `limactl clone` of that base, and a light **finalize**
pass applies the per-VM bits (hostname, git identity, `apt upgrade`, optional repo
clone). Creating and recreating a VM streams the provisioner's output into a
scrollable progress pane.

## Prerequisites

- **Lima** (`limactl`) on your `PATH`, new enough to support `limactl clone`. The
  TUI runs a preflight check on startup and exits with an install link if either
  is missing. (See [Lima installation](https://lima-vm.io/docs/installation/).)
- **Run it from within a checkout** of this playbook repository. On startup the
  TUI locates the playbook by walking up from the current git checkout to a
  toplevel that contains `site.yml`; it exits with an error if it cannot find one.
- You do **not** need Ansible on your host. The base image's Lima config installs
  `ansible` (and `rsync`) inside the guest, and the playbook is then run in the VM
  with `--connection=local` — the same model `new-vm.sh` uses.

## Build & run

```bash
cd tui
go build -o claude-vm ./cmd/claude-vm
./claude-vm
```

## Keybindings

The bindings below come from `tui/internal/ui/keys.go` and the per-view handlers
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
| `f` | Toggle the filter: show all VMs ↔ only claude-vm instances (managed + base) |
| `q` | Quit |

Pressing `S` suspends the TUI and hands your terminal to `limactl shell <name>`;
the TUI resumes when you exit the shell.

The **Managed** column marks which VMs `claude-vm` created: `yes` for a managed
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
| `esc` / `backspace` | Back to the list |
| `enter` | Back to the list |
| `q` | Quit |

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

Most fields default like `new-vm.sh` when left blank (hostname → the instance
name, user → your host username, CPUs → half the host's cores, memory/disk →
`8GiB`/`100GiB`). **`Name` is required** and starts empty — it does not silently
default. `GitHub repo URL` and `GitHub token` are optional and used only to clone
a private repo into the VM.

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
- **Preserve project .env + checkout** — keeps the per-org directory (the cloned
  repo and its `.env`). It is disabled when the VM cloned no repo. When enabled it
  **skips the re-clone**, so you do **not** need to re-supply a token to reset a
  VM that had cloned a private repo (the clone token is never stored).

Enabling either toggle copies that data out of the VM to a private temp dir on
your host and restores it after the recreate. The form warns that this moves your
Claude login and `.env` token off the VM: **do not preserve if you suspect the VM
is compromised.** See the main [Security Model](../README.md#security-model).

**Disk sizing.** A VM's disk can **grow** from the base floor (`20GiB`) but
cannot **shrink** below the current base's virtual size — qcow2 cannot shrink a
live disk — so the form enforces a minimum of the floor. A base built before
per-VM sizing keeps its old (larger) size and clones can't go under it; delete
`claude-base` to rebuild the base at the floor on the next create/reset.

## Managed VMs and safety

`limactl` lists **every** Lima instance on your machine, not just the ones this
tool created — your `default` template VM, a Colima docker VM, and so on all show
up in the list. You can safely list, inspect, start, stop, restart, and (with a
confirmation) delete any of them; those are ordinary `limactl` operations.

**Recreate is different**: it deletes the instance and re-clones it from a Claude
base image, so pointing it at an unrelated VM would replace that VM with a sandbox.
To prevent this, `claude-vm` records the instances **it** creates and:

- marks them in the **Managed** column (and the detail view), and
- offers **recreate only for managed VMs** (`f` filters the list down to
  claude-vm's own instances — managed clones and base images).

Base images (the heavy, identity-free images each VM is cloned from, such as
`claude-base`) are shown as `base` in the **Managed** column and labelled "base
image (clone source)" in the detail view, so they stand out from the disposable
VMs cloned from them.

The index of managed VMs is a small JSON file at
`${XDG_DATA_HOME:-$HOME/.local/share}/claude-code-ansible/managed-vms.json` (the
same data dir `new-vm.sh` uses). It is updated when you create, recreate, or delete
a VM from the TUI, and reconciled against `limactl list` on each refresh so an
instance deleted outside the TUI stops being flagged managed. VMs created outside
the TUI (e.g. directly via `new-vm.sh` or `limactl`) are treated as unmanaged;
delete still works, recreate does not.

The index also stores each managed VM's create configuration (CPUs, memory, disk,
hostname, git identity, …) so **recreate pre-fills the reset form faithfully**
instead of resetting it to defaults (see [Reset a VM](#reset-a-vm)). The **clone
token is never stored** — recreating a VM that had cloned a private repo will need
the token re-supplied, unless you enable *Preserve project .env + checkout*, which
keeps the existing checkout and skips the re-clone.

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

## Relationship to `new-vm.sh`

The TUI and [`scripts/new-vm.sh`](../scripts/new-vm.sh) share the same model and
security posture:

- **Base / clone / finalize**: provision the heavy work once into a stopped base
  image, clone each VM from it, then run a light finalize pass. The split is
  driven by the `provision_phase` variable (`base` / `finalize` / `full`).
- **Ephemeral VMs**: the only mount is the playbook, **read-only** — there is no
  writable host mount, so `limactl delete <name>` provably removes everything a VM
  produced. Move files in or out with `limactl copy`.
- **Secrets in tmpfs**: per-phase Ansible vars (which may carry a clone token) are
  streamed into the guest's tmpfs and removed on exit; they never land in argv or
  on the persistent disk.

The bash script is **unchanged**, and remains the scripted entry point: the
`curl … | bash` (Homebrew / standalone) install and the CI path both go through
`new-vm.sh`. The TUI is an interactive alternative for managing VMs from a
checkout, not a replacement for it.
