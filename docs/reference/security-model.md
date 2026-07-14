# Security Model

What isolation a `sand` VM provides, and what it doesn't.

A `sand` VM is a **disposable, single-purpose development environment**. It
is convenient to throw away and rebuild, not hardened against a hostile
tenant or a determined attacker.

!!! warning
    Do not use `sand` to provision a machine that holds sensitive data, or
    one exposed to the public internet. It is designed for an isolated LAN
    or virtual network where the VM is treated as disposable — assume
    anything Claude Code does inside it could be adversarial, and plan to
    delete and rebuild the VM rather than trust it after the fact.

## What's true about a `sand` VM

- **Passwordless sudo** is enabled for the configured user (default:
  `claude`). It is not intended to host multiple users or untrusted
  workloads alongside the intended one.
- **The only guest mount is the playbook, and it is read-only.** There is no
  writable host mount — not even Lima's stock host-home share — so the VM
  cannot modify anything on your machine, and `limactl delete <name>`
  provably removes everything the VM produced. Move files in or out
  deliberately with the TUI's Upload/Download actions instead.
- **Samba is forced off** for Lima-provisioned VMs: there is no host-home
  mount to share, so there is nothing for it to serve.
- **`sand` does not provision a Claude Code credential.** You log in inside
  the VM yourself; no host-side Claude Code token is copied in. See
  [Logging into Claude Code](../getting-started/first-vm.md#logging-into-claude-code).
- **Claude Code runs with permission prompts skipped.** The provisioned
  settings set `skipDangerousModePermissionPrompt: true` and alias `claude`
  to `claude --dangerously-skip-permissions`, so Claude Code operates
  without interactive approval prompts. This is deliberate, not an
  oversight: it's appropriate specifically because the VM is ephemeral and
  isolated, and can be torn down and reprovisioned at any time.
- **Remote control is on by default** (`remoteControlAtStartup: true` in the
  provisioned settings), so you can drive and monitor a session from the
  Claude app once you've logged in inside the VM.
- **Credentials never touch argv.** A `--clone-token` and every secret value
  are streamed into the guest over stdin into tmpfs and removed via an exit
  trap — never passed as a command-line argument — so they cannot appear in
  a host or guest process listing.
- **Host-side secrets are stored unencrypted at `0600`.** See
  [Files and State](files-and-state.md) for the path and what deleting it
  costs. Treat that file as sensitive: anyone who can read your user
  account's files can read every VM's secrets.
- **The TUI's reset "preserve" options are a deliberate, opt-in exception**
  to "nothing leaves the VM": when enabled, the selected data (your Claude
  Code login, and/or a cloned project's checkout with its `.env`) is copied
  to a private host temp dir, restored into the reset VM, then deleted. They
  default off. Do not enable them if you suspect the VM you're resetting is
  compromised.

Together these mean: assume a `sand` VM can be fully compromised by whatever
you run inside it, and rely on deletion — not defense — to recover. Nothing
you do inside the VM is expected to reach your host filesystem except
through the two deliberate exceptions above.
