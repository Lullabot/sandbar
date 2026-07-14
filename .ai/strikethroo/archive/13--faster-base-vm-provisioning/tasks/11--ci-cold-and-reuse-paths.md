---
id: 11
group: "validation"
dependencies: [8, 5]
status: "completed"
created: 2026-07-13
model: "sonnet"
effort: "high"
skills:
  - github-actions
---
# CI: exercise both the cold `--rebuild` build and the base-reuse path

## Objective

Keep the cold build honest and start covering the path this plan makes the new default. CI runs a plain `sand create` on an ephemeral runner today — it never passes `--rebuild`, and with base reuse becoming the default it would never touch the in-place re-apply path at all.

## Skills Required

- **github-actions** — `.github/workflows/test.yml`, the `lima-e2e` job.

## Acceptance Criteria

- [x] The `lima-e2e` job passes `--rebuild` explicitly on its first create, so the cold path is exercised **by intent**, not merely because the runner happens to be ephemeral.
- [x] The job performs a **second create** that exercises base reuse / in-place re-apply (i.e. with the base already built), and asserts the base was **not** rebuilt from scratch — no Debian image download, no base deletion. (Workflow authored and strings verified against source; actual pass/fail of the assertion in a live run is unverified — see report.)
- [x] The existing `_apt`-readable keyring assertion is retained and passes against the **consolidated** apt path from task 3. (Retained unchanged, runs against the cold-built VM before it is deleted; live pass unverified — no KVM runner available locally.)
- [x] The existing `Linger=yes` assertion is retained (main's persistent-shell feature depends on it).
- [x] The existing `docker` / `gh` / `node --version` assertions are retained.
- [ ] The job still fits its disk budget (cloning doubles the qcow2 footprint; the job already frees runner disk). Confirm it does not run out of space with the second create. (Design keeps peak footprint at base + 1 clone, same as before, by deleting the cold clone before the second create — but this is unverified without an actual CI run.)
- [x] Dead step cleaned up: the workflow dumps `/var/log/sand-provision.log` and `/var/log/sand-finalize.log`, which **nothing in the codebase ever writes** — it is a `|| true` no-op. Remove it or point it at the real output.
- [ ] The workflow is valid: the job runs green end to end. (Cannot run locally — the `lima-e2e` job needs a KVM-enabled GitHub runner. YAML parses and referenced strings are verified against source; live green run is unverified until CI executes it.)

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- `.github/workflows/test.yml`: `lima-e2e` builds `sand` from the checkout, then runs (~:99-104):
  `./sand create --name claude-ci --git-name "CI Bot" --git-email ci@example.com --cpus 2 --memory 6GiB --disk 30GiB`
- Existing assertions: every `/etc/apt/keyrings/*` readable by `_apt` via `sudo -u _apt test -r` (~:111-124); `apt-get update` succeeds (~:126-127); `loginctl … Linger=yes` (~:132-137); `docker`/`gh`/`node --version` (~:139-146).
- Other jobs: `lint` runs `ansible-playbook --syntax-check`; `unit` runs `go vet` + `go test ./...`. The `limae2e`-tagged Go tests are **not** run in CI (they need `-tags limae2e` + `LIMA_E2E=1`).
- The runner is KVM-enabled with a Lima cache.

## Input Dependencies

- Task 8: the base lifecycle (re-apply, refresh) is settled, so the reuse path is real and testable.
- Task 5: the tool-set exists, so its effect on the base build is what CI is building.

## Output Artifacts

- A `lima-e2e` job covering both the cold `--rebuild` build and the reuse/re-apply path.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**1. Make the cold path explicit.**

```yaml
- name: Create a VM (cold — force a true from-scratch base build)
  run: |
    ./sand create --rebuild --name claude-ci \
      --git-name "CI Bot" --git-email ci@example.com \
      --cpus 2 --memory 6GiB --disk 30GiB
```

**2. Add the reuse create.** The base now exists, so a second create must reuse it. Prove it did:

```yaml
- name: Create a second VM (warm — must REUSE the base, not rebuild it)
  run: |
    ./sand create --name claude-ci-warm \
      --git-name "CI Bot" --git-email ci@example.com \
      --cpus 2 --memory 6GiB --disk 30GiB 2>&1 | tee warm.log

    # The base must not be rebuilt from scratch on the warm path.
    if grep -qiE 'Downloading the base image|downloading.*debian.*qcow2' warm.log; then
      echo "FAIL: the warm create re-downloaded the Debian image — the base was rebuilt"
      exit 1
    fi
```

Match on whatever string the provisioner actually prints for a cold build — read `provision.go`'s `step()` banners and grep for the real text rather than guessing. If a dirty-tree edit is needed to trigger the in-place re-apply specifically, `touch` a playbook file before the second create and assert the output shows a **re-apply**, not a rebuild.

**3. Watch the disk budget.** A second VM means a second qcow2. The job already frees runner disk; if the warm create runs out of space, either delete the first VM before creating the second (`./sand delete claude-ci` — but that defeats nothing, the *base* is what must survive) or shrink the disks. **The base must survive between the two creates** — that is the entire point.

**4. Keep every existing assertion.** Do not drop the `_apt` keyring loop — it is the acceptance test for task 3's apt consolidation, and it guards a regression the project has already been bitten by. Run it against the VM built by the **cold** create.

**5. Remove the dead log-dump step.** Nothing writes `/var/log/sand-provision.log` or `/var/log/sand-finalize.log` (grep confirms zero hits). It is `|| true`, so it silently prints nothing. Delete it, or repoint it at output that actually exists.

</details>
