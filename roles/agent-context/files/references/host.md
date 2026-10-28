# Host-side `sand` commands

`sand` runs on the user's workstation, not in this guest; there is no `sand`
command here. When a task needs one, give the user the exact command to run
on their workstation instead of trying it yourself. Fill in `NAME` from
[this-vm.md](this-vm.md); it can differ from `hostname`.

| Goal | Command on the workstation |
| --- | --- |
| Open a shell in the VM's tmux session | `sand shell NAME` |
| List the VM's checkouts and branch/PR state | `sand land NAME` |
| Review a checkout's changes in a browser | `sand land NAME PATH --review` |
| Review only part of the history | `sand land NAME PATH --review -- HEAD~2` (any `git diff` arguments) |
| Open a draft PR for a pushed branch | `sand land NAME PATH --pr` |
| Send a clipboard image into the guest | `sand paste-image NAME` |
| Change CPUs or memory | `sand reset NAME --cpus 8 --memory 16GiB` (see below) |
| Upgrade `sand` itself | `brew upgrade sand` |

`PATH` is the checkout's guest path, as `sand land NAME` lists it. If the
user has the same VM name on more than one connection profile, add
`--profile PROFILE`. In the interactive TUI (`sand` with no arguments) the
same actions are keys on the VM's tile: `S` shell, `l` landing, `R` reset,
`v` paste image.

## `sand reset` deletes the guest

A reset rebuilds the VM from its base image. **Every file in the guest is
deleted unless the user passes a preserve flag**, including when the reset is
only meant to change `--cpus` or `--memory`. Always say this when suggesting
a reset, and suggest the flag that keeps what they need:

- `--preserve-home`: the whole guest home except `~/.ssh/authorized_keys`.
  The safe default for resizing a working VM.
- `--preserve-agents`: coding-agent settings, credentials, and sessions.
- `--preserve-project`: the project checkout created with the VM, with its
  uncommitted work and `.env`.
- `--preserve PATH`: one more directory in the guest home; repeatable. Quote
  `'~/…'` so the workstation's shell does not expand it.

Running processes, containers, and anything outside the home do not survive.
Unpushed work outside the preserved paths is lost.

More: <https://lullabot.github.io/sandbar/latest/using-sand/cli-reference/>
