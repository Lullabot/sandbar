---
id: 5
group: "cli"
dependencies: [1, 4]
status: "completed"
created: 2026-07-09
model: "sonnet"
effort: "high"
skills:
  - go
---
# `sand secret sync` — Live Re-render Into a Running VM

## Objective
Add `sand secret sync` to re-render the host store's secrets into an already-running VM and report the effect of each change truthfully: GitHub/git secrets take effect immediately; global and directory env-var secrets take effect only in new shells.

## Skills Required
- **go** — reuse the provisioning render path (task 4) to run the `secrets` role against a running VM via `limactl shell`, with vars over stdin.

## Acceptance Criteria
- [ ] `go build ./...` and relevant `go test` pass.
- [ ] `sand secret sync --vm test` runs the `secrets` role against the running VM using the current host store, with secret values passed over stdin (never argv).
- [ ] After changing a GitHub token via `sand secret set ... --github` then `sand secret sync`, the next `git`/`gh` call in an already-open shell uses the new token (validated end-to-end in task 7).
- [ ] `sync` prints an honest effect summary: git/GitHub changes are effective immediately; global/directory env-var changes require a new shell. It does **not** claim a running process picks up new env vars.
- [ ] `sync` does not force a VM or shell restart.

## Technical Requirements
- Reuse the store→Ansible-vars mapping and the stdin var-passing from task 4; do not duplicate the credential-rendering logic.
- Target the running VM via the same `limactl shell` provisioning mechanism used elsewhere in `internal/provision`.
- Output must distinguish live (git/GitHub) from deferred (env-var) effects.

## Input Dependencies
- Task 1: `internal/secrets` store package.
- Task 4: the store→Ansible-vars mapping and the render-over-stdin provisioning path.

## Output Artifacts
- `sand secret sync` subcommand.
- A reusable "render secrets into running VM" entry point (shared with the TUI refresh action in task 6).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

Factor the "load store → map to `secrets_*` vars → run `secrets` role over stdin against `<vm>`" logic (introduced in task 4) into a function both `create/reset` and `sync` call, so `sync` is a thin command that runs only the `secrets` role (not a full finalize) against the running instance.

Effect reporting: after a successful render, print something like:
- `GitHub tokens updated — effective immediately (next git/gh call).`
- `Global/directory environment variables updated — open a new shell for them to take effect. Already-running processes (e.g. a running claude) keep the old values until restarted.`

Only print the lines relevant to what actually changed if that's cheap to determine; otherwise print both categories. Keep it accurate — the honesty about env vars is a stated success criterion. No forced restart. No secret values in output or logs.
</details>
