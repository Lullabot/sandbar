---
id: 4
group: "image-build"
dependencies: [15]
status: "completed"
created: 2026-09-12
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
complexity_score: 7
complexity_notes: "Security verification gate over a publicly distributed artifact; the rubric's risk floor requires capable models and high effort."
skills:
  - shell
  - security-verification
---
# Assert the built image is safe to distribute

## Objective

Write an automated check that inspects a built image and fails if it carries anything that must not be shipped to the public: a set user password, SSH host keys, a non-empty machine-id, or build residue. This is the gate that keeps a catastrophic, easy-to-miss regression out of a widely downloaded artifact.

## Skills Required

`shell` for the offline-root inspection; `security-verification` for knowing what a distributable image must not contain and asserting it without false confidence.

## Acceptance Criteria

- [x] A committed script (suggested: `scripts/check-base-image.sh`) takes an image path, mounts it read-only, and asserts every hygiene property, exiting non-zero with a specific message naming the first failure.
- [x] Asserted: the sandbar user cannot authenticate with a password (`passwd -S <user>` reports `L` or `NP`, never `P`).
- [x] Asserted: no `/etc/ssh/ssh_host_*` files exist.
- [x] Asserted: `/etc/machine-id` is zero-length. If `/var/lib/dbus/machine-id` exists, it is a symlink to `/etc/machine-id`; absence is valid when the image does not install `dbus`.
- [x] Asserted: `/var/lib/apt/lists/` contains no package lists and `/var/cache/apt/archives/` contains no `.deb` files.
- [x] Asserted: no shell history files (`/root/.bash_history`, `/home/*/.bash_history`).
- [x] Asserted: dpkg's `force-unsafe-io` build hack is **not** present in `/etc/dpkg/dpkg.cfg.d/`.
- [x] Asserted: no file under `/home` or `/root` matches obvious credential shapes — at minimum check that no `.env`, `*.pem`, `id_rsa`/`id_ed25519` private key, or `~/.claude/.credentials.json`-style file is present.
- [x] Verification: run the script against a **deliberately dirtied** copy of a built image (e.g. `touch /etc/ssh/ssh_host_rsa_key` in the mounted root) and confirm it exits non-zero naming that specific failure. Paste the output. A gate that has never been seen to fail is not known to work.
- [x] Verification: run it against the real built image from task 03 and confirm it exits 0. Paste the output.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Mount read-only where possible to avoid mutating the artifact being checked.
- Use the same `qemu-nbd` + `blkid` + mount approach as task 03; factor the mount/teardown into a shared helper if that avoids duplicating the retry loop, but do not let the checker depend on the builder having run in the same process.
- Every assertion must produce a distinct, greppable failure message — a single "image is bad" is useless in CI logs.
- The script must be safe to run repeatedly and must clean up its nbd connection via `trap` on any exit path.

## Input Dependencies

- Task 03's `scripts/build-base-image.sh` and task 15's final size-optimized image to check against.

## Output Artifacts

- `scripts/check-base-image.sh` — consumed by task 05 (the workflow runs it before publishing) and by task 13 (CI wiring).

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

**Why this task exists separately from the build.** The build script's job is to produce the right image; this script's job is to disbelieve it. Keeping them separate means the assertions can be run against *any* image — including one built months ago, or one a contributor produced locally — and means a bug in the build's generalization step cannot also silently disable its own check.

**The specific risks being guarded, from the plan:**

1. **Shared user password.** `roles/user/tasks/main.yml:6-19` generates a 24-character random password and sets it on the user. In a locally built base that is harmless. Baked into a published image it becomes one password shared by every sandbar user in the world, on a machine with passwordless `sudo`. Check with `chroot <root> passwd -S <user>` and require the status field to be `L` (locked) or `NP`. A `P` is a hard failure.
2. **Shared SSH host keys.** If `/etc/ssh/ssh_host_ed25519_key` ships in the image, every VM created from it presents the same host identity, so host-key verification protects nobody and a MITM is undetectable. Require the glob to match nothing.
3. **Shared machine-id.** Already a known, fixed bug class in this repo — cloned machine-ids made `systemd-networkd` hand every clone the same DHCP lease (`internal/provider/proxmoxprovision.go:437-457`). Require `/etc/machine-id` to be zero length. If `/var/lib/dbus/machine-id` exists, require it to link there; do not require the path to exist on an image without `dbus`.
4. **Build residue.** APT lists and caches bloat the artifact and leak what was installed when; shell history can contain anything the build typed; a stray credential file would be the worst case.

**Determining the username.** Do not hardcode it if the playbook derives it. Read it from the image — e.g. the last entry in `/etc/passwd` with a `/home` directory and UID >= 1000 — or accept it as a parameter with a sensible default matching `user_name`'s default in the role defaults.

**Testing the gate.** The acceptance criteria require proving the check *fails* on a bad image, not just that it passes on a good one. Make a copy, dirty it, run the check, observe the failure, and paste it. This is the single most important verification in this task — an assertion script that silently passes everything is worse than no script, because it manufactures confidence.

**Keep it fast.** This runs in CI on every image build. Mounting and running a few dozen `test` calls is cheap; avoid anything that walks the entire filesystem more than once (combine the credential-shape searches into a single `find` with multiple `-name` predicates).

</details>

## Execution Notes

- Wrote `tests/check_base_image_test.sh` first and ran it RED against the final image: it failed because `scripts/check-base-image.sh` did not exist. The checker then made the clean-image test GREEN. The test harness requires explicit clean and dirty image paths plus the expected failure text.
- `scripts/check-base-image.sh` checks a qcow2 image through `qemu-nbd --read-only` and an ext4 `ro,noload` mount. It discovers every non-system `/home` user from `/etc/passwd`, checks each with `passwd -S`, and emits a distinct first-failure message for passwords, host keys, machine IDs, APT residue, dpkg unsafe I/O, shell histories, and credential-shaped files. The home scan explicitly covers mkcert's `~/.local/share/mkcert/` CA and key material. An EXIT trap unmounts, disconnects NBD, and removes the temporary mount directory.
- Two concurrent checker invocations initially selected the same free NBD device; one mkcert check falsely passed. A root-owned `/run/sand-base-image-check.lock` flock now serializes checker instances. Running both acceptance cases concurrently after this fix produced the correct clean pass and specific dirty failures.
- Final-image check, exit 0:

  ```text
  check-base-image: PASS: /var/tmp/sand-base-task15-e.qcow2 is safe to distribute (passwords locked; no host keys, machine identity, build residue, or credential-shaped files)
  /dev/nbd0 disconnected
  ```

- A disposable qcow2 overlay of the final image was mounted writable only for injection of `/etc/ssh/ssh_host_rsa_key`. Checker output, exit 1:

  ```text
  check-base-image: SSH host key remains: /etc/ssh/ssh_host_rsa_key
  /dev/nbd0 disconnected
  ```

- A second disposable overlay carried `/home/claude/.local/share/mkcert/rootCA-key.pem`. Checker output, exit 1:

  ```text
  check-base-image: mkcert CA material remains: /home/claude/.local/share/mkcert/rootCA-key.pem
  /dev/nbd0 disconnected
  ```

- `bash -n scripts/check-base-image.sh tests/check_base_image_test.sh` and `git diff --check` passed. The final image still hashes to `13b59e82d4b65943ffe2e29f3602e959cb9a4bc6571f6dc0d5844c743576e4b6`. No `sand-image-check` or `sand-task04` mount directories, mounts, or connected NBD devices remained after testing. The phase-boundary commit is owned by the blueprint executor.
- Independent phase verification ran the SSH-key and mkcert-key harnesses concurrently. Both serialized clean-image checks passed; the dirty overlays exited nonzero with their exact injected paths; the final image hash remained unchanged; and no NBD device, mount, or checker work directory remained.
