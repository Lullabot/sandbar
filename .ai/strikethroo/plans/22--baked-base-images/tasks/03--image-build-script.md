---
id: 3
group: "image-build"
dependencies: [1]
status: "completed"
created: 2026-09-12
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
complexity_score: 8
complexity_notes: "Image plumbing over qemu-nbd and chroot, producing a publicly distributed artifact; correctness failures here are either silent (missing tool) or security-relevant (residual credential)."
skills:
  - shell
  - linux-image-plumbing
---
# Build the shared-dependency image in a chroot

## Objective

Create one parameterized script that turns an upstream Debian 13 genericcloud qcow2 into a complete, generalized, compressed sandbar base image by running the current base phase in a `chroot`. The image contains shared dependencies; coding agents remain per-VM finalize work.

## Skills Required

`shell` for the script itself; `linux-image-plumbing` for `qemu-nbd`, offline roots, `systemctl --root=`, generalization and qcow2 compression.

## Acceptance Criteria

- [x] A committed script (`scripts/build-base-image.sh`) accepts an architecture (`amd64` | `arm64`) and an output path, and produces a compressed qcow2.
- [x] The script runs the **full base phase**: `ansible-playbook -i localhost, --connection=local site.yml` with `provision_phase=base` and `sand_image_build=true`, inside the chroot, with network working.
- [x] Generalization is performed: user password locked, `/etc/ssh/ssh_host_*` removed, `/etc/machine-id` truncated with `/var/lib/dbus/machine-id` re-linked, APT lists and caches cleared, logs and shell history cleared, and dpkg's `force-unsafe-io` build hack restored to safe settings.
- [x] The image is sparsified and compressed, and the script **exits non-zero** if the result exceeds a configurable threshold defaulting conservatively below 2 GiB (suggested default: 1900 MiB).
- [x] The script prints the final size and SHA-256.
- [x] Verification: running `sudo ./scripts/build-base-image.sh --arch amd64 --out /var/tmp/sand-base-task03.qcow2` on a Linux amd64 host exits 0 and produces a file. The output and work paths are disk-backed; the printed size and SHA-256 are recorded below.
- [x] Verification: mount the image and confirm shared dependencies are present — Node, Docker, DDEV, Go, a JDK, glab, drupalorg, uv, mkcert, cloudflared, and self-review tooling — while Claude Code, Codex, OpenCode, and Pi binaries/state are absent.
- [x] Verification: the script is idempotent about cleanup — run it twice in a row and confirm the second run succeeds (no leftover `/dev/nbd0` connection or stale mount blocks it).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Reuse the proven scaffold from `.github/workflows/base-image.yml:72-116` verbatim where possible: `modprobe nbd max_part=16`, `qemu-nbd --connect`, `blkid -t TYPE=ext4` in a retry loop with `partprobe`/`udevadm settle`, mount, `policy-rc.d` returning 101, bind-mount `dev`/`proc`/`sys`, and the matching teardown.
- **Never boot the guest.** Cloud-init first-boot behaviour must remain untouched.
- **No libguestfs.** `virt-*` tools are broken on the 24.04 runner (Debian #1086844); that is why the nbd+chroot approach exists.
- Source URL must be arch-parameterized: `https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-{amd64,arm64}.qcow2`.
- `/etc/resolv.conf` must be usable inside the chroot so APT repos and vendor installers work.
- Ansible must be available to run inside the chroot — install `ansible-core` (and the playbook's bootstrap deps) into the image root first, matching what `overlayProvision` installs today (`internal/provision/overlay.go:51-101`): `ansible-core rsync curl gnupg ca-certificates python3-passlib`.
- Teardown must be robust: use a `trap` so a mid-script failure still unmounts and disconnects nbd, or the runner is left in a broken state and the second run fails.
- The script must run unprivileged-invoked-with-sudo on a plain Ubuntu runner with only `qemu-utils` installed.

## Input Dependencies

- Task 01's `sand_image_build` flag and the written chroot-compatibility audit — the audit specifies which offline equivalents the script must provide (notably which units it must enable with `systemctl --root=` from outside the chroot).

## Output Artifacts

- `scripts/build-base-image.sh` — consumed by task 05 (the workflow calls it) and by task 04 (which asserts properties of its output), and documented by task 14 as the local build facility.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

**Start from the existing workflow.** `.github/workflows/base-image.yml` already contains a working, commented version of every hard part. Read it first and lift the nbd/mount/chroot/teardown scaffold directly — including the comments explaining *why* each piece is shaped the way it is (the blkid retry loop exists because racing udev-populated `lsblk` was unreliable; `policy-rc.d` exists because postinsts would otherwise try to start daemons with no init).

**Recommended structure:**

1. Parse `--arch`, `--out`, optional `--max-size-mib` (default 1900), optional `--src-url` override for local testing.
2. Download the upstream image for the arch (skip if a cached copy is present and matches).
3. `qemu-img resize` the working copy larger before mounting — the stock genericcloud image is small and the tools will not fit. Give it room (e.g. 20 GiB, matching `vm.BaseDiskFloor`), then grow the filesystem after mounting (`resize2fs`). Get this right early; running out of space mid-playbook wastes a long build.
4. Connect nbd, find the root partition, mount at `/mnt/img`.
5. Prepare the chroot: `policy-rc.d`, bind-mount `dev`/`proc`/`sys`, ensure `/etc/resolv.conf`.
6. Install `ansible-core` and bootstrap deps into the root via `chroot ... apt-get install`.
7. Copy the playbook fileset into the root (e.g. `/root/playbook`) — the fileset is exactly `site.yml ansible.cfg inventory roles group_vars`, the same set pinned by `TestGuestSyncCopiesOnlyThePlaybook`. Keep it in sync with that invariant.
8. Run the playbook in the chroot with `provision_phase=base` and `sand_image_build=true`.
9. Enable any units the audit says must be enabled offline, via `systemctl --root=/mnt/img enable <unit>` from **outside** the chroot.
10. Generalize (see below).
11. Remove `policy-rc.d`, remove the staged playbook, unmount binds, unmount, disconnect nbd.
12. Sparsify and compress: zero the free space (`zerofree` on the unmounted ext4, or `fstrim` before unmount), then `qemu-img convert -c -O qcow2` into the output path. Consider `-o compression_type=zstd` if the size gate is tight — it compresses notably better than the default zlib and is read transparently by QEMU and PVE.
13. Check the size against the threshold and fail loudly if exceeded, naming the fallback ladder from the plan (zstd compression, safe trimming, or an alternate direct host such as GHCR for the intact qcow2) in the error message so the next person knows the options.
14. Print size and `sha256sum`.

**Generalization checklist** (task 04 will assert all of these, so get them right here):

- `chroot /mnt/img passwd -l <user>` — lock the password. Also confirm task 01 or the build removed the random-password generation and the password-display task from the base path; if `roles/user` still sets a password, locking it afterwards is the belt-and-braces fix, but the generation itself should not be baked.
- `rm -f /mnt/img/etc/ssh/ssh_host_*` — and verify the regeneration path (on Debian, `ssh-keygen -A` runs via the `ssh` unit's `ExecStartPre` or a generator; confirm host keys come back on first boot during task 06's verification).
- `: > /mnt/img/etc/machine-id` and `ln -sf /etc/machine-id /mnt/img/var/lib/dbus/machine-id` — mirror `generalizeScript` at `internal/provider/proxmoxprovision.go:437-457`.
- `rm -rf /mnt/img/var/lib/apt/lists/*` and `chroot /mnt/img apt-get clean`.
- `find /mnt/img/var/log -type f -delete` (or truncate), `rm -f /mnt/img/root/.bash_history /mnt/img/home/*/.bash_history`.
- Restore safe dpkg IO — `roles/base/tasks/main.yml:492-496` already does this at the end of the base role; confirm it ran and the `force-unsafe-io` config is gone.

**Make it usable locally.** The whole point of extracting this from the workflow is that a developer can run it. Keep dependencies to `qemu-utils` plus coreutils; print clear errors when run on an unsupported host (macOS has no `qemu-nbd` in this form — say so rather than failing obscurely); and let `--src-url` point at a local file for fast iteration.

**Expect the first runs to fail inside the playbook.** That is the audit's job to have predicted, but reality will add a few. When a task fails under chroot, fix it by extending the `sand_image_build` guard from task 01 rather than by hacking the script around it — the flag is the designed seam.

</details>

## Noteworthy Events

- [2026-09-28] The first real amd64 build exposed an Ansible 2.19 type error: `-e sand_image_build=true` supplied a string to a boolean `when` clause. The script now passes typed JSON extra vars. The failure trap removed all mounts, disconnected `/dev/nbd0`, and removed its private work directory before the retry.
- [2026-09-28] Two consecutive amd64 builds from the upstream Debian 13 image exited 0 using disk-backed `/var/tmp`. Run 1: 1,085,472,768 bytes (1,036 MiB), SHA-256 `ec1160e27b664fe1296ea0d3d94c6f7bf30aa41dfa696b920885465512bf79ea`. Run 2, replacing the same output path: 1,081,409,536 bytes (1,032 MiB), SHA-256 `7416b92f6354e40923d8eefa532f4c7fd4212daa6c119b195bf4764332e19986`. Logs are `/var/tmp/sand-base-task03-run1-retry.log` and `/var/tmp/sand-base-task03-run2.log`.
- [2026-09-28] Read-only nbd mounts of both outputs confirmed all requested shared tools and self-review skills present, four coding agents and their state absent, locked `claude` account, empty machine ID, no SSH host keys, APT lists, logs, or temporary dpkg unsafe-IO file. `qemu-img check` found no errors. The host `nbd` module was loaded for the builds and unloaded afterward; no nbd device or build mount remains.
- [2026-09-28] The 1900 MiB failure path is verified by inspection of its `die` branch; an over-limit build was not run because the real artifacts were comfortably below the threshold.
- [2026-09-28] Independent phase verification rebuilt from the upstream image at `/var/tmp/sand-base-phase2-root.qcow2`: 1,090,650,112 bytes (1,041 MiB), SHA-256 `27f24dc6aceda9ede14c34ad8d88239cd8261aed1c67777e37b7ffbb4a0f86c4`. `qemu-img check` found no errors. A read-only nbd inspection confirmed the shared toolchain and self-review tooling present; all four coding-agent binaries/state absent; locked password; empty machine ID; no SSH host keys, APT lists, logs, histories, staged playbook, or unsafe dpkg config; and the intended offline service links. Cleanup left no nbd connection, mount, or private work directory.
