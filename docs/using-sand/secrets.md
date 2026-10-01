# Secrets

`sand` keeps a per-VM store of `KEY=VALUE` secrets on your host and applies
them into each VM's guest environment as it starts.

## Where secrets live on the host

Secrets are stored in a single JSON file at:

```
${XDG_DATA_HOME:-~/.local/share}/sandbar/secrets.json
```

The file is written mode `0600`, and its parent directory `0700`, so it is
not readable by other users on the host. That is the only protection it
gets — **the file is unencrypted, plaintext JSON**. Anything you put in it
is stored on disk exactly as you typed it. Decide what you're willing to
keep there accordingly.

For the exact schema and how it's kept there, see
[Files and State](../reference/files-and-state.md).

## Scopes: global vs. per-directory

Secrets are organized into **scopes**. The **global** scope applies to the
whole VM. Any other scope is a directory path relative to the guest home,
such as `foo/bar`. This directory scope is entirely within one VM — it's
unrelated to which [Connection Profile](connection-profiles.md) that VM
runs on. Since the same VM name can exist under two different profiles at
once (see [Connection Profiles](connection-profiles.md#all-enabled-profiles-are-active-at-once)),
the secrets store also keys everything by which profile's connection a VM
belongs to first, so editing `claude`'s secrets on your `work` profile never
touches a same-named `claude` on `local`. You never see or manage that
connection-scope key directly — `e` on a tile always opens the right VM's
secrets, on whichever profile that tile's VM actually lives on. See
[Files and State](../reference/files-and-state.md#two-different-scope-dimensions)
for how the two scope dimensions are kept distinct on disk.

Where each scope lands in the guest:

| Scope | Guest location |
| --- | --- |
| Global (default) | `~/.config/sandbar/secrets.env`, sourced from both `~/.profile` and `~/.bashrc` |
| `foo/bar` | `~/foo/bar/.env` |

A scoped `.env` is written in [direnv](https://direnv.net/)'s dotenv format
and approved with `direnv allow`, so a repo-scoped secret shows up as an
`.env` file direnv picks up automatically the moment your shell `cd`s into
that repo's working directory — you don't have to source anything by hand.

Claude Code sessions get the same treatment even though direnv's shell hook
only fires at interactive prompts: the provisioned `~/.claude/settings.json`
carries `SessionStart` and `CwdChanged` hooks that run `direnv export` for
the session's working directory. So a session working in a repo sees that
repo's scoped secrets, wherever it was launched from — including sessions
dispatched via `claude agents`, which would otherwise inherit only the
environment of the directory the supervisor was started in.

## Editing secrets

Press `e` on a tile to open the secrets editor. It's a plain text buffer:
one `KEY=VALUE` pair per line for the global scope, and a `[scope]` header
line to start a new section for anything else, for example:

```
GLOBAL_TOKEN=abc123

[github.com/my-organization]
API_KEY=def456
```

Saving applies immediately to a running VM, or on the VM's next start if
it's stopped — editing does not require the VM to be up.

## How secrets reach the guest

Every secret value is streamed into the guest over the command's stdin as
it's written — **never passed as a command-line argument** — so a secret
never appears in a host `ps` listing. Each guest-side file is created at
mode `0600` before any bytes are written to it, so there is no instant at
which a world-readable file could hold a secret.

Deleting a VM (`d` on a tile) removes its host-stored secrets along with
its disk.

## GitHub and GitLab tokens

A key named `GH_TOKEN` gets special handling on top of the generic scope
mechanism above: for any **non-empty** (directory) scope, `sand` also wires
`git`/`gh` credentials for that subtree, via a git `includeIf
"gitdir:~/<scope>/"` stanza that points at a generated credential helper.
Put a token under `[github.com/acme]` and `git`/`gh` authenticate
automatically for anything under `~/github.com/acme/` — no manual `gh auth
login`, no per-repo credential setup. `GITLAB_TOKEN` receives the same
scoped Git credential-helper treatment for GitLab. Its helper username is
`oauth2`; on GitLab.com it targets `gitlab.com`, and for a self-hosted
instance it targets the host in the scope. Git and `glab` receive the scoped
`GITLAB_TOKEN` when run from that directory. A fresh shell can fetch and push from an existing checkout over HTTPS
without an interactive login. This is a convention read
by a small, fixed table of recognized token names (`internal/provision/gitcred.go`),
not a feature of the secrets store itself — the store only ever holds
`(scope, KEY, VALUE)` triples.

Scopes remain **directories relative to the guest home**, for every project.
Sand clones into `~/<host>/<group>/<repo>` (including nested groups), so the
top-level directory is `github.com`, `gitlab.com`, a self-hosted instance
such as `git.example.internal`, or `git.drupalcode.org` for a Drupal.org
HTTPS clone.

For GitLab, place `GITLAB_TOKEN` in that directory tree. A scope of
`[gitlab.com]` covers checkouts beneath `~/gitlab.com/`;
`[git.example.internal/platform]` covers checkouts beneath
`~/git.example.internal/platform/`. Use a deeper directory scope to limit
where the token is available. The GitLab helper uses the first directory
component as its HTTPS endpoint. It only activates for repositories in the
scoped subtree; matching a remote hostname elsewhere does not activate it.
There are no aliases or fallbacks from other directories to GitLab.com.

Self-hosted GitLab still requires choosing GitLab in the create form or
passing `--clone-forge gitlab`, since a hostname does not identify which
service runs there. Clone tokens support standard HTTPS endpoints without
explicit port numbers. Use a DNS name rather than an IPv6 literal.

Drupal.org projects follow the same directory-scope convention for secrets.
Their [publishing authentication](drupalorg-publishing.md) remains separate
from GitHub and GitLab token handling.

**The global scope is the one exception.** `GH_TOKEN` or `GITLAB_TOKEN` with
no scope is delivered to the guest as a plain environment variable, but does
**not** get automatic git-credential wiring — that only fires for a named,
non-empty scope. The create-time GitHub token uses global `GH_TOKEN` and is
also written to the cloned repository's `.env`; GitLab create tokens use the
repository-parent scope described below.

**Known limitation: a linked worktree inherits its main clone's token.**
`includeIf "gitdir:…"` is matched against git's `$GIT_DIR`, and for a linked
worktree (`git worktree add`) that is
`<main-clone>/.git/worktrees/<name>` — not the worktree's own working
directory. So a scope's `includeIf "gitdir:~/<scope>/"` stanza never
actually matches from inside a worktree of that scope; what authenticates
there instead is whatever `includeIf` matched the *main clone's* path. In
practice this means every worktree of a repo silently uses the main clone's
`GH_TOKEN`, and there is currently no way to give one worktree its own,
narrower token. This is a pre-existing property of how this wiring resolves
paths, not something specific to any one feature — it surfaced while
investigating
[publishing to drupal.org](drupalorg-publishing.md#why-publication-is-host-side),
whose manual per-issue-fork escape hatch would need a genuinely
per-worktree credential, which `includeIf "gitdir:…"` cannot express. It is
left unchanged here and recorded instead, since fixing it is a larger
change to this GitHub-token wiring than any one feature has needed so far.

### Creating a fine-grained token

Use a GitHub **fine-grained personal access token**, scoped to the
repositories the VM should touch and set to expire. It's important to give
the agent a token with minimal access, so if it goes off track it's limited
in the damage it can do. See further details in the
[Security Model](../reference/security-model.md#a-least-privilege-token-reasonable-agent-access).

### Supplying it and where it lands

Provide the token at VM-create time — the TUI's `Clone token` field or
`sand create --clone-token` — alongside a repository URL. GitHub URLs select
GitHub automatically; `gitlab.com` selects GitLab automatically. For a
self-hosted GitLab URL, choose GitLab in the form or pass
`--clone-forge gitlab`. `--clone-forge` also accepts `auto` (the default) and
`github`.

For GitHub, `sand` saves the token as `GH_TOKEN` in the host secrets store's
**global** scope, and writes it to the cloned repository's per-org `.env` for
Git and `gh`. Global scope does not configure the credential helper for
other directories; add `GH_TOKEN` to a `[host/org]` section to do that.

For GitLab, `sand` saves the token as `GITLAB_TOKEN` in the non-empty
repository-parent scope. For example,
`https://gitlab.example.internal/platform/tools/app.git` stores it in
`[gitlab.example.internal/platform/tools]`. This gives the initial clone
and future Git commands in that directory the right credential.

After creating or resetting the VM, and on every start, `sand` applies saved
secrets to the guest. Git credentials are supplied by the generated
host-specific helper. Treat scoped `.env` files as secrets. Edit or remove the
GitLab token in its scope (or the GitHub global token) in the secrets editor
(`e`); saving applies the change immediately to a running VM, or on its next
start if stopped.

### Precedence and multiple orgs

For GitHub, `GH_TOKEN` takes precedence over a token stored by `gh auth login`;
it is the configured Git credential helper's environment token. GitLab
credentials are scoped by instance host, so GitLab.com and a self-hosted
instance can use different tokens. For unrelated organizations or clients,
prefer a **separate VM per org** to keep each context's credentials and code
isolated.

### Rotating, expiring, and revoking

When a token expires or you rotate it, update `GH_TOKEN` in global scope or
`GITLAB_TOKEN` in its repository-parent scope in the secrets editor (`e` on
the VM's tile); saving applies it to a running guest immediately. Revoke the
old token in the forge's settings.

### Reset and the token

The token survives a reset because it is saved in the host secrets store.
Both the TUI and [`sand reset`](cli-reference.md#sand-reset-name) apply it
to the rebuilt guest, and the Git credential helper is restored for fresh
shells.

Cloning happens **before** saved secrets are applied. As a current limitation,
a reset does not reuse the saved forge token for that clone. To reset a VM
with a private project, enter the token again in the form's `Clone token`
field or pass `sand reset NAME --clone-token …`. This supplies the token for
provisioning; the saved secret is then reapplied. If you preserve the
project checkout, reset skips cloning and needs no clone token. See
[Resetting a VM](tui.md#resetting-a-vm).

### GitLab token permissions

For fine-grained GitLab personal access tokens, grant **Code Download** for
clone and pull, and **Code Push** for push. Choose the narrowest project or
group boundary that contains the repositories the VM needs; a group boundary
also authorizes its projects. See GitLab's
[fine-grained token permissions](https://docs.gitlab.com/auth/tokens/fine_grained_access_tokens_other/).
Legacy personal access tokens use the separate `read_repository` and
`write_repository` scopes for those operations.

## drupal.org: a notable exception

[Publishing a checkout's commits to drupal.org](drupalorg-publishing.md) is
the one write-capable integration `sand` offers that needs **no secret in
this store at all**, and no `.env` entry in the guest either. Everything
above this section exists to get a `KEY=VALUE` pair from your host into a
VM; drupal.org publication deliberately does the opposite — the credential
never leaves your workstation in the first place. A guest checkout clones
and reads drupal.org anonymously, over an unauthenticated HTTPS remote; only
the host, holding a personal access token from a file it reads directly (not
through this store), performs the one call that actually writes. See
[Files and State](../reference/files-and-state.md#host-paths) for where that
token file lives, and
[Security Model](../reference/security-model.md#publishing-to-drupalorg-agent-decides-what-host-decides-where)
for why: a drupal.org account PAT is account-wide rather than scoped to one
repository, so the only credential safe to place in an agent-controlled VM
for that kind of account is none.
