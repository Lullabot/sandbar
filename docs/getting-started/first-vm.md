# Your First VM

## The interactive way

Run `sandbar` with no arguments to open the TUI board:

```bash
sandbar
```

Press `n` to create a new VM, fill in the form, and confirm. Once it's
running, select its tile and press `S` to get a shell inside it.

## The headless way

To create a VM without the TUI:

```bash
sandbar create
```

Then connect with:

```bash
sandbar shell NAME
```

See the [CLI Reference](../reference/cli.md) for the full flag
list.

To clone a private GitHub or GitLab project while creating the VM, provide a
clone token with the repository URL. GitHub and GitLab.com are detected
automatically. For a self-hosted GitLab URL, select GitLab in the form or use
`sandbar create --clone-forge gitlab`; token setup and reset behavior are in
[Secrets](../using-sandbar/secrets.md#github-and-gitlab-tokens).

## What to expect the first time

The very first VM you create builds a shared base image (`sandbar-base`),
which can take a while. Every VM you
create after that clones the shared development tools and installs current
releases of your selected agents. See [How Provisioning Works](../background/how-it-works.md) for why it's
built this way.

## Putting the VM somewhere else

Both paths above create the VM on the machine you ran `sandbar` from. To put it
on another machine or on a Proxmox host, add that machine as a profile once
(press `p` in the board, then `n`), then pick it from the create form's
profile selector — or pass `sandbar create --profile NAME` headlessly. Nothing
else about the VM changes. See [Where VMs
Run](../advanced/connection-profiles.md).

## Signing in to your coding agent

See [Sign in to your coding agent](coding-agents.md) for Claude Code, Codex, OpenCode and Pi.

Remote Control does not remove Sandbar's cross-machine messaging boundary.
Claude Code still asks for your explicit approval before one session sends a
message to another machine or a cloud session, even though ordinary permission
prompts are skipped inside the VM. Messages between sessions in the same VM do
not cross that boundary.
