---
id: 4
group: "ci"
dependencies: [2, 3]
status: "pending"
created: 2026-11-05
models:
  anthropic: "claude-sonnet-5"
  openai: "gpt-6-sol"
effort: "medium"
skills:
  - github-actions
  - bash
---
# lima-e2e assertion that the recorded ddev ports reach the guest

## Objective
Add one step to the `lima-e2e` job that proves the VM's recorded slot reached the guest: its `~/.ddev/global_config.yaml` and the `sandbar-environment` skill's `this-vm.md` carry the slot's ports.

## Skills Required
GitHub Actions workflow YAML; bash/jq.

## Acceptance Criteria
- [ ] A new step in `.github/workflows/test.yml`'s `lima-e2e` job, after the "Create a VM" step, reads the slot from the VM's provenance marker on the runner (`${LIMA_HOME:-$HOME/.lima}/claude-ci/sandbar.json`, `.config.DDEVPortSlot`), fails if it is absent, and computes the expected ports (base `20000 + 10*slot`, offsets +0..+6).
- [ ] It asserts via `limactl shell claude-ci` that `~/.ddev/global_config.yaml` contains `router_http_port: <+0>`, `router_https_port: <+1>`, `mailpit_http_port: <+2>`, `mailpit_https_port: <+3>`, `xhgui_http_port: <+4>`, `xhgui_https_port: <+5>`, `traefik_monitor_port: <+6>`, and that `~/.agents/skills/sandbar-environment/references/this-vm.md` mentions the router HTTPS port.
- [ ] It asserts `cold.log` (the create step's tee) contains the router HTTPS port, proving the CLI printed it.
- [ ] The step uses `set -euo pipefail`, prints `::error::` lines naming the missing value on failure, and starts neither Docker nor a ddev project.
- [ ] YAML is valid: `python3 -c 'import yaml,sys; yaml.safe_load(open(".github/workflows/test.yml"))'` exits 0; if `actionlint` is available, it reports no new issues.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Follow the existing step style in `lima-e2e` (e.g. "Assert systemd linger is enabled for the VM user": `limactl shell claude-ci bash -lc '…'`).
- Marker JSON shape: `{"schema":…, "config": {…, "DDEVPortSlot": <int>}, …}` (task 1 adds `DDEVPortSlot` with `json:",omitempty"`). Confirm the exact key by reading `internal/vm/vm.go` after task 1.
- Confirm the exact CLI port line format by reading task 3's output code in `cmd/sandbar`.
- `jq` is already used in this workflow.

## Input Dependencies
Task 2 (guest role + this-vm.md template), task 3 (slot recorded at create; CLI port line).

## Output Artifacts
Updated `.github/workflows/test.yml`.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- Do not dispatch the workflow from this task; the orchestrator's self-validation does that. Keep the step cheap (a few seconds).
- Place the step near the other post-create guest assertions (after "Assert systemd linger…" is fine), with a short comment: the assertion reaches the guest because a passing unit test only proves the vars were rendered, not that they landed (AGENTS.md "An assertion must reach the boundary the user cares about").
- Use `grep -qx` on exact lines in `global_config.yaml` (lineinfile writes `key: value` lines). Loop over key/port pairs to keep it compact.
- The marker is on the runner's own filesystem for local Lima: find `LIMA_HOME` the way other steps do (check the workflow for an existing `LIMA_HOME`/`~/.lima` reference) and default to `$HOME/.lima`.
</details>
