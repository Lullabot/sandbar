# Files and State

Where `sand` keeps its config, state, and per-VM files — on the host and in
the guest. This is the **only** page in these docs that spells these paths
out; every other page links here instead of restating them.

## Host paths

| Path | What it holds | Safe to delete? |
| --- | --- | --- |
| `${XDG_CONFIG_HOME:-~/.config}/sandbar/profiles.yaml` | The [Connection Profiles](../using-sand/connection-profiles.md) config: every location `sand` can run VMs on (the permanent Local profile, plus any `remote-ssh` profiles you've added), each profile's host/user/port/key-path/Lima-home, and which profile was last used. **Secret-free** — no password or key material, only a path to a key file — and hand-editable YAML. | Yes, but you lose every remote profile's connection settings (host/user/port/etc.) — `sand` reseeds a fresh Local-only file on next start. The VMs and their disks on any remote host are untouched; you'd need to re-add a profile pointing at that host to manage them again. |
| `${XDG_DATA_HOME:-~/.local/share}/sandbar/managed-vms.json` | The secret-free index of every `sand`-managed VM: which VMs `sand` created, the base image each was cloned from, and each VM's create configuration (the settings that pre-fill the form when you `Reset`). Since schema version 3, each entry is keyed by **(connection scope, name)** rather than by bare name, so the same VM name can exist under two different connection profiles without colliding. | Yes, but you lose the pre-filled reset config and `sand`'s record of which VMs it manages — a VM's actual disk is untouched, and Lima will still list it, but `sand` will no longer treat it as managed. |
| `${XDG_DATA_HOME:-~/.local/share}/sandbar/secrets.json` | The secret store: every KEY=VALUE pair you've set for every VM, across all scopes. Stored **unencrypted**, mode `0600` inside a `0700` directory. | Only if you're prepared to re-enter every secret for every VM — deleting it does not touch a running VM's already-rendered guest files, but the next apply will have nothing to render. |
| `${LIMA_HOME:-~/.lima}` | Lima's own instance store — disk images, `lima.yaml`, per-instance logs. `sand` does not own this directory; Lima does. | `sand` never deletes it directly; use `limactl delete` or Lima's own tooling. |

Sources: `internal/profiles/store.go` (`profiles.yaml` default path, schema, and CRUD), `internal/registry/registry.go:53` and `internal/registry/registry.go:62` (`managed-vms.json` default path), `internal/registry/registry.go:172` (the config saved into that index is the create config, stripped of the clone token), `internal/registry/registry.go:107`-`internal/registry/registry.go:129` (the v3 `(scope, name)`-keyed array shape), `internal/secrets/secrets.go:124` and `internal/secrets/secrets.go:133` (`secrets.json` default path), `internal/secrets/secrets.go:342`-`internal/secrets/secrets.go:350` (directory forced to `0700`) and `internal/secrets/secrets.go:357`-`internal/secrets/secrets.go:359` (file created at `0600`), `internal/provision/baseversion.go:62`-`internal/provision/baseversion.go:68` (`LIMA_HOME` fallback to `~/.lima`).

### Two different "scope" dimensions

Two unrelated things are both called a "scope" in this file store, and it's
easy to conflate them:

- The **connection scope** (`registry.Scope`: which [Connection
  Profile](../using-sand/connection-profiles.md) — local, or a specific
  `user@host:port` — a VM lives on) is what schema version 3 added to both
  `managed-vms.json` and `secrets.json`. It's what lets the same VM *name*
  exist independently on two different profiles.
- The secrets store's pre-existing **directory scope** (`global`, or a
  path like `foo/bar`) is unrelated and unchanged by this — it's *within
  one VM*, picking which guest directory a secret's `.env` lands in. See
  [Secrets](../using-sand/secrets.md#scopes-global-vs-per-directory).

A single secret is addressed by the combination of both: connection scope →
VM name → directory scope → key.

## Legacy data-directory migration

If you installed an older version of this tool under its previous name,
`claude-code-ansible`, its VM index lived at
`${XDG_DATA_HOME:-~/.local/share}/claude-code-ansible/managed-vms.json`. The
first time `sand` starts and finds no `sandbar/managed-vms.json` yet, it
copies that legacy file into the new location — copy-then-verify-then-remove,
so a crash mid-migration cannot lose it — and removes the old directory if it
ends up empty. **Your VMs are not lost**; they are simply indexed under the
new path from then on. This runs at most once: if `sandbar/managed-vms.json`
already exists, the migration is skipped.

Source: `internal/registry/registry.go:68`-`internal/registry/registry.go:90` (`migrateLegacyIndex`), called from `Load` at `internal/registry/registry.go:95`.

## Guest paths

| Path | What it holds |
| --- | --- |
| `~/.config/sandbar/secrets.env` | The VM's **global**-scope secrets, one `export KEY='VALUE'` line per pair. Sourced from both `~/.profile` and `~/.bashrc` so it's available in every shell type. Removed on VM start if no global secrets are stored, so stale values don't linger. |
| `~/<scope>/.env` | A **scoped** secret set for directory `<scope>`, auto-loaded by direnv when you `cd` into it and unloaded when you leave. |

Source: `internal/provision/secrets.go:37` (global scope sourced from `.profile`/`.bashrc`) and `internal/provision/secrets.go:78` (`RenderDotenv` for a scoped `~/<scope>/.env`).

See [Security Model](security-model.md) for why the host secrets store is
unencrypted and what that means for you.
