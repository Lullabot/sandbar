# Opening a VM in VS Code

`c` on a running VM's tile, or `sandbar code NAME` from the command line,
opens the VM in [VS Code](https://code.visualstudio.com/) through its
**Remote - SSH** extension. VS Code runs its server inside the guest, so the
editor, terminal and extensions all work on the VM's own files. Both entry
points share one implementation and print the same messages.

VS Code opens on the VM's project checkout (`~/<host>/<org>/<repo>`, the
layout `sandbar create --clone-url` uses), or on the guest home directory when
the VM was created without a repository.

## Prerequisites

- VS Code with Microsoft's **Remote - SSH** extension installed.
- VS Code's `code` shell command. On macOS, run **Shell Command: Install
  'code' command in PATH** from VS Code's command palette. If it is not on
  your `PATH`, sandbar also looks for the copy inside
  `/Applications/Visual Studio Code.app`.
- A running VM. Both `c` and `sandbar code` refuse a stopped VM, because a
  stopped Proxmox VM has no address to write down.

If sandbar cannot find `code`, it still writes the host entry and tells you
which alias to choose in VS Code's **Remote-SSH: Connect to Host…**.

## What sandbar writes

sandbar turns each VM into an ordinary named ssh host, so VS Code, plain
`ssh` and `scp` all reach it by the same alias:

| Where the VM runs | Alias |
| --- | --- |
| Local profile (Lima on this machine) | `lima-<name>` |
| A remote Lima or Proxmox profile | `sandbar-<profile>-<name>`, where `<profile>` is the profile name lowercased with other characters replaced by `-` (`Home Proxmox` → `home-proxmox`) |

The entries live in one file sandbar owns,
`${XDG_CONFIG_HOME:-~/.config}/sandbar/ssh_config`, one delimited block per
VM. sandbar rewrites a VM's block every time you open it, so don't edit
inside a block. Deleting a VM from the board (`d`) removes its block too.

sandbar never writes host entries into `~/.ssh/config`. ssh only reads the
managed file once `~/.ssh/config` includes it, so the first time you open a
VM, sandbar asks before adding this line to the **top** of `~/.ssh/config`:

```
Include "/home/you/.config/sandbar/ssh_config"
```

It has to go at the top: an `Include` placed after a `Host` block applies
only inside that block. If you decline, sandbar still writes the entry and
opens VS Code, but the alias will not resolve until you add the line
yourself. sandbar does not offer to edit `~/.ssh/config` when it is a
symlink (often into a dotfiles repository) or is not writable, or when
`sandbar code` is not run from a terminal. In those cases it prints the line
for you to add by hand.

## Notes by backend

**Local Lima.** The entry includes Lima's own per-VM `ssh.config` rather
than copying from it. Lima picks a new ssh port each time the VM starts, and
the include follows it, so the alias keeps working across restarts.

**Remote Lima.** The guest only trusts the remote host's Lima key, which
never leaves that host. So sandbar adds a public key from your workstation to
the guest's `~/.ssh/authorized_keys` and tells you which one. It uses the
profile's identity file (it needs the matching `.pub`), or else the first of
`~/.ssh/id_ed25519`, `id_ecdsa` or `id_rsa` that has both halves. The
connection goes through the remote host to the guest's sshd, so the guest's
sshd must allow TCP forwarding (`AllowTcpForwarding`, on by default). A reset
gives the guest a fresh `authorized_keys`, and sandbar adds the key back the
next time you open the VM.

**Proxmox.** The entry uses the guest's current IP address, read from the
QEMU guest agent, along with the profile's key and user. sandbar refreshes
the address each time you open the VM. If VS Code stops connecting after a
restart, the DHCP lease probably changed: press `c` again (or rerun
`sandbar code`).

On remote Lima and Proxmox, guests are disposable and get a new host key on
every reset, so their entries do not record host keys
(`StrictHostKeyChecking no`, `UserKnownHostsFile /dev/null`), matching
sandbar's own connections.

## Troubleshooting

- **"Could not resolve hostname lima-…" or "sandbar-…".** `~/.ssh/config`
  does not include the managed file. Add the `Include` line above at the top
  of `~/.ssh/config`.
- **The connection times out on a Proxmox VM.** Its address changed. Open it
  again with `c` or `sandbar code` to refresh the entry.
- **Remote Lima: "no ssh key pair found in ~/.ssh".** Create a key pair, or
  set the profile's identity path to a key that has a `.pub` beside it.
- **Remote Lima: "open failed: administratively prohibited" or "stdio
  forwarding failed".** The guest's sshd refuses TCP forwarding; set
  `AllowTcpForwarding yes` in the guest's sshd configuration.
- **Local Lima: "ssh config … not found".** Lima writes the VM's `ssh.config`
  when it starts the VM; start it once and try again.
- **To test without VS Code**, run `ssh <alias>`. If that connects, the
  problem is on VS Code's side; see its **Remote - SSH** output panel.
