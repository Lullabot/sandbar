# Security Model

What isolation a `sand` VM provides, and what it doesn't.

A `sand` VM is a **disposable, single-purpose development environment**. It
is convenient to throw away and rebuild, not hardened against a hostile
tenant or a determined attacker.

!!! warning
    Do not use `sand` to provision a machine that holds sensitive data, or
    one exposed to the public internet. It is designed for an isolated LAN
    or virtual network where the VM is treated as disposable — assume
    anything Claude Code does inside it could be adversarial, and plan to
    delete and rebuild the VM rather than trust it after the fact.

## What's true about a `sand` VM

- **Passwordless sudo** is enabled for the configured user (default:
  `claude`). It is not intended to host multiple users or untrusted
  workloads alongside the intended one.
- **The only guest mount is the playbook, and it is read-only.** There is no
  writable host mount — not even Lima's stock host-home share — so the VM
  cannot modify anything on your machine, and `limactl delete <name>`
  provably removes everything the VM produced. Move files in or out
  deliberately with the TUI's Upload/Download actions instead.
- **Guest listen ports are forwarded to loopback only.** Lima forwards a
  guest's listening TCP ports to `127.0.0.1` on the host that runs the VM —
  never a LAN interface — so a server inside the VM is not exposed to the
  network. Publishing one further (a cloudflared tunnel, an SSH forward) is
  always an explicit action; see
  [Web Servers and Ports](../using-sand/web-servers.md).
- **Samba is forced off** for Lima-provisioned VMs: there is no host-home
  mount to share, so there is nothing for it to serve.
- **`sand` does not provision a Claude Code credential.** You log in inside
  the VM yourself; no host-side Claude Code token is copied in. See
  [Logging into Claude Code](../getting-started/first-vm.md#logging-into-claude-code).
- **Claude Code runs with permission prompts skipped.** The provisioned
  settings set `skipDangerousModePermissionPrompt: true` and alias `claude`
  to `claude --dangerously-skip-permissions`, so Claude Code operates
  without interactive approval prompts. This is deliberate, not an
  oversight: it's appropriate specifically because the VM is ephemeral and
  isolated, and can be torn down and reprovisioned at any time.
- **Remote control is on by default** (`remoteControlAtStartup: true` in the
  provisioned settings), so you can drive and monitor a session from the
  Claude app once you've logged in inside the VM.
- **Claude Code sessions see the scoped secrets of the directory they work
  in**, via the provisioned direnv hooks (see
  [Secrets](../using-sand/secrets.md)). Scope your tokens accordingly — a
  secret placed at a scope is available to any agent working under it.
- **Codex runs with approvals off and sandbox disabled** (when selected). When
  you provision the VM with `--with-codex`, Codex is installed with
  `approval_policy = "never"` and `sandbox_mode = "danger-full-access"`, a
  deliberate choice because the ephemeral VM is the sandbox itself. Codex is
  opt-in; if you don't pass `--with-codex` or enable it in the TUI, it is not
  installed.
- **No Codex credential is provisioned.** You log in to Codex inside the VM
  yourself with your ChatGPT account; no host-side token is copied in. See
  [Logging into Codex](../getting-started/first-vm.md#logging-into-codex).
- **Credentials never touch argv.** A `--clone-token` and every secret value
  are streamed into the guest over stdin into tmpfs and removed via an exit
  trap — never passed as a command-line argument — so they cannot appear in
  a host or guest process listing.
- **Host-side secrets are stored unencrypted at `0600`.** See
  [Files and State](files-and-state.md) for the path and what deleting it
  costs. Treat that file as sensitive: anyone who can read your user
  account's files can read every VM's secrets.
- **The TUI's reset "preserve" options are a deliberate, opt-in exception**
  to "nothing leaves the VM": when enabled, the selected data (your Claude
  Code login, and/or a cloned project's checkout with its `.env`) is copied
  to a private host temp dir, restored into the reset VM, then deleted. They
  default off. Do not enable them if you suspect the VM you're resetting is
  compromised.

Together these mean: assume a `sand` VM can be fully compromised by whatever
you run inside it, and rely on deletion — not defense — to recover. Nothing
you do inside the VM is expected to reach your host filesystem except
through the two deliberate exceptions above.

## Landing: the audited counterpart to the no-host-mount boundary

The no-host-mount boundary above means code inside a `sand` VM cannot write
to your host filesystem. **Landing** (`l` on a tile, or `sand land`) is the
one deliberate path code takes to *leave* the VM at all, and it is built to
match that boundary rather than undercut it: Landing moves **PR metadata —
a branch name, a compare URL, a PR number and state — never a file, a diff,
or a commit's contents**. It reads the guest's git state and, for `--pr`,
calls GitHub through your own workstation's `gh`; it does not copy the
guest's working tree anywhere, and nothing it produces auto-executes on the
host.

This is a genuine **two-token split**, not a formality: the VM's Claude Code
uses its own least-privilege token (above) to `git push` from inside the
guest; opening the draft PR is a *separate* action that runs the
**workstation's** `gh` and its own, human-held credentials. The guest's
token never touches PR creation, and the workstation's `gh` never touches
the guest.

**What this provably does and does not do:** Landing controls what PR
metadata reaches your host and records that action in its own ledger — it
does not, and cannot, stop the guest from using its own push token to push a
branch; that authority was already granted at create time by the token's
own scope (see the least-privilege token section below). Landing narrows
what happens *after* a push, not what the guest's token can do. Code from
that branch reaches your host only when you choose to bring it there
yourself — a `gh pr checkout` or `git pull` you run and review — never as a
side effect of Landing itself.

## Publishing to drupal.org: agent decides what, host decides where

[Publishing to drupal.org](../using-sand/drupalorg-publishing.md) — replaying
a guest checkout's commits onto a drupal.org issue fork — splits authority
along one line: **the guest decides WHAT changes** (the commits, their
messages, and their file contents) and **the host decides WHERE they go**
(which fork, which branch, whether a merge request follows). The guest never
sees a drupal.org credential, a project, a branch name, or a URL; it only
ever produces an inert payload of commits and file actions that is
structurally incapable of naming a destination. That split is enforced by the
payload type itself, not by convention — see `internal/drupalorg`'s package
doc comment — so no amount of prompt injection or agent compromise can make
a guest's output smuggle in where it lands.

**No drupal.org credential ever enters a VM.** The guest clones and reads
drupal.org anonymously — anonymous requests already cover the entire
development loop, including CI results and merge-request feedback — and only
the host, using a personal access token that never leaves your workstation,
performs the one call that writes. This is a stricter version of the
least-privilege reasoning below, not a variant of it: a drupal.org account
PAT is not scoped to one repository the way a GitHub fine-grained token can
be — it is account-wide, and a working Drupal contributor's account may hold
push access to modules running on tens of thousands of sites. The only safe
credential to place in an agent-controlled VM for that kind of account is
none at all. See
[Files and State](files-and-state.md#host-paths) for where the token
actually lives and why its path is a convention rather than a config value.

**The limits, stated honestly.** This design does not make publication safe
in the abstract — it moves the one dangerous credential to the one place an
agent cannot directly reach, and stops there:

- **The host still holds a powerful, account-wide credential**, and it uses
  that credential to publish content an agent authored. The human-in-the-loop
  confirmation (see the publishing guide) is what stands between an agent's
  output and a public, permanent write — not any property of the credential
  itself. A compromised or careless human confirmation is still a compromise.
- **drupal.org's edge allowlist is not a containment boundary.** drupal.org
  runs a per-path, per-method allowlist in front of its GitLab API that
  blocks a few credential-minting endpoints (`POST /access_tokens`,
  `POST /deploy_tokens`, `POST /deploy_keys`) while routing every
  content-write endpoint this design uses straight through. It is tempting to
  read that as "drupal.org contains what an agent can do", but two things
  defeat that reading: `POST /api/graphql` is **not** blocked and returns 200
  unauthenticated — protecting GraphQL writes the same way "would require
  inspecting the request body, which is not a capability of our load balancer,
  as far as I know", per the infrastructure discussion in
  [#3379836](https://www.drupal.org/project/infrastructure/issues/3379836) —
  and `git push` over HTTPS reaches GitLab directly, bypassing the REST
  allowlist entirely. So the allowlist is a control on one specific REST
  path, not a barrier that would stop a leaked credential. **If a credential
  ever enters a guest, the allowlist will not save you** — the guarantee has
  to come from the credential's own boundary, or from its absence, exactly as
  above.

## A least-privilege token: reasonable agent access

`sand` can hand Claude Code a GitHub token at create time so it can clone,
pull, push, and open pull requests from inside the VM (see
[GitHub tokens](../using-sand/secrets.md#github-tokens)). An autonomous agent
uses whatever that token grants, so the token is itself a security boundary —
and scoping it well is a concrete, real-world exercise in giving an agent
*reasonable* access: enough to do the work, not enough to do damage that no
human reviewed.

GitHub's fine-grained personal access tokens fit this well: they're scoped to
specific repositories, grant only the permissions you choose, and can't be
created without an expiry. Create one at **Settings → Developer settings →
Personal access tokens → Fine-grained tokens** with:

| Permission | Access | Purpose |
| --- | --- | --- |
| Contents | Read and write | Push and pull code |
| Pull requests | Read | Read PRs without letting an agent self-merge to `main` |
| Issues | Read | Read issues without write access |
| Actions | Read and write | Inspect and trigger CI |
| Workflows | Read and write | Update workflow files |
| Metadata | Read-only | Always required (included automatically) |

Pull requests and Issues are deliberately read-only so an autonomous agent
can't merge its own PRs or close issues without a human in the loop — widen
them only if your workflow needs an agent to manage them directly.

!!! warning "Branch protection is required to keep agents off `main`"

    A `Contents: Read and write` token can `git push` to **any** branch,
    including `main`. The read-only `Pull requests` permission stops an agent
    from *merging* its own PR, but it does nothing to stop a direct push that
    bypasses review entirely. The token cannot enforce this — the repository
    must. Add a **branch protection rule** (or ruleset) on `main` and every
    other protected branch that **requires a pull request before merging**.
    Without it, nothing prevents an agent from pushing straight to `main`.

## Proxmox: a pool-scoped API token

The [Proxmox](../using-sand/proxmox.md) backend applies the same
least-privilege idea to a different boundary: the API token `sand` uses to
drive a Proxmox host. The concern there is not an agent inside a VM — it's
`sand` itself, on a host that may run VMs you care about and never want `sand`
to touch.

The guarantee is **structural, not behavioural**. `sand` places every VM it
creates into a dedicated resource pool, and the token is granted a custom role
scoped to `/pool/<pool>`. Proxmox enforces pool permissions by *projecting* the
role onto the pool's member VMs and storage only — a VM outside the pool has no
projection, so the token is denied on it, with no wildcard or path that escapes.
This is not `sand` choosing to leave other VMs alone; it is Proxmox refusing the
token if it tried. The setup guide's
[verification step](../using-sand/proxmox.md#verify-the-scope) is how you confirm
the token really did end up confined — if any grant lands at `/`, it hasn't.

Three privileges (`SDN.Use` for the bridge, `Sys.AccessNetwork` for the image
download, `Sys.Audit` for node stats) cannot be pool-scoped, because a pool
holds only VMs and storage. The guide grants each at the narrowest path that
works and names them explicitly rather than papering over the gap with a broad
role — none of the three grants any access to another VM.

The token is a secret, and `sand` handles it the way it handles every secret:
the [`profiles.yaml`](files-and-state.md#host-paths) file records only a **path**
to the token file (`token_file`), never the value, and `sand` refuses to read a
token file that is readable by group or other. The credential stays outside the
config that's safe to share.

### Guest SSH host keys are not pinned

`sand` reaches a Proxmox guest by SSH to the IP the guest's DHCP lease handed
out — an address a prior, now-deleted VM very likely used before it. Those VMs
are cattle: each is freshly created and presents a brand-new host key on an IP
the last one already put a different key on. So the guest transport
deliberately runs with `StrictHostKeyChecking=no` and
`UserKnownHostsFile=/dev/null` — trust-on-first-use pinning would fail on the
first reused IP, and the interactive prompt it falls back to cannot be answered
from the TUI (it would just hang). The tradeoff is bounded to this backend: it
means `sand`'s provisioning traffic to a guest is not protected against an
on-path attacker *on the VM subnet* who can impersonate the guest's IP. The
remote-Lima hop — SSH to a persistent host you configured — keeps host-key
pinning on, because there the key is stable and a change is worth stopping for.
Keep the VM subnet one you trust; it is the same network you must already trust
to reach the guests at all.

### Guests are offered only the `identity_path` key

The other half of the guest transport's SSH stance is which key it presents. A
guest trusts exactly one: the public half of `identity_path`, which `sand` has
cloud-init install for the login user. So the guest transport runs with
`IdentitiesOnly=yes` and offers that key alone — your SSH agent's other keys are
never presented to a guest, and never leave your machine's agent for it.

That is a privacy improvement, but the reason it is not optional is
availability. A guest's `sshd` disconnects after `MaxAuthTries` failed attempts
(6 by default), and every refused key burns one. Without this, an agent holding
six or more keys can exhaust the budget *before* `ssh` ever offers the right
one, so a correctly-provisioned guest and a perfectly good key still fail to
connect — and a single locked agent key turns a clean `Permission denied` into a
much more confusing `Too many authentication failures`.

The remote-Lima hop again keeps the opposite default: that host is a machine you
configured and authenticate to on your own terms, so an agent key or an
`ssh_config` `IdentityFile` is a legitimate way in, and `sand` does not restrict
it.
