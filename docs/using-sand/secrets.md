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

[myproject]
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
