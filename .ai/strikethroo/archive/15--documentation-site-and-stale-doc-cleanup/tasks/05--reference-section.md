---
id: 5
group: "docs-content"
dependencies: [1]
status: "completed"
created: 2026-07-14
model: "sonnet"
effort: "medium"
skills:
  - technical-writing
  - go
---
# Reference section: files and state, security model, troubleshooting

## Objective

Write the three Reference pages: a single authoritative statement of where `sand` keeps its files on the host and in the guest, the security model of a disposable dev VM, and a troubleshooting page for the sharp edges that are currently undocumented or buried.

## Skills Required

`technical-writing`; `go` to read `internal/registry/` and `internal/secrets/` for the real paths and the legacy data-directory migration.

## Acceptance Criteria

- [ ] `docs/reference/files-and-state.md`, `security-model.md`, and `troubleshooting.md` are written (no stubs remain).
- [ ] `files-and-state.md` is the **only** place in `docs/` where the host state paths are spelled out; every other page links here rather than restating them.
- [ ] `security-model.md` states plainly what a `sand` VM is not safe for, and does not attribute the security posture to "the playbook" — it is `sand`'s posture.
- [ ] `troubleshooting.md` covers at minimum: a stale base image and how to rebuild it, `limactl list` failing while an instance is being cloned or deleted, a failed build's red tile and where to find its log, disk exhaustion on the Lima volume, and a Lima version too old to support `clone`.
- [ ] `uvx --with-requirements docs/requirements.txt mkdocs build --strict` exits 0 with no `WARNING`. Paste the output into your completion report.
- [ ] Report the file:line locations from which you took each host path.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Sources of truth: `internal/registry/registry.go` (managed-VM index, legacy data-dir migration), `internal/secrets/secrets.go` (secret store path and mode), `internal/lima/` (`LIMA_HOME`), `AGENTS.md` (records known upstream Lima bugs), `CHANGELOG.md` (records fixes for real failure modes — a good source of what actually goes wrong).

## Input Dependencies

Task 1: scaffold, nav, stubs.

## Output Artifacts

Three written pages under `docs/reference/`.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**`files-and-state.md`.** The audit found these paths scattered across three places in two files with differing wording. Consolidate here, verify each against the code, and make this the single home:

- `${XDG_DATA_HOME:-~/.local/share}/sandbar/managed-vms.json` — the secret-free index of sand-managed VMs, plus each VM's create config (which is what pre-fills the form when you Reset). `internal/registry/registry.go`.
- `${XDG_DATA_HOME:-~/.local/share}/sandbar/secrets.json` — the secret store, mode `0600`, **unencrypted**. `internal/secrets/secrets.go`.
- A **legacy data-directory migration**: an older `claude-code-ansible` directory is migrated on startup (`internal/registry/registry.go`). This is documented nowhere today. Document it — a user with an old install wants to know their VMs were not lost.
- `LIMA_HOME` / `~/.lima` — where Lima itself keeps the instances. `sand` does not own this directory; Lima does.
- In the guest: global secrets at `~/.config/sandbar/secrets.env`; scoped secrets at `~/<scope>/.env` for direnv.

A table plus a sentence each. State clearly which of these are safe to delete (and what deleting them costs) — that question is the reason people read this page.

**`security-model.md`.** The current README has this content but attributes it to "this playbook". Rewrite it as `sand`'s posture:

- A `sand` VM is a **disposable, single-purpose development environment**. It is not hardened.
- Do not use it for machines holding sensitive data. Keep that warning, prominently — a Material `!!! warning` admonition is the right form (the `admonition` extension is enabled).
- Secrets on the host are stored unencrypted at `0600` (link to `files-and-state.md`, do not restate the path).
- Credentials (`--clone-token`, secret values) are streamed into guest tmpfs and never appear on argv.
- `sand` does **not** provision a Claude Code credential; you log in inside the VM.
- The playbook fileset is mounted **read-only** into the guest, and it is the only mount.
- Samba is forced off for Lima-provisioned VMs.

**`troubleshooting.md`.** These are all real, and all currently undocumented or buried. For each: the symptom the user sees, why it happens, and the fix.

- **Stale base image.** The base image is built once and reused. When the base install changes (or you want a newer floor), delete `claude-base` — or use `--rebuild` — to rebuild it. This is currently buried in two different READMEs as an aside.
- **`limactl list` fails while an instance is being cloned or deleted.** A known upstream Lima behaviour; `AGENTS.md` and the changelog both reference it, and `sand` was hardened to survive it. Tell the user what they may see and that it is transient.
- **A build failed / the tile is red.** Where the log is, that `l` reopens the last log, and that builds keep running in the background when you leave the progress pane.
- **Out of disk on the Lima volume.** Clones are grown to `--disk`; the host must actually have the space. How to see it (the header shows free disk) and what to delete.
- **Lima too old to support `clone`.** `sand` depends on `limactl clone`. Say which Lima version introduced it and how to upgrade (`brew upgrade lima`).

Mine `CHANGELOG.md` for other real failure modes — it is a record of things that actually broke — but do not link to changelog entries from the docs; describe the behaviour.
</details>
