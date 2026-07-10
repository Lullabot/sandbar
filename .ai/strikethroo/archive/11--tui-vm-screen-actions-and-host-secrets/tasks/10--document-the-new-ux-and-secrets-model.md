---
id: 10
group: "documentation"
dependencies: [1, 3, 4, 9]
status: "completed"
created: 2026-07-09
model: "haiku"
effort: "low"
skills:
  - technical-writing
---
# Document the new UX and the host secrets model

## Objective

Update the README and the `internal/ui` package doc to describe the new
screen-responsibility split, the reset action, and — most importantly — the
host-side secrets store, stating plainly that it is unencrypted.

## Skills Required

- **technical-writing** — accurate, non-hedging documentation of a security-
  relevant trust model.

## Acceptance Criteria

- [ ] `README.md`'s GitHub-token section (currently a 6-step list around line 230)
      is rewritten to describe the full lifecycle: a token supplied at create time
      still clones the repo and still lands in the per-org `.env`, **and** is now
      recorded in the host secrets store and re-applied to
      `~/.config/sandbar/secrets.env` on every `sand`-initiated start.
- [ ] A new README subsection documents the secrets editor: how to reach it
      (`e` on the VM screen), that it works whether the VM is running or stopped,
      the `KEY=VALUE` line format, that keys must match `[A-Za-z_][A-Za-z0-9_]*`,
      that blank lines and `#` comments are ignored, and that a value may contain
      `=`.
- [ ] That subsection states, without euphemism, that `secrets.json` is stored
      **unencrypted** on the host at mode `0600` under
      `${XDG_DATA_HOME:-~/.local/share}/sandbar/`, and that a host compromise
      therefore exposes every sandbox's secrets.
- [ ] It documents the guest side: `~/.config/sandbar/secrets.env`, mode `0600`,
      sourced from both `~/.profile` and `~/.bashrc`.
- [ ] It documents the limitation: a VM started outside `sand` (a bare
      `limactl start`) does not get freshly-applied secrets.
- [ ] Any README keybinding table is updated to the new split — list:
      `enter`, `n`, `f`, `/`, `X`, `q`; VM screen: `s`, `x`, `r`, `R`, `S`, `d`,
      `u`, `g`, `e`, `esc`. If no such table exists, add one.
- [ ] Every reference to the **TUI's** recreate flow becomes "reset".
      `sand create --recreate` remains documented as-is — it is a real, unchanged
      flag.
- [ ] `internal/ui/model.go`'s package doc, which currently calls the TUI "a thin
      interactive surface over the lima.Client (VM lifecycle) and
      provision.Provisioner (create/recreate) packages", is updated to name the
      screen-responsibility rule (the list selects, the VM screen acts) and the
      secrets store.
- [ ] Verification: `grep -rni "recreate" README.md` returns only lines about
      `sand create --recreate`, and nothing describing the TUI.
- [ ] Verification: `grep -c "secrets.env" README.md` returns at least `1`, and
      `grep -i "unencrypted" README.md` returns at least one match.
- [ ] Verification: `go build ./... && go vet ./...` still succeed (the package
      doc is a comment, but a malformed one can break `go vet`'s doc checks).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Files: `README.md`, `internal/ui/model.go` (package comment only).
- No `AGENTS.md` exists in this repository, and there is no root `CLAUDE.md`, so
  no AI-facing configuration file needs updating. Do not create one.
- Do not document `internal/secrets`' Go API in the README — it is an internal
  package. Document the *user-visible* behaviour.

## Input Dependencies

Every implementation task (1, 2, 3, 4, 6, 7, 8, 9). Read the code as merged, not
the plan, so the documentation describes what was actually built. Where the two
disagree, the code is authoritative — and say so in your task report, because a
divergence from the plan is worth surfacing.

## Output Artifacts

- Accurate user-facing documentation of the new UX and trust model.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Read first:** `README.md` lines ~225–310 (the token section and the playbook
variable table), and `internal/ui/model.go` lines 1–6.

**On the trust model.** This is the paragraph that matters most. The project's
prior posture was explicit — `internal/registry/registry.go` says "secrets never
touch the on-disk index" — and this change deliberately reverses it for a new,
separate file. The README must not bury that. Something like:

> **Where secrets live.** `sand` stores each VM's secrets in
> `${XDG_DATA_HOME:-~/.local/share}/sandbar/secrets.json`, **unencrypted**, at
> mode `0600` inside a `0700` directory. This is a deliberate trade: it is what
> lets you edit a VM's token without booting the VM. It also means anyone who can
> read your user account's files can read every sandbox's secrets. The managed-VM
> index (`managed-vms.json`) remains secret-free.

Do not soften "unencrypted" to "stored locally" or "kept on your machine."

**On the keybinding table**, the authoritative source is
`internal/ui/keys.go`'s `defaultKeys()` and `viewHelp()`. Read them rather than
transcribing from this task, in case the implementing tasks diverged.

**On the package doc**, the current text is:

```go
// Package ui holds the Bubble Tea model, views, and commands for the sand
// TUI. It is a thin interactive surface over the lima.Client (VM lifecycle) and
// provision.Provisioner (create/recreate) packages: all blocking I/O happens in
// tea.Cmds so Update never stalls, and the long-running provisioner streams its
// output into a scrollable progress pane.
```

Keep its shape and its good bits (the "all blocking I/O happens in tea.Cmds"
sentence is load-bearing guidance for future contributors). Replace
"(create/recreate)" and add the screen rule and the store. Roughly:

```go
// Package ui holds the Bubble Tea model, views, and commands for the sand
// TUI. It is a thin interactive surface over the lima.Client (VM lifecycle),
// provision.Provisioner (create/reset), registry.Registry (which VMs are ours),
// and secrets.Store (per-VM host-side secrets) packages.
//
// Screens divide by responsibility: the list selects a VM and owns the global
// actions (new, filter, search, stop all); the VM screen owns every action that
// targets one VM. All blocking I/O happens in tea.Cmds so Update never stalls,
// and the long-running provisioner streams its output into a scrollable
// progress pane.
```

**Testing philosophy.** This task writes no tests. Its verification is the four
`grep` assertions in the Acceptance Criteria plus a clean `go vet`. Run them and
paste the output into your task report.

</details>
