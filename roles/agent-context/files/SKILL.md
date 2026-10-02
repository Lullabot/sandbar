---
name: sandbar-environment
description: Use when working in a Sandbar development VM and its tmux, clipboard, or preinstalled tools affect the task.
---

<!-- Sandbar-managed environment skill -->

# Sandbar environment

This session runs inside a disposable Sandbar guest. Use passwordless `sudo`
for authorized guest system changes when needed. Keep the guest/workstation
boundary in mind: guest paths and processes are not host paths or processes.

- For tmux keys, persistent sessions, and clipboard behavior, read [terminal.md](references/terminal.md).
- For installed development tools and image transfer, read [tools.md](references/tools.md).

Treat the files as shipped defaults. Check live state before relying on a
specific version, executable, or tmux setting.
