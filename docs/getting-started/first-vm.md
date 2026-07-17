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

The very first VM you create builds a shared base image (`sandbar-base`),
which can take a while. Every VM you
create after that clones the base image instead of reinstalling everything,
so it's fast. See [How Provisioning Works](how-it-works.md) for why it's
built this way.

## Logging into Claude Code

`sand` installs the Claude Code CLI but does **not** provision a credential
for it — no host-side token is copied into the VM. Shell into the VM (`S`
on its tile, or `sand shell NAME`) and run `claude`: the first time, it
walks you through an interactive sign-in, then starts the session. Later
runs go straight to the prompt.

Under the hood, provisioning pre-seeds Claude Code's first-run onboarding
state so even the very first session starts with bypass permissions active —
without the seed, Claude Code ignores `--dangerously-skip-permissions` until
onboarding has completed and the CLI is relaunched. Because seeding
onboarding also skips the wizard's login step, the provisioned `claude`
command runs `claude auth login` for you whenever you're not signed in, so
you're never dropped to an un-authed prompt. The one prompt you'll still see
each new folder is the per-directory trust dialog, which is deliberate.

A full interactive login is required, rather than a headless token, because
remote control is enabled by default (see
[Security Model](../reference/security-model.md)) and remote control
sessions need a full-scope OAuth login — the inference-only token from
`claude setup-token` can't establish one, so headless token auth isn't
supported here.

Once you're logged in, notifications arrive through Claude Code's remote
control: you're alerted in the Claude app when a session needs input or
finishes, with no webhook or extra configuration required.
