# Golden Templates

Golden templates are named, reusable clone sources captured from an existing sand-managed VM. Snapshot an existing VM once you've provisioned it to your liking, then create new VMs from that template instead of the shared base image — skipping provisioning entirely and landing a fresh clone in seconds.

## Why use templates

The shared base image (`sandbar-base`) is built once and shared across all your VMs — changing it is expensive and rare. A template lets you bake in project-specific or team-specific setup **above** the base: extra toolchains, development dependencies, pre-cloned repos, or secrets — anything you'd otherwise script or type into every new VM. Once that setup is saved as a template, `sand create --template mytemplate` gives you a fresh, isolated clone with everything already in place.

A template is faster than re-running that setup by hand, and more flexible than rebuilding the base itself — the base stays stable while your templates evolve and branch.

## Concepts

### What gets saved

When you snapshot a VM into a template, `sand` captures:

- **The guest disk:** everything installed or changed inside the VM — toolchains, configs, cloned repos — except per-VM identity (hostname, git user.name/email). More precisely: everything except hostname, git config, and SSH keys. Secrets propagation is detailed below.
- **Sizing and settings:** vCPUs, memory, disk size, locale, domain suffix, docker-proxy-host, and the clone URL (but not the token).
- **Provisioning metadata:** which playbook version the template was built from, and which toolsets (Claude Code, DDEV, Go, Java) were installed in its base.

Templates are **per-host, per-connection-profile** — a template saved under a local profile never leaks into a remote profile, and vice versa. Templates are never exported; they live as reserved Lima instances under `${LIMA_HOME}` on their own host.

### Power-state behavior

When you snapshot a running VM, `sand` stops it, clones it (capturing the state at that moment), then restarts it. The source VM ends up exactly as it started — running or stopped. A template's own power state is always stopped (it is a clone source, not a working VM), but the source VM's state is preserved.

### Staleness

A template's `status` in `sand template list` reflects whether its playbook predates the playbook embedded in the current `sand` binary:

- **`current`:** built with the same playbook version as this binary.
- **`stale`:** built with an older playbook. A stale template still works, but new VMs cloned from it miss playbook updates (new base packages, role changes, etc.). Snapshot a fresh VM to get a current template.
- **`unknown`:** the embedded playbook could not be located (a broken build, or the binary was cross-compiled). Treat as `current` and proceed.

## Snapshot: save a VM as a template

### CLI

```sh
sand template snapshot <source-vm> <name>
```

Capture `<source-vm>` (a running or stopped sand-managed VM) into a new golden template named `<name>`. The source VM's power state is preserved.

```sh
# Snapshot a running VM named 'dev' into a template called 'golden'
sand template snapshot dev golden

# Snapshot, specifying a connection profile
sand template snapshot dev golden --profile work
```

The name must be a slug — lowercase alphanumeric with hyphens (e.g., `golden`, `next-gen`, `ai-tools`). It cannot collide with an existing template or managed VM in the same scope.

### TUI

1. On the board, press `t` on the tile of the VM you want to snapshot.
2. Type the template name and press `ctrl+s` to confirm.

The snapshot runs in the background (just like a create or reset). Its progress streams into the progress pane; you can leave and come back (`l` reopens the log).

## Create from a template

### CLI

```sh
sand create --template <name>
```

Create a new VM cloned from the golden template `<name>` instead of the shared base image. All other `sand create` flags work normally:

```sh
# Create a fresh VM from a template, using defaults
sand create --template golden

# Create with custom sizing
sand create --template golden --name myvm --cpus 8 --memory 16GiB

# Create on a specific connection profile
sand create --template golden --profile work
```

The `--template` flag is mutually exclusive with `--rebuild` (templates are already cloned, not re-built from base) and `--base-name` (the template instance IS the clone source).

### TUI

1. Press `n` (or `enter` on the ghost tile) to start a create.
2. A "Source" field appears in the form, defaulting to the shared base image. Press `enter` or space to open the source selector.
3. Pick "Base image" or a template name from the list.
4. Fill in the rest of the form and confirm with `ctrl+s`.

## List templates

### CLI

```sh
sand template list
```

List all golden templates in the current scope (or use `--profile <name>` to list on a specific connection profile):

```
NAME             SIZE     CREATED     SOURCE  STATUS
golden           5.2 GiB  2025-07-15  dev     current
next-gen         6.1 GiB  2025-07-14  dev     stale
```

Columns:

- **NAME:** the template's user-facing name.
- **SIZE:** the template's disk size (the reserved Lima instance's guest disk).
- **CREATED:** when the template was saved.
- **SOURCE:** the name of the managed VM it was snapshotted from.
- **STATUS:** whether the template is `current` (matches this binary's playbook), `stale` (older playbook), or `unknown` (playbook could not be checked).

## Delete templates

### CLI

```sh
sand template delete <name>
```

Delete a golden template. If any managed VMs were cloned from it, a warning lists them — they keep working, but can no longer be recreated from this template afterward (if you `--recreate` a VM cloned from a deleted template, it re-clones from whatever source its creation recorded, which is now missing). The delete proceeds anyway; there is no `--force` gate.

```sh
sand template delete golden
sand template delete golden --profile work
```

### TUI

1. On the board, press `d` on the tile of a template. (Templates appear as special tiles in the VM list, marked as such.)
2. If there are dependents, a warning lists them.
3. Confirm the delete.

## Secrets and identity propagation

### What propagates

Everything installed or configured in the guest disk propagates into clones **except:**

- **Hostname** — each clone gets its own hostname (its Lima instance name, or the `--hostname` flag).
- **Git user.name / user.email** — each clone is provisioned with its own git identity (the `--git-name` and `--git-email` flags, or host git config).
- **SSH keys** — each clone generates its own keypair inside the guest.

This matters because clones are independent VMs. A template capturing `git config user.name "Alice"` should not force every clone to author commits as Alice — that should be up to the person cloning it.

### Secrets (tokens, API keys)

**Do not put secrets into a template.** If a template's guest disk contains a GitHub token, API key, or other credential, every clone copies that credential. A leaked template then leaks every VM cloned from it.

Instead:

1. Snapshot the template **before** storing secrets in the VM's guest (or clean them out first).
2. Use the host-side secrets store (`sand secrets`, key `e` on a tile) to manage per-VM credentials, or put them in a cloned project's `.env` at create time (the `--clone-token` flag).

See [Secrets](secrets.md) for how the host secrets store works and how to manage them per-VM and per-directory.

## When to rebuild the base vs. snapshot a template

- **Rebuild the base** (`sand create --rebuild`) when the playbook itself needs to change and every future VM should pick it up — a new Go version, a new Ansible role, a critical security update.
- **Snapshot a template** when you want to preserve a specific VM's state — extra setup, team conventions, a pre-cloned project — without affecting the base or other templates.

The two are independent: you can have a current base and a stale template (the template's playbook is older but its extra setup is still valuable), or vice versa.
