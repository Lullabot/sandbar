---
name: sandbar-environment
description: Use when working in a Sandbar development VM and the task involves this VM's name or connection type; host-side `sand` commands the user runs on their workstation (shell, land, browser review, reset, resizing, upgrading sand); reaching a guest port or web server from the workstation; where secrets and tokens come from; tmux keys or the clipboard; or the preinstalled tools.
---

<!-- Sandbar-managed environment skill -->

# Sandbar environment

This session runs inside a disposable Sandbar guest. Use passwordless `sudo`
for authorized guest system changes when needed. Keep the guest/workstation
boundary in mind: guest paths and processes are not host paths or processes.

- For this VM's name and connection profile type, read [this-vm.md](references/this-vm.md). Read it first whenever a later answer depends on either.
- For `sand` commands the user runs on their workstation, and what `sand reset` deletes, read [host.md](references/host.md).
- For opening a guest port or web server in the workstation's browser, read [ports.md](references/ports.md).
- For where secrets and Git tokens come from, read [secrets.md](references/secrets.md).
- For showing the user a diff in their browser, read [review.md](references/review.md).
- For tmux keys, persistent sessions, and clipboard behavior, read [terminal.md](references/terminal.md).
- For installed development tools and image transfer, read [tools.md](references/tools.md).

Treat the files as shipped defaults. Check live state before relying on a
specific version, executable, or tmux setting. Each reference links the page
of the Sandbar documentation it summarizes.
