# Sign in to your coding agent

Sandbar installs the coding agents you choose in each VM, but it copies no credential into the VM. Sign in once inside each VM, and choose how much the agent may do without asking you.

## Before you start

- **The agent is installed in the VM.** The create form has a checkbox for each agent: *Install Claude Code*, *Install OpenAI Codex*, *Install OpenCode* and *Install Pi*. From the command line, use `sandbar create --with-claude`, `--with-codex`, `--with-opencode` or `--with-pi`. All four are off the first time; after that, sandbar remembers your last choices for the next VM. To add an agent to an existing VM, reset it with that agent checked (`R` on its tile, or `sandbar reset NAME --with-pi`, for example).
- **You have a shell in the VM.** Press `S` on the VM's tile, or run `sandbar shell NAME`.
- **You have an account with the agent's provider**, such as a Claude subscription for Claude Code or a ChatGPT account for Codex.

Each agent installs its current release when the VM is created or reset.

## Claude Code

### Sign in

Run `claude` with no arguments. The first time, sandbar asks two questions before Claude Code starts:

1. *Enable Claude Code Remote Control for every interactive session?* See [Remote Control](#remote-control) below. If you're not sure, answer no; you can turn it on later.
2. *Start sessions in bypass permissions mode instead of auto mode?* See [Choose a permission mode](#choose-a-permission-mode).

Then, because the VM has no credential, sandbar starts Claude Code's sign-in. Open the link it prints in your workstation browser, sign in, and paste the code back if asked. Later runs of `claude` go straight to the prompt.

Claude Code asks once, in each new folder, whether you trust the files in it. That question comes from Claude Code, not sandbar, and appears whatever mode you choose.

### Choose a permission mode

Claude Code starts in [auto mode](https://code.claude.com/docs/en/permission-modes) unless you answer yes to the bypass question. In auto mode, a classifier reviews each action and blocks risky ones without asking you about routine work. Bypass permissions mode skips every check. If auto mode isn't available for your account or model, Claude Code starts in manual mode, which asks before each action.

We recommend bypass permissions mode for work in a VM. The VM is the boundary that limits what the agent can reach, so per-action checks mostly slow the agent down. Choose auto mode if you want a second check anyway, for example when the VM holds a token that can push to repositories other people use. See the [Security model](../background/security-model.md) for what the VM does and doesn't isolate.

To change modes later:

- Press `Shift+Tab` in a session to switch between modes.
- Use `/config` in Claude Code to change the default for new sessions.

Sessions you start from `claude agents` use the same default mode.

!!! warning "If the first session opens in manual mode"
    Right after the first sign-in, Claude Code sometimes ignores the chosen mode, or Remote Control isn't connected yet. It may also ask once about its full-screen renderer. Type `/exit` and run `claude` again.

### Remote Control

[Remote Control](https://code.claude.com/docs/en/remote-control) lets you follow and steer a session running in the VM from `claude.ai/code`, another browser or the Claude mobile app. Claude Code and its tools keep running inside the VM. Use it to check a long task away from your desk, answer when Claude needs a decision, or get a notification when tests finish.

If you answer yes to sandbar's question, every interactive session connects to Remote Control. If you answer no, it stays off. You can still connect one session with `/remote-control` (or `/rc`), or turn the default on from `/config`. Remote Control needs an eligible Claude subscription and a full-scope Claude.ai sign-in; API keys and setup tokens don't support it.

With Remote Control on, the session's transcript and tool activity go through Anthropic so you can see them on other devices.

Remote Control doesn't remove the approval for messages between machines. Even with permission prompts skipped, Claude Code asks you before a session sends a message to a session on another machine or in the cloud. Sessions in the same VM can message each other without asking.

### Start a new session from your phone

A VM with Claude Code has a `new-session` skill. Ask Claude something like "start a new session in sandbar with a prompt to fix the failing test", and it starts a separate Claude Code session in the VM that appears in Remote Control. This is how you open a new session in the VM from the Claude web or mobile app, which otherwise can start only cloud sessions.

- Name a repository loosely and Claude finds it in the VM's checkout layout, `~/<host>/<group>/<repo>`.
- In a Git repository, the new session works in a new worktree branched from `origin/main`, unless you ask it to work in the directory itself.
- The new session starts in the same permission mode as the session that asked for it.
- Remote Control must be on by default. If it is off, the skill starts nothing and tells you how to turn it on.

If you already have something at `~/.claude/skills/new-session`, sandbar leaves it in place and doesn't install its skill there.

## Codex

### Sign in

Codex offers a remote control feature, and sandbar asks about it the first time you run `codex` with no arguments.

To sign in without remote control, which we recommend if you're unsure:

1. Run `codex login --device-auth`. Open the link it prints in your workstation browser, sign in with your ChatGPT account, and enter the code it shows.
2. Run `codex`. When sandbar asks *Enable Codex remote control support?*, press `enter` to answer no. Codex opens, signed in.

A device code sign-in works wherever the VM runs, because it doesn't need your browser to reach the VM.

To use remote control instead, run `codex` and answer `y`. Sandbar then signs you in with a device code, starts Codex's app server with remote control on, and runs `codex remote-control pair` so you can pair the ChatGPT app. When setup finishes, Codex opens. The app server starts again whenever the VM boots. Afterwards:

- Run `codex agents` to connect to the app server from the VM.
- Run `codex remote-control pair` to pair another device.

Sandbar remembers your answer. Later runs of `codex` go straight to Codex.

### Permission mode

Sandbar configures Codex to run without approval prompts and without Codex's own sandbox (`approval_policy = "never"` and `sandbox_mode = "danger-full-access"` in `~/.codex/config.toml`), because the VM is the sandbox. We recommend you keep it. To have Codex ask before acting, change those two settings in `~/.codex/config.toml`.

## OpenCode

### Sign in

Run `opencode` in your project directory. In OpenCode, type `/connect`, choose your provider, and follow its prompts to sign in or paste an API key.

### Permission mode

Sandbar doesn't change OpenCode's permission settings. By default, OpenCode runs commands and edits files without asking, and asks before reading `.env` files. We recommend you keep the defaults: sandbar keeps tokens in `.env` files in the VM, so the one prompt protects them. See [OpenCode's permissions documentation](https://opencode.ai/docs/permissions/) to change them.

## Pi

### Sign in

Run `pi` in your project directory. In Pi, type `/login` and connect a subscription or an API key.

### Permission mode

Pi doesn't ask for approval before it runs a tool. In a folder that holds Pi extensions or other project resources, Pi asks once whether to trust the folder before it loads them. That trust decision doesn't limit what Pi's tools can do.

## What every agent gets

When a VM is created or reset, each selected agent receives a short block of instructions in its global instruction file. The block says the agent is in a sandbar VM, that passwordless `sudo` is available for work you authorized, and points to the `sandbar-environment` skill. The skill describes the VM's name and connection type, the `sandbar` commands you run on your workstation, how to reach guest ports, where secrets come from, browser reviews, tmux keys, the clipboard and the installed tools. It lives at `~/.agents/skills/sandbar-environment`, and Claude Code finds it through `~/.claude/skills/sandbar-environment`.

| Agent | Global instruction file |
| --- | --- |
| Claude Code | `~/.claude/CLAUDE.md` |
| Codex | `~/.codex/AGENTS.md` |
| OpenCode | `~/.config/opencode/AGENTS.md` |
| Pi | `~/.pi/agent/AGENTS.md` |

Sandbar updates only its own marked block and skill files, and keeps any text you add to those files. If one of your own skills is already at the skill's path, provisioning stops with an error that names it. A VM with no agents selected gets no instructions. Start a new agent session after provisioning to load them.

## Related

- [Quickstart: a Drupal site with DDEV](quickstart.md)
- [Security model](../background/security-model.md)
- [Secrets](../using-sandbar/secrets.md): give an agent API keys and tokens
- [Files and state](../reference/files-and-state.md#coding-agent-preferences-and-migration): where your agent choices are saved
