# Why sandbar

## The tension

AI coding agents earn their keep when you stop approving their every
move. Claude Code with `--dangerously-skip-permissions`, and the
equivalent on other agents, edits files, runs commands, and pushes
branches without pausing to ask. The flag means what it says: an agent
running that way can do whatever the machine it runs on can do,
including reading your SSH keys or force-pushing to a branch you care
about.

Every practical answer to that trades away something. A container
shares your host kernel and almost always bind-mounts your repo, so the
agent's reach extends back onto your disk. Approving each action puts
you back in the loop you were trying to leave. A cloud sandbox moves
the risk off your machine by moving your code onto someone else's.
Rolling your own VM gives you the right boundary and hands you all the
provisioning work.

We wanted the autonomy of skip-permissions with a boundary the agent
genuinely cannot cross, and without rebuilding a VM by hand each time.
That's sandbar.

## The one decision that defines it

A sandbar VM has no writable path back to your machine.

Each VM is a full guest, not a container sharing your kernel. Its only
mount is the read-only provisioning playbook. Lima's stock host-home
share is forced off. So the agent inside cannot see or touch your host
filesystem, and `limactl delete` provably removes everything the VM
ever produced, because there was never a channel for it to leave
anything behind. Files move in and out only when you upload or download
them on purpose. The [Security Model](reference/security-model.md)
spells out the full set of guarantees and their two deliberate
exceptions.

That property is what lets sandbar run the agent with permissions
skipped by default and treat it as safe rather than reckless. The
recovery model for a misbehaving agent isn't a hardened host. It's `R`
to reset the VM from a clean base image, or `d` to delete it. You get
the autonomy because the blast radius is one disposable VM and stops
there.

## What else is in the box

The sealed boundary is the reason to use it. These are the reasons it
stays pleasant to use.

- **Setup happens once.** One base image carries the full toolchain
  (Docker, ddev, Node, Go, Python, a JDK, `gh`, tmux, direnv). Every VM
  after the first is a clone of it plus a light finalize pass for
  hostname, git identity, and an optional repo clone, so new
  environments come up in seconds. See
  [How Provisioning Works](getting-started/how-it-works.md).
- **A board and a CLI.** Run `sand` for a
  [terminal board](using-sand/tui.md) where every action fires from the
  focused tile, or script `sand create` and `sand shell` headlessly for
  CI. Builds keep running when you navigate away, and tmux sessions
  survive a disconnect because systemd linger is on.
- **Secrets stay off argv.** Clone tokens and
  [secret values](using-sand/secrets.md) stream into the guest over
  stdin into tmpfs and are removed on exit, so they never appear in a
  process listing on either side.
- **Least-privilege access is documented, not assumed.** The
  [Security Model](reference/security-model.md#a-least-privilege-token-reasonable-agent-access)
  walks through a fine-grained GitHub token that can push code and read
  pull requests and issues but cannot merge or close them, paired with
  branch protection, so an unattended agent can open a PR yet cannot
  push straight to your default branch.
- **Notifications come for free.** Remote control is on by default, so
  the Claude app tells you when a session needs input or finishes, with
  no webhook to configure.

## How it compares

Running an agent in a local VM isn't unique to sandbar, and it's worth
being precise about where the rest of the field sits.

The nearest neighbors are **clawk** and **agent-vm**, both recent.
[clawk](https://github.com/clawkwork/clawk), posted to Hacker News in
mid-July 2026, is close on the surface: a single Go binary you install
with Homebrew that gives a coding agent a local full VM with its own
kernel, already agent-agnostic across Claude, Codex, and others.
[agent-vm](https://github.com/sylvinus/agent-vm) is built on Lima, the
same backend sandbar uses today, is multi-agent, and even shares the
base-template-then-clone provisioning model.

Both make the opposite call on the decision above. clawk live-mounts
your repo over virtio-fs, and its own README notes that an agent can
therefore commit bad code or push to any repo. agent-vm mounts your
working directory read-write by default. That writable mount is the
convenient choice, because the agent edits the same files already open
in your editor, and it's the exact channel sandbar removes. If you want
the agent working directly against your on-disk checkout, one of those
may suit you better. If you want the checkout out of the agent's reach
entirely, that's the line sandbar draws.

Everything else sits further off on one axis or another:

| Tool(s) | Isolation | Local / Cloud | Gap vs sandbar |
|---|---|---|---|
| [clawk](https://github.com/clawkwork/clawk) | Full VM (Apple Virtualization / Firecracker) | Local | Live-mounts your repo writable; CLI-only; macOS-first |
| [agent-vm](https://github.com/sylvinus/agent-vm) | Full VM (Lima) | Local | Mounts working dir writable by default; Bash script; persistent, not disposable |
| [E2B](https://e2b.dev/), [Modal](https://modal.com/), [Daytona](https://www.daytona.io/), [Runloop](https://www.runloop.ai/), [Vercel Sandbox](https://vercel.com/docs/vercel-sandbox), [Cloudflare Sandbox](https://developers.cloudflare.com/), [Blaxel](https://www.blaxel.ai/) | microVM or gVisor | Cloud | Your code runs on someone else's machine; API-first |
| [Arrakis](https://github.com/abshkbh/arrakis), [microsandbox](https://github.com/superradcompany/microsandbox), [qbox](https://www.qbox.sh/), [SmolVM](https://particula.tech/blog/smolvm-vs-firecracker-sandbox-ai-generated-code) | microVM | Self-host / local | A sandbox SDK, server, or fleet infra, not an opinionated dev-VM workflow |
| [container-use](https://github.com/dagger/container-use), [claude-code-sandbox](https://github.com/textcortex/claude-code-sandbox), [ClaudeBox](https://github.com/RchGrav/claudebox), [Anthropic devcontainer](https://code.claude.com/docs/en/sandbox-environments) | Container (shared kernel) | Local | Shares your kernel and bind-mounts the repo |
| [Vibe Kanban](https://github.com/BloopAI/vibe-kanban), [Conductor](https://conductor.build/), Crystal, [Sculptor](https://imbue.com/sculptor/) | Host worktrees / containers | Local | Little or no isolation boundary; agent runs on the host |
| [sandbox-runtime](https://github.com/anthropic-experimental/sandbox-runtime), bubblewrap, Seatbelt | OS process confinement | Local | Per-process host confinement, policy-dependent, no provisioned environment |
| [Coder](https://github.com/coder/coder), Gitpod/Ona, Codespaces | Workspaces | Cloud / self-host | General-purpose remote dev, not agent-disposable |

The pattern runs through all of them. The tools that get the VM
boundary right still mount your files into it, and the tools that seal
the environment do it in the cloud or as an SDK rather than a local dev
VM. Sandbar is the point where local, full-VM, sealed, and disposable
meet.

## Where it's going

Lima and Claude Code are the first supported backend and agent, not the
definition of the tool. The provisioning model is built to add more of
both: other agents behind the same disposable-VM workflow, and other
backends behind the same commands, with Proxmox and similar targets
planned so a sandbar VM can land on a home lab or a server, not only a
laptop.

## Try it

If you run coding agents and you've felt the pull between letting one
run free and keeping it off your actual machine, sandbar settles it.
One command installs it, Homebrew brings in the single prerequisite,
and there's no Ansible or Go toolchain to set up first.

```bash
brew install lullabot/sandbar/sand
sand          # open the board, press n to create a VM, S for a shell
```

Point an agent at a repository, let it work unattended, hear about it
when it's done, and delete the VM when you finish. The worst a bad run
costs you is one disposable VM.

## In three sentences

> `sand` is a single Go binary that provisions disposable local VMs for
> AI coding agents, so you can let one run unattended without giving it
> any path back to your actual machine. Each VM is a full guest with no
> writable host mount, so a bad run costs you one throwaway VM, and
> deleting it provably removes everything the agent produced. It builds
> one base image and clones it for every VM after, so a fresh
> environment with your whole toolchain comes up in seconds. It runs on
> Lima with Claude Code today, with more backends like Proxmox and more
> agents on the way.
