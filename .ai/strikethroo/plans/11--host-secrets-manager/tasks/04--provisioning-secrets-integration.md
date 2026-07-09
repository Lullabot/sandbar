---
id: 4
group: "provisioning"
dependencies: [1, 2]
status: "pending"
created: 2026-07-09
model: "sonnet"
effort: "high"
skills:
  - go
  - ansible
---
# Provisioning Integration — Render Host Secrets on Create/Reset + Reshape `--clone-token`

## Objective
Make the host secrets store authoritative during provisioning: on every `create` and `Reset`, read the VM's store and feed it to the `secrets` role over stdin (never argv), so recreating a VM re-materialises all secrets without regeneration. Reshape the existing `--clone-token`/`--clone-url` flow to record the clone token as a directory-scoped GitHub secret rather than the old direnv `.env`/`GH_TOKEN` path.

## Skills Required
- **go** — `internal/provision` (and `cmd/sand/create.go`) wiring: map the store JSON to Ansible vars, pass over stdin.
- **ansible** — invoke the `secrets` role with the mapped vars in the finalize/full phase.

## Acceptance Criteria
- [ ] `go build ./...` and `go test ./internal/provision/... ./cmd/sand/...` pass.
- [ ] A unit/integration test asserts the store JSON is correctly mapped to the `secrets_global` / `secrets_github` / `secrets_dir_env` Ansible vars.
- [ ] Secrets are passed to Ansible over **stdin only** (assert no secret value appears in any argv/command line the provisioner builds — extend or mirror the existing stdin-vars test).
- [ ] `sand create --clone-url <private-repo> --clone-token <T>` records `{scope: "github.com/<org>", token: T}` in the host store and provisions a working private clone (validated end-to-end in task 7).
- [ ] `Reset`/recreate of a VM re-renders secrets from the host store (the old VM-side `.env` staging is no longer the source of truth for secrets); `Reset --preserve-project` still preserves the checked-out tree.

## Technical Requirements
- Map the host JSON (task 1 schema) to the Ansible var contract (task 2): `global→secrets_global`, `github→secrets_github`, `dir_env→secrets_dir_env`.
- Continue passing vars to Ansible over stdin, as `internal/provision/provision.go` already does for existing vars — secret hygiene must not regress.
- Run the `secrets` role in the finalize/full phase (not base) so the base image stays secret-free.
- The `--clone-token` reshape must produce the same observable outcome (working private clone + working `git` auth) via the new store + `secrets` role, with no parallel direnv/`GH_TOKEN` codepath.

## Input Dependencies
- Task 1: `internal/secrets` store package.
- Task 2: the `secrets` role and its var contract.

## Output Artifacts
- Provisioning that renders host secrets on create and reset.
- `--clone-token` reshaped onto the store (old per-org `.env`/`direnv allow`/`GH_TOKEN`-in-clone path in `roles/project` retired or redirected).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

In `internal/provision`, where create/finalize and `Reset` assemble Ansible vars and stream them over stdin, add a step that loads `secrets.Load(cfg.Name)` and serializes the three lists into the `secrets_*` vars alongside the existing ones. Keep them out of argv — extend the existing stdin var-encoding path (see the "Vars go over STDIN, never argv (secret hygiene)" comment).

`--clone-token` reshape: in `cmd/sand/create.go`, when `--clone-url` is a `github.com` URL and `--clone-token` is set, derive the org scope from the URL (reuse `cloneOrgRelDir`/the URL-parsing already in `provision.go`/`roles/project`) and write a `github` secret `{scope: "github.com/<org>", token}` into the store before provisioning. The `secrets` role then renders the credential; the `project` role's clone step should authenticate via that credential store rather than a `GH_TOKEN` env passed to `git`. Remove (or redirect) the `roles/project` tasks that write the per-org `.env`, run `direnv allow`, and pass `GH_TOKEN` in the clone environment — that responsibility now belongs to the `secrets` role. Ensure the clone still works for private repos (the credential store must be rendered before the clone runs).

For `Reset`: since the host store is authoritative, secrets no longer need to be staged out of the VM. Keep `PreserveProject` staging the *tree* (checkout) but let the `secrets` role re-render credentials/env from the host store on finalize. Verify the existing preserve-project ordering still holds.

Do not log secret values anywhere in the provisioning path.
</details>
