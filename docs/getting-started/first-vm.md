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
create after that clones the shared development tools and installs current
releases of your selected agents. See [How Provisioning Works](how-it-works.md) for why it's
built this way.

## Putting the VM somewhere else

Both paths above create the VM on the machine you ran `sand` from. To put it
on another machine or on a Proxmox host, add that machine as a profile once
(press `p` in the board, then `n`), then pick it from the create form's
profile selector — or pass `sand create --profile NAME` headlessly. Nothing
else about the VM changes. See [Where VMs
Run](../using-sand/connection-profiles.md).

## Logging into Claude Code

When selected, `sand` installs the Claude Code CLI but does **not** provision a credential
for it — no host-side token is copied into the VM. Shell into the VM (`S`
on its tile, or `sand shell NAME`) and run `claude`: the first time, it
walks you through an interactive sign-in, then starts the session. Later
runs go straight to the prompt.

Under the hood, provisioning pre-seeds Claude Code's first-run onboarding
state so sessions start with bypass permissions active, and the provisioned
`claude` command runs `claude auth login` for you whenever you're not signed
in, so you're never dropped to an un-authed prompt.

`--dangerously-skip-permissions` and `/remote-control` aren't always active
on the very session right after you sign in — Claude Code can prompt once
about its full-screen renderer and come up in manual mode. If that happens,
quit (`/exit`) and run `claude` again. The provisioned `claude` prints a
one-time reminder about this on first use. (This is Claude Code's own first-run
sequencing, which shifts between releases, so `sand` flags it rather than
trying to script around it.) The per-directory trust dialog you see in each
new folder is separate and deliberate.

A full interactive login is required, rather than a headless token, because
remote control is enabled by default (see
[Security Model](../reference/security-model.md)) and remote control
sessions need a full-scope OAuth login — the inference-only token from
`claude setup-token` can't establish one, so headless token auth isn't
supported here.

Once you're logged in, notifications arrive through Claude Code's remote
control: you're alerted in the Claude app when a session needs input or
finishes, with no webhook or extra configuration required.

## Logging into Codex

If you created the VM with `--with-codex` (or enabled Codex in the TUI create
form), the Codex CLI is provisioned but no credential is included. Shell into
the VM and run `codex` with no arguments. On the first bare interactive run,
`sand` asks whether you want to enable Codex remote-control support.

If you answer yes, the wrapper guides you through Codex's device-code login,
sets up its persistent app server with remote control enabled, and runs the
initial `codex remote-control pair` flow so you can pair another device with
the ChatGPT app. You do not need to run those setup commands yourself. When
setup finishes, the regular Codex interface opens.

After setup, run `codex agents` to connect to the persistent app server
locally. To connect another device later, run:

```bash
codex remote-control pair
```

If you decline remote control, `sand` remembers that choice and opens Codex
normally. Later bare runs, commands with arguments, and non-interactive uses
continue directly to the ordinary Codex CLI without another onboarding prompt.
