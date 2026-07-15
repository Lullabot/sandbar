---
id: 7
group: "documentation"
dependencies: [5]
status: "pending"
created: 2026-07-15
model: "haiku"
effort: "low"
skills:
  - markdown
  - technical-writing
complexity_score: 2
complexity_notes: "Documentation-only update reflecting the finished provider model and remote-Lima usage."
---
# Document the Provider model and remote-Lima-over-SSH usage

## Objective
Update the human- and agent-facing docs to describe the new `provider.Provider`
architecture, the local/remote-Lima providers, the host-access seam, the fake
conventions, and how to configure and use a remote-Lima-over-SSH target. The
current docs describe a Lima-only architecture down to the package level and must
be corrected.

## Skills Required
`markdown`, `technical-writing`.

## Acceptance Criteria
- [ ] `AGENTS.md` "Go package layout" and testing conventions describe: the `provider` package and `Provider` interface, the local and remote Lima providers, the host-access seam, and the updated fake guidance (fake the `Provider`/host-access seam; the local provider keeps runner-level fakes). The stale "fake `lima.Runner` and build a `*lima.Client`" instruction is replaced.
- [ ] `README.md` and `README-sand.md` document provider selection and a remote-Lima-over-SSH quick-start (host/user/port/identity/remote `LIMA_HOME` config), alongside the existing local quick-start.
- [ ] Docs match the actual flags/env/config names shipped in tasks 4–5 (no invented options).
- [ ] **Verification**: `grep -rn 'provider\|remote' AGENTS.md README.md README-sand.md` shows the new sections present; every command/flag shown in the docs is grep-able in the Go source (`grep -rn '<flag-or-env-name>' cmd internal --include='*.go'` returns a hit), proving no option was fabricated.

## Technical Requirements
- Keep the invariant-documenting code comments intact (they are handled in tasks 1–5); this task is prose docs only.
- Do not change any Go behaviour.

## Input Dependencies
- Task 5: the final shipped shape of the remote provider and its configuration.

## Output Artifacts
- Updated `AGENTS.md`, `README.md`, `README-sand.md`.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

`AGENTS.md` currently says under "Go package layout": "`lima` — typed wrapper over
the `limactl` CLI. All subprocess execution goes through a `Runner` interface…"
and under Testing: "`New`/model construction takes a `*lima.Client` built over the
fake." Both are now wrong. Rewrite them to describe: consumers depend on
`provider.Provider`; the local and remote Lima providers implement it; the
host-access seam is the local-vs-SSH split; tests fake the `Provider`/seam while
the local provider keeps its runner-level fakes; the three-entrypoint no-drift
rule now goes through the central provider constructor.

For the READMEs, add a short "Using a remote Lima host over SSH" section showing
the exact selection config the implementation shipped (pull the real flag/env
names from tasks 4–5 — do not invent them), next to the existing local
quick-start. Note the requirement that the remote host has Lima installed and a
working hypervisor, mirroring the existing local Lima install note.

Confirm every flag/env you document actually exists in the source before
finishing (the acceptance grep enforces this).
</details>
