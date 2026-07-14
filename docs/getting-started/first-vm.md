# Your First VM

## The interactive way

Run `sand` with no arguments to open the TUI board:

```bash
sand
```

Press `n` to create a new VM, fill in the form, and confirm. Once it's
running, select its tile and press `S` to get a shell inside it.

## The headless way

To create a VM without the TUI:

```bash
sand create
```

Then connect with:

```bash
sand shell NAME
```

See the [CLI Reference](../using-sand/cli-reference.md) for the full flag
list.

## What to expect the first time

The very first VM you create builds a shared base image (`claude-base`),
which does a full, identity-free install and takes a while. Every VM you
create after that clones the base image instead of reinstalling everything,
so it's fast. See [How Provisioning Works](how-it-works.md) for why it's
built this way.

## Logging into Claude Code

`sand` installs the Claude Code CLI but does **not** provision a credential
for it — no host-side token is copied into the VM. Shell into the VM (`S`
on its tile, or `sand shell NAME`) and run `claude` once to complete a
normal interactive login.

A full interactive login is required, rather than a headless token, because
remote control is enabled by default (see
[Security Model](../reference/security-model.md)) and remote control
sessions need a full-scope OAuth login — the inference-only token from
`claude setup-token` can't establish one, so headless token auth isn't
supported here.

Once you're logged in, notifications arrive through Claude Code's remote
control: you're alerted in the Claude app when a session needs input or
finishes, with no webhook or extra configuration required.
