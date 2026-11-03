# Host-side `sandbar` actions

`sandbar` runs on the user's workstation, not in this guest; there is no `sandbar`
command here. When a task needs one, tell the user what to do on their
workstation instead of trying it yourself. Fill in `NAME` from
[this-vm.md](this-vm.md); it can differ from `hostname`.

Most users drive `sandbar` from its interactive TUI (`sandbar` with no arguments),
where each action is a key on the VM's tile. Give the TUI steps by default.
Give the CLI command instead when the user is scripting, asks for a command,
or the action has no TUI equivalent.

| Goal | In the TUI | CLI command on the workstation |
| --- | --- | --- |
| Open a shell in the VM's tmux session | `S` on the tile | `sandbar shell NAME` |
| List the VM's checkouts and branch/PR state | `l` on the tile opens the Landing pane | `sandbar land NAME` |
| Review a checkout's changes in a browser | `l`, select the checkout, `v` (`V` discards a saved review) | `sandbar land NAME PATH --review` |
| Review only part of the history | `l`, select the checkout, `R`, type the range | `sandbar land NAME PATH --review -- HEAD~2` (any `git diff` arguments) |
| Open a draft PR for a pushed branch | `l`, select the checkout, `enter` ("open draft PR") | `sandbar land NAME PATH --pr` |
| Send a clipboard image into the guest | `v` on the tile | `sandbar paste-image NAME` |
| Change CPUs or memory | `R` on the tile, edit CPUs/memory in the reset form (see below) | `sandbar reset NAME --cpus 8 --memory 16GiB` (see below) |
| Upgrade `sandbar` itself | none | `brew upgrade sandbar` |

For the CLI only: `PATH` is the checkout's guest path, as `sandbar land NAME`
lists it. If the user has the same VM name on more than one connection
profile, add `--profile PROFILE`. The TUI shows every profile's VMs on one
board, so it needs neither.

## A reset deletes the guest

A reset rebuilds the VM from its base image. **Every file in the guest is
deleted unless the user chooses to preserve it**, including when the reset is
only meant to change CPUs or memory. Always say this when suggesting a reset,
and name the option that keeps what they need, in the form the user is using:

| Keeps | TUI reset form toggle | CLI flag |
| --- | --- | --- |
| The whole guest home except `~/.ssh/authorized_keys`; the safe default for resizing a working VM | "Preserve the entire home directory" | `--preserve-home` |
| Coding-agent settings, credentials, and sessions | "Preserve agent settings and files" | `--preserve-agents` |
| The project checkout created with the VM, with its uncommitted work and `.env` | "Preserve ~/…" for the project's directory | `--preserve-project` |
| Another directory in the guest home | one "Preserve ~/…" row per known checkout | `--preserve PATH`, repeatable; quote `'~/…'` so the workstation's shell does not expand it |

In the TUI, turning on the whole-home toggle locks the others on, since it
already includes them. Running processes, containers, and anything outside
the home do not survive. Unpushed work outside the preserved paths is lost.

More: <https://lullabot.github.io/sandbar/latest/using-sand/cli-reference/>
