---
id: 5
group: "ci-migration-and-removal"
dependencies: [2, 3]
status: "completed"
created: 2026-07-06
model: "sonnet"
effort: "high"
skills:
  - github-actions
complexity_score: 6
complexity_notes: "The lima-e2e job is the project's only real end-to-end gate; migrating it off new-vm.sh must preserve the exact guest assertions (apt-keyring readability, toolchain smoke test) or coverage is silently lost. Risk-floor: a verification gate gets careful treatment."
---
# Migrate CI `lima-e2e` to build `sand` from the checkout

## Objective
Prove the `sand` binary covers what `new-vm.sh` did, in CI, before the script is
deleted. Repoint the `lima-e2e` job in `.github/workflows/test.yml` to **build
`sand` from the checkout** (`go build -o sand ./cmd/sand`) and provision with
`./sand create --yes …` instead of `./scripts/new-vm.sh --yes …`, keeping the
existing apt-keyring and toolchain assertions and the on-failure log tail. Also
update the `lint` job so it no longer shellchecks the soon-to-be-deleted scripts
(while still running the ansible syntax check). This job builds from the checkout
and resolution is working-tree-first, so every PR e2e-tests its own playbook/role
edits.

## Skills Required
- **github-actions** — editing a workflow job's steps, Go build in CI, and
  preserving the guest-side assertions/log-tail exactly.

## Acceptance Criteria
- [ ] `lima-e2e` builds the binary from the checkout (`go build -o sand ./cmd/sand`,
      after an `actions/setup-go@v5` step) and provisions with
      `./sand create --yes --name claude-ci --git-name "CI Bot"
      --git-email ci@example.com --cpus 2 --memory 6GiB --disk 30GiB` — no
      `new-vm.sh` invocation remains in the job.
- [ ] The "apt keyrings readable by `_apt`" assertion, the "apt update verifies
      signatures cleanly" step, and the toolchain smoke test
      (`docker --version && gh --version && node --version`) are retained
      unchanged.
- [ ] The on-failure log tail still tails `/var/log/sand-provision.log` and
      `/var/log/sand-finalize.log`.
- [ ] The `lint` job no longer references `install.sh` or `scripts/new-vm.sh`
      (the `shellcheck install.sh scripts/new-vm.sh` step is removed or replaced),
      and it still runs `ansible-playbook … --syntax-check site.yml`.
- [ ] `grep -n "new-vm.sh" .github/workflows/test.yml` returns nothing.
- [ ] The workflow remains valid YAML and its trigger/permissions/concurrency
      blocks are unchanged (only the `lint` and `lima-e2e` steps are edited — the
      go-test/coverage job is Plan 08's, not this task's).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Target file: `.github/workflows/test.yml`. Two jobs today: `lint` (shellcheck
  + ansible syntax check) and `lima-e2e` (runs `./scripts/new-vm.sh --yes …` on a
  Lima VM via `lima-vm/lima-actions/setup@v1`, then asserts).
- The provisioning log paths were renamed to `sand-{provision,finalize}.log`
  (Plan 06); keep those paths in the failure-dump step.
- After task 1 the build command is `go build -o sand ./cmd/sand` from the repo
  root; add a Go setup step (`actions/setup-go@v5` with `go-version-file: go.mod`)
  before it. The build must happen before the Lima provisioning step.
- `sand create`'s flag surface (task 3) matches the `new-vm.sh` invocation the
  job used, so the arguments map directly (drop nothing except any `--ref`,
  which the job never passed).
- Do **not** add a `go test ./...` / coverage job — the plan explicitly assigns
  that to Plan 08, which lands after this plan and edits the workflow once at the
  repo-root path. Editing only `lint` and `lima-e2e` keeps this plan's and Plan
  08's edits sequenced, not simultaneous.
- Do not delete `new-vm.sh`/`install.sh` here — that is task 6, gated on this job
  going green.

## Input Dependencies
- **Task 2** (embedded playbook / working-tree-first resolution) — the migrated
  job relies on `sand` resolving the checkout's working tree so PR playbook edits
  are exercised.
- **Task 3** (`sand create`) — required: the job invokes `./sand create --yes …`.

## Output Artifacts
- A `lima-e2e` job that exercises the real `sand` entrypoint (build-from-checkout)
  and a `lint` job free of the doomed scripts. A green run here is the gate that
  authorises the script removal (task 6).

## Implementation Notes
This job is the repository's only real end-to-end coverage of the Lima
orchestration; migrate it surgically. Preserve the guest-side assertions verbatim
— they exist to catch a specific historical bug (the apt-keyring umask
regression), so losing them silently regresses the safety net. Test *your*
change by reading the diff: the only substantive change is the provisioning
mechanism (script → built binary) plus a Go setup/build step; the assertions are
untouched.

<details>
<summary>Step-by-step</summary>

1. In `.github/workflows/test.yml`, `lint` job: remove
   `shellcheck install.sh scripts/new-vm.sh` (delete the whole "Shellcheck…"
   step, since both files are being deleted). Keep the "Ansible syntax check"
   step exactly as-is.
2. `lima-e2e` job: before the provisioning step, add:
   ```yaml
   - uses: actions/setup-go@v5
     with:
       go-version-file: go.mod
   - name: Build sand from the checkout
     run: go build -o sand ./cmd/sand
   ```
   Place these after checkout (and they can run before or after the Lima setup;
   simplest is right after "Set up Lima").
3. Replace the "Provision a VM with new-vm.sh" step with:
   ```yaml
   - name: Provision a VM with sand (built from the checkout)
     run: |
       ./sand create --yes \
         --name claude-ci \
         --git-name "CI Bot" --git-email ci@example.com \
         --cpus 2 --memory 6GiB --disk 30GiB
   ```
4. Leave the three assertion steps (keyring readability, apt-update verify,
   toolchain smoke test) and the on-failure log-tail step untouched — the log
   paths already say `sand-provision.log` / `sand-finalize.log`.
5. Sanity-check: `grep -n "new-vm.sh" .github/workflows/test.yml` → empty; the
   file is still valid YAML (e.g. `yamllint` or a quick parse). Do not touch the
   `on:`, `permissions:`, or `concurrency:` blocks.
6. Do not add a coverage/go-test job (Plan 08 owns it).
</details>
