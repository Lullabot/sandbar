---
id: 15
group: "image-build"
dependencies: [3]
status: "completed"
created: 2026-09-13
models:
  anthropic: "claude-sonnet-5"
  openai: "gpt-6-sol"
effort: "medium"
skills:
  - shell
  - linux-image-plumbing
---
# Reduce the published image size with safe trims and zstd compression

## Objective

Cut the published image's compressed size without removing any tool, by switching qcow2 compression from zlib to zstd and excluding content that is measurably large and functionally unused — then measure the actual combined saving rather than estimating it.

## Skills Required

`shell` for the build script changes; `linux-image-plumbing` for dpkg path-exclusion, locale trimming and qcow2 compression options.

## Acceptance Criteria

- [x] `qemu-img convert` uses `-o compression_type=zstd` (with a documented fallback if the host's QEMU is too old to support it).
- [x] dpkg path-exclusions are effective for `/usr/share/doc` and `/usr/share/man` — the base role already attempts this but **30M of doc and 15M of man survived into the built image**, so the current exclusion is leaking and must be fixed, not merely re-added.
- [x] `/usr/share/locale` is trimmed to the configured locale(s) only, keeping `en_CA` and `en_US` (the base role configures `en_CA.UTF-8`).
- [x] `/usr/share/i18n` locale *source* definitions are removed after `locale-gen` has run.
- [x] `/usr/share/go-1.24/test` and `/usr/share/go-1.24/api` are removed.
- [x] The `ieee-data` package (or its `/usr/share/ieee-data` payload) is removed.
- [x] **No tool is removed.** The JDK and cloudflared are explicitly retained by user decision. `/usr/include`, GCC, and Go's `src` tree are explicitly retained (see Technical Requirements).
- [x] Verification: build the current-`main` base before and after optimization and report both sizes. Do not use the earlier 1,229,651,968-byte prototype as the baseline because it predates per-VM agent installation.
- [x] Verification: after the build, mount the image and confirm every shared dependency still works — Node, Go, Java, Docker, DDEV, glab, gh, uv, drupalorg, mkcert, cloudflared, and self-review tooling. Confirm coding-agent binaries remain absent because they install during finalize.
- [x] Verification: confirm the configured locale still works — `locale -a` inside the image lists `en_CA.utf8`. Paste it.
- [x] Verification: build twice and confirm the size remains reproducible (within ~1 MiB), so the optimization did not reintroduce the nondeterminism that task 03 fixed.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- **Do not remove `/usr/include` or GCC.** `node-gyp` and Python C extensions compile against those headers; removing them breaks native module builds. Measured at 97M + 133M, and deliberately retained.
- **Do not remove Go's `src` tree** (`/usr/share/go-1.24/src`, 140M). Since Go 1.20 the standard library is compiled from source on demand, so deleting it breaks `go build`. Only `test` and `api` may go.
- **Do not remove the JDK or cloudflared** — both retained by explicit user decision.
- Locale trimming must not break `locale-gen`'s output; trim *after* the locale is generated, and verify `en_CA.utf8` survives.
- `compression_type=zstd` requires a reasonably modern `qemu-img`. Detect support and fall back to zlib with a clear warning rather than failing the build outright, so the script stays usable on older hosts.
- Keep task 03's discard-aware `fstrim` intact — it is what prevents deleted filesystem content from inflating the converted image, and it must run before compression regardless of algorithm.

## Input Dependencies

- Task 03's `scripts/build-base-image.sh`, including its discard-aware trim and its size gate.

## Output Artifacts

- A smaller published image and an updated build script — consumed by task 05 (CI) and task 04 (the hygiene gate, which must still pass).

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

**Measured starting point.** The image was analysed by mounting run 3's output. Total filesystem content is 3.0 GB, compressing to 1172.68 MiB (2.6x). The breakdown that matters:

Inherent and untouchable content includes the JDK, Go toolchain and source, Docker stack, GCC and headers, Node, glab, uv, gh, DDEV, and cloudflared. Claude Code, Codex, OpenCode, and Pi are no longer image-size inputs: `main` installs selected agents per VM during finalize.

Trimmable, with measured sizes:

| Target | Uncompressed | Note |
| --- | --- | --- |
| `/usr/share/locale` | 94M | Only `en_CA.UTF-8` is configured |
| `/usr/share/doc` | 30M | Exclusion already attempted upstream but leaking |
| `/usr/share/go-1.24/test` | 20M | Go's own test suite |
| `/usr/share/i18n` | 17M | Locale sources, redundant post-`locale-gen` |
| `/usr/share/ieee-data` | 15M | OUI/MAC vendor database |
| `/usr/share/man` | 15M | Same leaking exclusion as doc |
| `/usr/share/go-1.24/api` | 8.7M | Go API history |

**Set expectations honestly.** That is ~200M uncompressed, but it is almost entirely *text*, which compresses 4-6x. Expect only roughly **40 MiB compressed** from the trims. The larger lever is zstd, which on this class of content typically buys 10-20% — on a 1172 MiB image that is **120-230 MiB**. Do zstd first and measure it alone before layering the trims, so the plan record shows which change actually bought what. That ordering matters more than the total.

**Why doc/man are leaking.** `roles/base/tasks/main.yml` sets dpkg speed/size hacks early (around `:65-89`) including no-docs/no-man exclusions, and then restores safe dpkg settings near the end (`:492-496`). If the restore removes the path-exclusions before some packages install, or if packages were installed outside that window, docs land anyway. Investigate which packages contributed the surviving 45M (`du -sh /usr/share/doc/* | sort -rh | head` on a mounted image) before changing anything — the fix may be as simple as moving the restore, or may need an explicit post-install purge. An explicit purge at generalization time is the more robust option and is acceptable here.

**Locale trimming.** The conventional tools are `localepurge` (interactive by default — needs preseeding) or a dpkg `path-exclude=/usr/share/locale/*` with `path-include` for the locales you keep. Given the build already has a generalization stage, a direct `find /usr/share/locale -mindepth 1 -maxdepth 1 -type d ! -name 'en*' -exec rm -rf {} +` is simpler and more predictable than adding a package. Verify `locale -a` afterwards.

**Measure per-change.** Record the size after: (a) zstd alone, (b) zstd + trims. Two numbers, so the plan can say which lever mattered. If zstd alone gets the image comfortably small, the trims become optional complexity and that is a legitimate finding to report.

**Do not move agents back into the image to simplify verification.** Their per-VM lifecycle is deliberate and keeps fast-moving binaries out of a slowly released base. Optimize only content the base phase actually owns.

</details>

## Execution Notes

- Fresh current-main/task-03 zstd-only baseline (`/var/tmp/sand-base-phase2-root.qcow2`): 1,090,650,112 bytes (1041 MiB), SHA-256 `27f24dc6aceda9ede14c34ad8d88239cd8261aed1c67777e37b7ffbb4a0f86c4`. The earlier 1,229,651,968-byte prototype was not used.
- Read-only baseline inspection: `/usr/share/doc` 34M, man 16M, locale 97M, i18n 17M, Go test 20M, Go api 8.7M, ieee-data 15M. Docs include material from the upstream cloud image and bootstrap packages installed before the role's dpkg exclusions; the final cleanup now removes those already unpacked files while keeping `copyright` files.
- First optimized full build (`/var/tmp/sand-base-task15-a.qcow2`): 1,017,511,936 bytes (971 MiB), SHA-256 `d071f2e744e3e975fa8ca21be8905a983ebba6b89e176aff96a79ac6e0661398`. Saving versus the fresh zstd-only baseline: 73,138,176 bytes (69.75 MiB).
- First image mounted verification: `locale -a` printed `C`, `C.utf8`, `POSIX`, `en_CA.utf8`; doc 8.4M, man 4K, locale 24K; i18n, Go test/api, ieee payload and all four coding-agent binaries absent. Node, Go, Java, Docker, DDEV, glab, gh, uv, drupalorg, mkcert, cloudflared, and both self-review executables remained present and responded to version/help probes. DDEV's version command reported v1.25.4 but exited nonzero because Docker is not running in a mounted image. In both the baseline and optimized read-only chroot, Go needs `GOROOT=/usr/lib/go-1.24` and Java needs `LD_LIBRARY_PATH=/usr/lib/jvm/java-21-openjdk-amd64/lib` for direct version probes; these are pre-existing chroot limitations, not trim regressions.
- The first two full trim builds varied by 7,602,176 bytes (7.25 MiB) despite identical regular-file payload. Increasing qcow2 cluster size to 2 MiB left a 6.44 MiB gap. An offline `e2fsck` → `resize2fs -M` → regrow to the unchanged 20 GiB partition → `e2fsck` → `fstrim` repack on copies of those builds yielded 1,006,501,888 and 1,006,698,496 bytes, only 196,608 bytes (0.1875 MiB) apart. That exact sequence is now in the builder, retaining its original discard step.
- The base play previously ran `mkcert -install`, creating a shared CA private key in every clone. The mkcert binary stays baked, while a dedicated `mkcert-ca` role now runs after `dev-tools` in `full` and `finalize`, never in `base`. Diagnostic full build C with the integrated repack yielded 1,006,698,496 bytes, SHA-256 `05856fd3e009b4f2544e20cbc24eb12dcb46497b3f68a2e621667ea3ff326df4`; its mounted image had `en_CA.utf8`, mkcert binary, Go source, headers, and GCC, with no CA/key or trimmed content.
- Final-fileset full builds D and E succeeded from the same script/playbook: D 1,007,288,320 bytes (SHA-256 `79cd3fafe74a49069bbdfaef1e8e6b755b1960b756e12fc3cf723c4660957122`), E 1,006,436,352 bytes (SHA-256 `13b59e82d4b65943ffe2e29f3602e959cb9a4bc6571f6dc0d5844c743576e4b6`). The 851,968-byte difference is 0.8125 MiB, within the ~1 MiB target. Relative to the fresh zstd-only baseline, E saves 84,213,760 bytes (80.3125 MiB). `qemu-img check` found no errors on either image.
- Mounted E verification: `locale -a` printed `C`, `C.utf8`, `POSIX`, `en_CA.utf8`; doc 8.4M (copyright kept), man 4K, locale 24K. All specified trim targets, coding-agent binaries, and mkcert CA/key were absent; Node 24.21.0, Go 1.24.4, OpenJDK 21, Docker 29.8.1, DDEV 1.25.4, glab 1.119.0, gh 2.101.0, uv 0.12.20, drupalorg 0.13.0, mkcert 1.4.4, cloudflared 2026.9.3, self-review-serve 1.45.0, and the self-review installer were present. Direct Go and Java probes in a read-only chroot needed the same environment overrides as the untrimmed baseline; DDEV reported its version but could not contact Docker in the offline image.
- An isolated writable copy of E ran the new mkcert role twice: `base` skipped both role tasks and asserted no CA key; `finalize` created `rootCA.pem` and `rootCA-key.pem` for the VM user (Ansible recap `ok=9 changed=1 failed=0 skipped=2`). No standalone unit test was added for shell-only image cleanup because full builds and mounted inspection exercise the actual filesystem and qcow2 result. `bash -n`, Ansible syntax check, and `git diff --check` passed.
- Independent phase verification reproduced both final sizes and digests, ran `qemu-img check` cleanly on D and E, and mounted E read-only. Root-owned probes confirmed the full 5,210,107-block filesystem, `en_CA.utf8`, all retained tool paths and reported versions, absent trim targets/coding agents/mkcert key, and the reduced doc/man/locale payload. A separate disposable qcow2 overlay ran the role boundary itself: `base` skipped both mkcert tasks and `finalize` created non-empty CA/key files. All mounts, overlays, and nbd connections were cleaned afterward.
- Root-owned `molecule test -s base-idempotence` passed after the locale refresh change. The replay reported `ok=32 changed=0 failed=0`, skipped `locale-gen` after detecting `en_CA.utf8`, passed every stable-state assertion, and destroyed the test container.
