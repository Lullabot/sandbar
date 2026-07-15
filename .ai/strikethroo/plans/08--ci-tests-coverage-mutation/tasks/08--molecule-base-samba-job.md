---
id: 8
group: "ci-wiring"
dependencies: []
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "high"
skills:
  - github-actions
  - ansible-molecule
---
# CI: scheduled molecule converge/verify for the base + samba roles

## Objective
Add molecule scenarios for the `base` and `samba` Ansible roles (converge + idempotence re-run + verify) and run them in a scheduled + `workflow_dispatch` Docker job using systemd-capable images, while keeping the existing `--syntax-check`. CI + Ansible test assets only — no production Go code.

## Skills Required
`github-actions`, `ansible-molecule` (scenario authoring, systemd-in-Docker, verify assertions).

## Acceptance Criteria
- [ ] Molecule scenarios for `base` and `samba` exist (converge with an idempotence re-run, plus a `verify`).
- [ ] `verify` asserts concrete outcomes: for `samba`, the share is configured and `smbd` is enabled/active; for `base`, its foundational setup lands.
- [ ] A `molecule` CI job runs on `schedule` (weekly) + `workflow_dispatch` only, with Docker and a systemd-enabled Debian image; the existing `lint` syntax-check is retained.
- [ ] The other roles (`dev-tools`, `claude-code`, `project`, `user`) are documented as follow-up, not built.
- [ ] Verification: `molecule test -s base` and `molecule test -s samba` converge idempotently and pass verify locally (or via `workflow_dispatch`); `actionlint` shows the job parses and is not PR-triggered.

## Technical Requirements
- Molecule + Docker driver; a systemd-enabled Debian image matching the project's Debian target so services actually start.
- Weekly cron + `workflow_dispatch`, gated with `if: github.event_name == 'schedule' || github.event_name == 'workflow_dispatch'`.

## Input Dependencies
None.

## Output Artifacts
`molecule/` scenarios for `base` + `samba`; a `molecule` job in `test.yml`; a follow-up note listing deferred roles (feeds task 9's docs).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- Use a systemd-capable Debian image (e.g. `geerlingguy/docker-debian12-ansible` or an equivalent with `/sbin/init`, `privileged: true`, and the cgroup mounts molecule needs) so `smbd`/systemd units start under converge.
- `verify` with Ansible assertions (`ansible.builtin.assert`, `service_facts`, a `stat` of the samba config/share) rather than shelling out ad hoc.
- Keep the scenario minimal — `base` + `samba` only. Document the deferred roles in the role/test docs (task 9 links them).
- This is the one task that touches Ansible role assets; it does not touch the Go module.
</details>
