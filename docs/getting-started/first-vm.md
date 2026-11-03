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

See the [CLI Reference](../using-sand/cli-reference.md) for the full flag
list.

To clone a private GitHub or GitLab project while creating the VM, provide a
clone token with the repository URL. GitHub and GitLab.com are detected
automatically. For a self-hosted GitLab URL, select GitLab in the form or use
`sandbar create --clone-forge gitlab`; token setup and reset behavior are in
[Secrets](../using-sand/secrets.md#github-and-gitlab-tokens).

## What to expect the first time

The very first VM you create builds a shared base image (`sandbar-base`),
which can take a while. Every VM you
create after that clones the shared development tools and installs current
releases of your selected agents. See [How Provisioning Works](how-it-works.md) for why it's
built this way.

## Putting the VM somewhere else

Both paths above create the VM on the machine you ran `sandbar` from. To put it
on another machine or on a Proxmox host, add that machine as a profile once
(press `p` in the board, then `n`), then pick it from the create form's
profile selector — or pass `sandbar create --profile NAME` headlessly. Nothing
else about the VM changes. See [Where VMs
Run](../using-sand/connection-profiles.md).

## Logging into Claude Code

When selected, `sandbar` installs the Claude Code CLI but does **not** provision a credential
for it — no host-side token is copied into the VM. Shell into the VM (`S`
on its tile, or `sandbar shell NAME`) and run `claude` with no arguments. On
the first bare interactive run, `sandbar` asks whether you want to enable
Claude Code Remote Control, then walks you through sign-in and starts the
session. Later runs go straight to the prompt.

[Remote Control](https://code.claude.com/docs/en/remote-control) keeps Claude
Code and all of its tools running inside the VM while letting you follow and
steer the same live session from `claude.ai/code`, another browser, or the
Claude mobile app. That makes it useful for checking a long-running task away
from your desk, replying when Claude needs a decision, attaching a photo from
your phone, or asking Claude to notify you when tests finish.

If you opt in, every interactive session connects automatically. If you
decline, `sandbar` remembers the choice and leaves cross-machine access off. You
can still enable one session later with `/remote-control` (or `/rc`), or turn
the default on from Claude Code's `/config` screen. Remote Control is available
for eligible Claude subscriptions and full-scope Claude.ai logins; API keys and
inference-only setup tokens do not support it.

Remote Control does not remove Sandbar's cross-machine messaging boundary.
Claude Code still asks for your explicit approval before one session sends a
message to another machine or a cloud session, even though ordinary permission
prompts are skipped inside the VM. Messages between sessions in the same VM do
not cross that boundary.

Under the hood, provisioning pre-seeds Claude Code's first-run onboarding
state so sessions start with bypass permissions active, and the provisioned
`claude` command runs `claude auth login` for you whenever you're not signed
in, so you're never dropped to an un-authed prompt.

`--dangerously-skip-permissions` and an opted-in Remote Control connection
aren't always active in the session immediately after the first sign-in. Claude
Code can also prompt once about its full-screen renderer. If the first session
comes up in manual mode or without Remote Control, quit (`/exit`) and run
`claude` again. The provisioned launcher prints a one-time reminder about this.
The per-directory trust dialog you see in each new folder is separate and
deliberate.

## Logging into Codex

If you created the VM with `--with-codex` (or enabled Codex in the TUI create
form), the Codex CLI is provisioned but no credential is included. Shell into
the VM and run `codex` with no arguments. On the first bare interactive run,
`sandbar` asks whether you want to enable Codex remote-control support.

If you answer yes, the wrapper guides you through Codex's device-code login,
sets up its persistent app server with remote control enabled, and runs the
initial `codex remote-control pair` flow so you can pair another device with
the ChatGPT app. You do not need to run those setup commands yourself. When
setup finishes, the regular Codex interface opens. The app server starts again
when the VM boots, even before you open a shell.

After setup, run `codex agents` to connect to the persistent app server
locally. To connect another device later, run:

```bash
codex remote-control pair
```

If you decline remote control, `sandbar` remembers that choice and opens Codex
normally. Later bare runs, commands with arguments, and non-interactive uses
continue directly to the ordinary Codex CLI without another onboarding prompt.
