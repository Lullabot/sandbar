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
such as `foo/bar`.

Where each scope lands in the guest:

| Scope | Guest location |
| --- | --- |
| Global (default) | `~/.config/sandbar/secrets.env`, sourced from both `~/.profile` and `~/.bashrc` |
| `foo/bar` | `~/foo/bar/.env` |

A scoped `.env` is written in [direnv](https://direnv.net/)'s dotenv format
and approved with `direnv allow`, so a repo-scoped secret shows up as an
`.env` file direnv picks up automatically the moment your shell `cd`s into
that repo's working directory — you don't have to source anything by hand.

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

## GitHub tokens

A key named `GH_TOKEN` gets special handling on top of the generic scope
mechanism above: for any **non-empty** (directory) scope, `sand` also wires
`git`/`gh` credentials for that subtree, via a git `includeIf
"gitdir:~/<scope>/"` stanza that points at a generated credential helper.
Put a token under `[github.com/acme]` and `git`/`gh` authenticate
automatically for anything under `~/github.com/acme/` — no manual `gh auth
login`, no per-repo credential setup. This is a convention read by a small,
fixed table of recognized token names (`internal/provision/gitcred.go`), not
a feature of the secrets store itself — the store only ever holds
`(scope, KEY, VALUE)` triples and has no idea what GitHub is.

**The global scope is the one exception.** A `GH_TOKEN` with no scope is
still delivered to the guest as a plain environment variable, but it does
**not** get the automatic git-credential wiring — that only fires for a
named, non-empty scope. See below for where the create-time clone token
lands by default.

### Creating a fine-grained token

GitHub's fine-grained personal access tokens are recommended over classic
tokens: they're scoped to specific repositories, grant only the permissions
you choose, and can't be created without an expiry. Create one at
**Settings → Developer settings → Personal access tokens → Fine-grained
tokens** with:

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

### Supplying it and where it lands

Provide the token at VM-create time — the TUI create form's `GitHub token`
field, or `sand create --clone-token` — alongside a repo URL to clone. From
there:

- It's written into the cloned repo's per-org `.env` as `GH_TOKEN` (treat
  that file as a secret; it's what makes the git/gh wiring above work for
  that directory).
- The create form seeds it into the host secrets store's **global** scope,
  so — per the exception above — it does **not** get the automatic
  git-credential wiring on its own. The per-org `.env` written at clone time
  is what authenticates `~/<host>/<org>/`. To get the same token wired for
  other scopes too, copy it into a `[host/org]` section with the secrets
  editor (`e`).
- It's applied to the guest's `~/.config/sandbar/secrets.env` on every VM
  start (see [scopes](#scopes-global-vs-per-directory) above), so it's
  always present in the environment, even after a reset.

### Precedence and multiple orgs

`GH_TOKEN` takes precedence over any token `gh auth login` stored, because
`gh` is the configured git credential helper — `git`/`gh` over HTTPS use
whichever token is in the environment. For multiple organizations or
clients, prefer a **separate VM per org** over juggling several tokens on
one VM: VMs are disposable, and this keeps each context's credentials and
code fully isolated.

### Rotating, expiring, and revoking

Fine-grained tokens must have an expiry. When one expires or you rotate it,
update the secret in the secrets editor (`e` on the VM's tile) or re-supply
the new token the next time you create a VM, then revoke the old token in
GitHub's settings.

### Reset and the token

The token lives in the host secrets store, not in the managed-VM index (see
[Files and State](../reference/files-and-state.md)), so it survives a
[reset](tui.md#resetting-a-vm) and is re-applied to the rebuilt VM. But a
reset re-clones the project during its finalize pass, which runs **before**
stored secrets are written into the guest — so resetting a VM that cloned a
**private** repo still needs the clone token re-supplied on the reset form,
unless you enable the reset form's project-directory preserve toggle, which
skips the re-clone entirely. See [Resetting a VM](tui.md#resetting-a-vm).
