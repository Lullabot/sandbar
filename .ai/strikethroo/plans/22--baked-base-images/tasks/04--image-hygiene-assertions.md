---
id: 4
group: "image-build"
dependencies: [3]
status: "pending"
created: 2026-09-12
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Security verification gate over a publicly distributed artifact; per the rubric's risk floor this never goes below sonnet + high."
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

- [ ] A committed script (suggested: `scripts/check-base-image.sh`) takes an image path, mounts it read-only, and asserts every hygiene property, exiting non-zero with a specific message naming the first failure.
- [ ] Asserted: the sandbar user's password is **locked** (`passwd -S <user>` reports `L`, not `P`).
- [ ] Asserted: no `/etc/ssh/ssh_host_*` files exist.
- [ ] Asserted: `/etc/machine-id` is zero-length, and `/var/lib/dbus/machine-id` is a symlink to it.
- [ ] Asserted: `/var/lib/apt/lists/` contains no package lists and `/var/cache/apt/archives/` contains no `.deb` files.
- [ ] Asserted: no shell history files (`/root/.bash_history`, `/home/*/.bash_history`).
- [ ] Asserted: dpkg's `force-unsafe-io` build hack is **not** present in `/etc/dpkg/dpkg.cfg.d/`.
- [ ] Asserted: no file under `/home` or `/root` matches obvious credential shapes — at minimum check that no `.env`, `*.pem`, `id_rsa`/`id_ed25519` private key, or `~/.claude/.credentials.json`-style file is present.
- [ ] Verification: run the script against a **deliberately dirtied** copy of a built image (e.g. `touch /etc/ssh/ssh_host_rsa_key` in the mounted root) and confirm it exits non-zero naming that specific failure. Paste the output. A gate that has never been seen to fail is not known to work.
- [ ] Verification: run it against the real built image from task 03 and confirm it exits 0. Paste the output.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Mount read-only where possible to avoid mutating the artifact being checked.
- Use the same `qemu-nbd` + `blkid` + mount approach as task 03; factor the mount/teardown into a shared helper if that avoids duplicating the retry loop, but do not let the checker depend on the builder having run in the same process.
- Every assertion must produce a distinct, greppable failure message — a single "image is bad" is useless in CI logs.
- The script must be safe to run repeatedly and must clean up its nbd connection via `trap` on any exit path.

## Input Dependencies

- Task 03's `scripts/build-base-image.sh` and a built image to check against.

## Output Artifacts

- `scripts/check-base-image.sh` — consumed by task 05 (the workflow runs it before publishing) and by task 13 (CI wiring).

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

**Why this task exists separately from the build.** The build script's job is to produce the right image; this script's job is to disbelieve it. Keeping them separate means the assertions can be run against *any* image — including one built months ago, or one a contributor produced locally — and means a bug in the build's generalization step cannot also silently disable its own check.

**The specific risks being guarded, from the plan:**

1. **Shared user password.** `roles/user/tasks/main.yml:6-19` generates a 24-character random password and sets it on the user. In a locally built base that is harmless. Baked into a published image it becomes one password shared by every sandbar user in the world, on a machine with passwordless `sudo`. Check with `chroot <root> passwd -S <user>` and require the status field to be `L` (locked) or `NP`. A `P` is a hard failure.
2. **Shared SSH host keys.** If `/etc/ssh/ssh_host_ed25519_key` ships in the image, every VM created from it presents the same host identity, so host-key verification protects nobody and a MITM is undetectable. Require the glob to match nothing.
3. **Shared machine-id.** Already a known, fixed bug class in this repo — cloned machine-ids made `systemd-networkd` hand every clone the same DHCP lease (`internal/provider/proxmoxprovision.go:437-457`). Require zero length.
4. **Build residue.** APT lists and caches bloat the artifact and leak what was installed when; shell history can contain anything the build typed; a stray credential file would be the worst case.

**Determining the username.** Do not hardcode it if the playbook derives it. Read it from the image — e.g. the last entry in `/etc/passwd` with a `/home` directory and UID >= 1000 — or accept it as a parameter with a sensible default matching `user_name`'s default in the role defaults.

**Testing the gate.** The acceptance criteria require proving the check *fails* on a bad image, not just that it passes on a good one. Make a copy, dirty it, run the check, observe the failure, and paste it. This is the single most important verification in this task — an assertion script that silently passes everything is worse than no script, because it manufactures confidence.

**Keep it fast.** This runs in CI on every image build. Mounting and running a few dozen `test` calls is cheap; avoid anything that walks the entire filesystem more than once (combine the credential-shape searches into a single `find` with multiple `-name` predicates).

</details>
