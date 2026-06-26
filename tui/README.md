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
(`list.go`, `detail.go`, `form.go`, `progress.go`). `ctrl+c` quits from anywhere.
The active view's keys are also shown in the help bar at the bottom of the screen.

### List view

| Key | Action |
|-----|--------|
| `↑` / `↓` (also `k` / `j`) | Move the selection |
| `enter` | Open the selected VM's detail view |
| `n` | Open the create-VM form |
| `s` | Start the selected VM |
| `x` | Stop the selected VM |
| `r` | Restart the selected VM |
| `d` | Delete the selected VM (opens a confirmation) |
| `q` | Quit |

### Delete / recreate confirmation (on the list)

Pressing `d` on the list opens an inline prompt: `[y] yes  [r] recreate  [n] cancel`.

| Key | Action |
|-----|--------|
| `y` (also `d`) | Confirm: delete the VM |
| `r` | Recreate: delete and re-clone the VM from the base image (streams the provisioner) |
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
| `tab` | Next field |
| `shift+tab` | Previous field |
| `enter` | Create the VM (validates, then switches to the progress view) |
| `esc` / `backspace` | Cancel, back to the list |

Typing edits the focused field; `q` is a literal character here, so only `ctrl+c`
quits from the form.

### Progress / streaming view

| Key | Action |
|-----|--------|
| `↑` / `↓`, `pgup` / `pgdn` | Scroll the provisioner output |
| `q` | Quit |
| `esc` / `backspace`, `enter` | Return to the list (after the run finishes) |

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
