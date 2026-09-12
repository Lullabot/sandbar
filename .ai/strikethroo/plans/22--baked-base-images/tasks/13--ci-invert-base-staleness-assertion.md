---
id: 13
group: "testing"
dependencies: [10, 11]
status: "pending"
created: 2026-09-12
model: "sonnet"
effort: "medium"
skills:
  - github-actions
  - testing
---
# Invert the CI base-staleness assertion and wire the hygiene gate

## Objective

Replace the `lima-e2e` job's warm-path assertion — which deliberately dirties a base role and asserts in-place convergence, a behaviour this plan deletes — with an assertion of the new contract, and make the image hygiene check run as a gate in CI.

## Skills Required

`github-actions` for the workflow changes; `testing` for designing assertions that would actually fail if the contract broke.

## Acceptance Criteria

- [ ] The assertion at `.github/workflows/test.yml:234-270` no longer dirties `roles/base/tasks/main.yml` to assert in-place re-application.
- [ ] It is replaced by an assertion of the new contract: after editing a file under `roles/base/`, a subsequent create performs **no** base rebuild and **no** image re-download.
- [ ] A complementary assertion confirms that changing the pinned image version **does** trigger a rebuild from the new image.
- [ ] `scripts/check-base-image.sh` runs as a gate wherever an image is produced, so a hygiene regression fails CI rather than shipping.
- [ ] The `lint` job's `ansible-playbook --syntax-check` still passes with the `sand_image_build` flag present.
- [ ] Verification: the full `test.yml` workflow runs green on a PR. Paste the run URL and the job summary.
- [ ] Verification: prove the new assertion can fail — temporarily make a create rebuild the base unconditionally, observe the job fail with a clear message, then revert. Paste the failing output. An assertion never seen to fail is not known to work.
- [ ] Verification: `grep -n "roles/base/tasks/main.yml" .github/workflows/test.yml` shows the old dirty-and-converge step is gone. Paste it.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- The `lima-e2e` job runs real Lima under QEMU+KVM on a hosted runner with a 60-minute timeout. A baked-image model changes its time profile: the base build disappears but an image download (~1.5 GiB) appears. Check the job still fits comfortably and adjust caching if not.
- The job already caches Lima image downloads and apt packages. Extend that cache to cover sand's own image cache so repeated CI runs do not re-download the image each time.
- Keep the existing cold-create assertions that remain valid: apt keyring readability, `apt update` signature verification, systemd linger, and the toolchain smoke test. The toolchain smoke test becomes *more* meaningful now, since it verifies the baked image actually contains the tools.
- `remote-lima-e2e` drives the remote provider over a loopback SSH hop. Confirm it still passes — the remote path's image handling (local cache path vs URL) is the part most likely to have been overlooked in task 08.
- Do not weaken assertions to make them pass. If the remote path is broken, that is a finding for task 08, not a reason to relax the test.

## Input Dependencies

- Task 10's simplified staleness logic (defines the new contract being asserted).
- Task 11's removed toolset surface (the job may reference `--with-*` flags that no longer exist).

## Output Artifacts

- Updated `.github/workflows/test.yml` — the CI encoding of success criterion 4.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

**Read the step being replaced first.** `.github/workflows/test.yml:234-270` is the single most load-bearing test of the *old* model. It creates a VM, edits `roles/base/tasks/main.yml`, creates a second VM, and asserts the base was converged in place rather than rebuilt from scratch. Under the new model that behaviour is gone entirely — so this is not a test to relax or delete quietly, it is a test whose assertion **inverts**. The old contract was "a playbook edit reaches existing bases cheaply"; the new contract is "a playbook edit does not touch the base at all, because the base is a pinned artifact".

**Shape of the replacement.** Two assertions, both cheap:

1. *Edit does not rebuild.* Create a VM. Touch `roles/base/tasks/main.yml`. Create a second VM. Assert the output shows the base being reused — no `limactl start` of a fresh base instance, no download line, and (most robustly) assert the base instance's creation timestamp or its stamped image version is unchanged.
2. *Version change does rebuild.* Override the pinned image version (an env var or a small test-only hook is cleaner than editing the generated file mid-job). Create a VM. Assert a rebuild occurred.

The second is the more valuable of the two, because a bug that makes sand *never* rebuild is far more dangerous than one that makes it rebuild too eagerly — users would silently run stale images forever, including past security updates.

**Proving the assertion can fail is an acceptance criterion, not optional.** The whole reason this task exists is that the previous assertion encoded a contract that silently stopped being true. Do not ship a replacement without watching it go red once.

**Watch the job's time budget.** The base build this job used to perform took real minutes; the image download replaces it. On a hosted runner with good bandwidth a ~1.5 GiB download is fast, but it is not free, and it happens on every run unless cached. Extend the existing cache (the job already caches Lima images and apt packages) to sand's image cache directory — task 07 documented where that is. Key the cache on the pinned image version so a bump invalidates it correctly.

**Flag findings rather than absorbing them.** If `remote-lima-e2e` fails because task 08 emitted a local cache path that does not exist on the remote host, that is a real bug in task 08 — record it and get it fixed rather than special-casing the test. Task 08's notes explicitly call this out as the fiddly case.

</details>
