# Secrets and tokens

The user manages this VM's secrets on their workstation, with `e` on the
VM's tile in the `sand` TUI. `sand` rewrites the guest files below whole
each time it applies secrets: when the VM starts and whenever the user saves.
Edits made to them in the guest are lost, so ask the user to add or change a
secret there instead. Never print secret values.

| Scope | Where it lands in the guest |
| --- | --- |
| Global | `~/.config/sandbar/secrets.env`, sourced by `~/.profile` and `~/.bashrc` when a shell starts |
| A directory such as `github.com/acme` | `~/github.com/acme/.env`, loaded by direnv |

A shell started before a global secret changed does not see the new value;
source `~/.config/sandbar/secrets.env` or start a new shell. Scoped `.env`
files load when a shell's prompt enters the directory. Claude Code also loads
them through its `SessionStart` and `CwdChanged` hooks, which run
`direnv export` for the session's working directory.

`GH_TOKEN` or `GITLAB_TOKEN` in a directory scope also configures Git
credentials for repositories under that directory, so `git` and `gh` (or
`glab`) authenticate there without a login. A global `GH_TOKEN` is only an
environment variable and configures no Git credential helper. A linked
worktree uses its main clone's token, not one scoped to the worktree's own
path.

More: <https://lullabot.github.io/sandbar/latest/using-sand/secrets/>
