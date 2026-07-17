# The Embedded Playbook

`sand` provisions each VM's software with an Ansible playbook — the same
`site.yml`, `roles/`, `ansible.cfg`, and `group_vars/` this repository has
carried since before its conversion to a Go CLI. That playbook is not a
separate tool you run: it is baked into the `sand` binary and driven
entirely by `sand` itself. This page documents the mechanism for
contributors who need to touch `roles/` or `site.yml`. **It is not a guide
to running Ansible by hand** — nothing here should be read as an
instruction to install Ansible or invoke `ansible-playbook`; `sand` does
both, inside the guest, on your behalf.

## The fileset is embedded in the binary

`playbook_embed.go` at the repository root `go:embed`s the whole fileset —
`site.yml`, `ansible.cfg`, `inventory`, `roles/`, `group_vars/`,
`scripts/`, and `shipped-profiles/` — into the `sandbar` module as
`PlaybookFS`. This is what lets a Homebrew-installed `sand` provision without
a repository checkout anywhere on disk: the playbook travels inside the
compiled binary.

## How the playbook directory is resolved

At run time, `provision.LocatePlaybook()` (`internal/provision/playbook.go`)
picks the directory to mount into the VM in two tiers:

1. **Working tree first.** It runs `git rev-parse --show-toplevel` and, if
   that succeeds and the resulting directory contains `site.yml`, uses it
   directly.
2. **Embedded fallback.** Otherwise — no git checkout, e.g. a
   Homebrew-installed binary run outside any repository — it extracts
   `PlaybookFS` to a fresh private temp directory and uses that.

This is the single most useful fact on this page for a contributor: **if
you run `go run ./cmd/sand` from inside this checkout, your uncommitted
edits to `roles/` or `site.yml` take effect on the very next provision.**
There is no rebuild-and-reinstall step to remember.

## The mount, and what runs where

The resolved directory is mounted into the guest as its **only** mount, and
that mount is **read-only**. Ansible itself is installed *inside the
guest*, not on the host, and the playbook runs there with
`--connection=local` — the host never executes `ansible-playbook` at all.

Inside the guest, the in-guest provisioning script (`inGuestScript` in
`internal/provision/provision.go`) rsyncs the mounted playbook fileset into
a guest-local working copy before each run, filtered to exactly the members
`playbook_embed.go` declares (`site.yml`, `ansible.cfg`, `inventory`,
`roles/***`, `group_vars/***`, `scripts/***`, `shipped-profiles/***`) —
never the whole mount, which in working-tree mode would otherwise be an
entire git checkout. Per-phase
extra-vars are streamed into the guest over stdin into `/dev/shm` (tmpfs)
and removed on exit; they are never placed on argv or written to the
persistent disk, so a clone token never appears in a process listing.

## The three provisioning phases

`site.yml`'s roles gate on a `provision_phase` variable
(`internal/provision/vars.go` sets it per run) that takes one of three
values:

| Phase | What runs | When |
|---|---|---|
| `base` | Heavy setup: `base`, (conditionally) `samba`, `dev-tools`, `claude-code` | Building the shared base image once, before any clone exists |
| `finalize` | Light, per-VM identity: `base`, `user`, `project`, `repo-profile` | Against each `limactl clone` of the base image |
| `full` | Everything, in one pass | The default when the phase isn't otherwise specified |

This split is what lets `sand` build one expensive base image and clone it
cheaply for every subsequent VM, running only the light identity-specific
work (hostname, git identity, optional project clone, optional repo
provisioning profile) against each clone.

## The seven roles

`roles/` has seven roles: `base`, `user`, `samba`, `dev-tools`, `claude-code`,
`project`, and `repo-profile`. `site.yml` runs them in that order, gated by
`provision_phase` as above. `samba` is worth calling out specifically:
`internal/provision/vars.go` sets `samba_enabled: false` on every `sand`
run (Lima VMs use a bind mount or `limactl copy` instead of a Samba share),
so the role exists in the tree and is exercised by CI's syntax check, but it
does not execute on the `sand` path.

## The repo-profile finalize stage

`roles/repo-profile` is the guest-side implementation of user-facing
[Provisioning Profiles](../using-sand/provisioning-profiles.md). `site.yml`
runs it immediately after `project` (which performs the clone), gated on
`project_clone_url` being set and `provision_phase != 'base'`. The role
itself gates further on the clone actually containing
`.sandbar/profile.yml` (`repo_profile_manifest_path`,
`roles/repo-profile/defaults/main.yml`) — a repo without that file costs
only a single `stat` and introduces no new variables.

When the manifest is present, `roles/repo-profile/tasks/main.yml` runs six
steps, in this order: validate the manifest with
`scripts/validate_profile.py` (aborting finalize with the validator's own
message on failure), install declared apt packages, reconcile the declared
`toolset` per-clone (a no-op for a tool already in the base, a per-clone
install otherwise, using the same [shipped provisioning
profiles](#the-shipped-profiles-directory) documented below), include each
declared role, enable/start each declared service, and finally run the
repo's `seed` tasks file. See the role's own header comment
(`roles/repo-profile/tasks/main.yml`) for the full rationale of that
ordering.

**This stage never touches the shared base image or its version stamp.**
Every task in `roles/repo-profile` runs guest-side, inside the clone, during
the finalize phase — the base's `v2:<playbook-hash>:<toolset>` stamp is
written only by Go (`internal/provision.writeBaseVersion`), only during the
base phase, inside the base lock. Nothing here reads `LimaHome` or mutates
the base image.

**Repo-supplied role content never enters the embedded playbook fileset.** A
repo's `.sandbar/roles/<name>/` roles are read *in place* from the cloned
checkout, via a runtime symlink onto a dedicated `repo-roles` roles-path
entry (`ansible.cfg`) that `roles/repo-profile` creates at finalize time.
They are never copied into `roles/` or `shipped-profiles/roles/`, and
`repo-roles` is deliberately absent from all three sides of the
embed/rsync/hash triple-pin (`playbook_embed.go`'s `go:embed` list, the
rsync allowlist above, and `internal/provision/baseversion.go`'s
`playbookFileset` hash) — only **shipped-profile** content (the next
section) participates in those three lists. The symlink itself is
rsync-excluded, so the next `Recreate`/`Reset` wipes and recreates it fresh.

## The shipped-profiles directory

`shipped-profiles/` holds the four dev tools `sand` has always offered —
`claude`, `ddev`, `go`, `java` — re-expressed as **shipped provisioning
profiles**: each is a `shipped-profiles/<tool>/profile.yml` manifest in
exactly the same format a repo's own `.sandbar/profile.yml` uses, backed by
Ansible role content under `shipped-profiles/roles/` (on `ansible.cfg`'s
`roles_path`, alongside `roles/`). Unlike repo-supplied content, this
directory *is* embedded, rsynced, and hashed like any other playbook
content — it ships with `sand` itself, so it participates in all three
lists documented above (`playbook_embed.go`, the rsync allowlist, and
`playbookFileset`) and must be updated in lockstep with them, guarded by
`TestGuestSyncCopiesOnlyThePlaybook` and the base-stamp tests.

Because a shipped profile must be applicable at either tier — the base
phase, via `--with-*` flags/TUI toggles, and the finalize phase, via a
repo's `toolset` reconciliation (previous section) — every shipped
profile's role content must be **idempotent and phase-agnostic**: it cannot
assume it is only ever running once, during base build. See
`shipped-profiles/<tool>/profile.yml` for each tool's own comment on how it
satisfies this.

## CI coverage

The `lint` job in `.github/workflows/test.yml` runs
`ansible-playbook -i localhost, --connection=local site.yml --syntax-check`
on every push and pull request, so a syntax error anywhere in the playbook
fails CI fast — this is the one place in the pipeline where
`ansible-playbook` runs directly, and it exists purely to validate the
fileset that `sand` embeds.

## `inventory` is vestigial

`inventory` still contains a single placeholder host
(`ansible_host=CHANGE_ME`), and it is part of the embedded fileset. It is a
holdover from before the Go conversion, when the playbook was run directly
against a real inventory. On the `sand` path it is unused: `sand` invokes
Ansible with `-i localhost,` inside the guest and never reads this file.
It stays in the tree because it is embedded and harmless, not because
anything on the `sand` path consults it.
