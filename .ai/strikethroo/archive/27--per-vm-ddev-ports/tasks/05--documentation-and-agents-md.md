---
id: 5
group: "docs"
dependencies: [1, 2, 3]
status: "completed"
created: 2026-11-05
models:
  anthropic: "claude-sonnet-5"
  openai: "gpt-6-sol"
effort: "medium"
skills:
  - technical-writing
  - mkdocs
---
# Document per-VM ddev ports (web-servers.md, CLI reference, AGENTS.md)

## Objective
Update the user docs and AGENTS.md so users know how ddev URLs work on sandbar VMs and future agents keep the design's invariants.

## Skills Required
Technical writing; MkDocs Material (strict build).

## Acceptance Criteria
- [ ] `docs/using-sand/web-servers.md` replaces the macOS "bare URL just works" text and the Linux privileged-port workaround with: the per-VM port scheme (slot of seven ports in 20000–29999, router HTTPS = base+1, etc.); how to find a VM's ports (CLI create/reset output; the in-guest `sandbar-environment` skill's `this-vm.md`; `ddev describe`); that VMs created before this change keep 80/443 until their next reset; that a reset replaces hand-set global ddev ports; that a project's `.ddev/config.yaml` port settings override the global ones; the fixed range and the host-service-collision caveat; that several users or Lima homes on one host share loopback without seeing each other's slots; an updated remote-Lima `ssh -L` example using the VM's HTTPS port; Proxmox unchanged; no opt-out, so host-native ddev keeps 80/443.
- [ ] `docs/using-sand/cli-reference.md` mentions the ddev port line printed by `sandbar create`/`sandbar reset`, if that page documents create/reset output (otherwise leave it and say so in your report).
- [ ] `AGENTS.md` gains a short section recording the invariants: ports changed in ddev's global config file, not via Lima remapping (one URL valid inside and outside the guest; fixes remote Lima and Linux privileged ports); the file is edited per key because `ddev config global` has no XHGui flags; the finalize role is gated on the port vars because `toolset_ddev` is base-only, and lives outside `dev-tools` because that role is skipped in finalize; Lima-only and `WithDDEV`-only; `DDEVPortSlot` on `vm.CreateConfig` with nil = unrecorded, carried explicitly by every reset path (`adoptRecordedConfig`, TUI reset replay); allocation in `manage.AssignDDEVSlot`, pure, excludes the VM itself; per-VM vars never enter the base phase/stamp; no opt-out by design.
- [ ] Wording describes the implemented behaviour (read the code from tasks 1–3 and the role from task 2), uses `sandbar` as the command name, and no nautical metaphor.
- [ ] `uvx --with-requirements docs/requirements.txt mkdocs build --strict` passes.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Files: `docs/using-sand/web-servers.md`, `docs/using-sand/cli-reference.md` (conditional), `AGENTS.md`.
- Read the final code: `internal/vm/ddevports.go`, `internal/manage` allocator, `internal/provision/vars.go`, `roles/ddev-ports`, `roles/agent-context/...`, CLI output in `cmd/sandbar`.

## Input Dependencies
Tasks 1, 2, 3 (implemented behaviour and names).

## Output Artifacts
Updated docs and AGENTS.md.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- Keep AGENTS.md's style: dense bullets where the reason is the load-bearing half. Put the new section near the provisioner/reset sections (e.g. after "What a reset preserves") titled like "ddev ports (read before touching …)".
- In `web-servers.md`, keep Lullabot's voice: plain, direct, human. Show a worked example: slot 12 → `https://<project>.ddev.site:20121`.
- Do not document internal Go names in user docs; do name them in AGENTS.md.
- Verify links resolve with the strict mkdocs build.
</details>
