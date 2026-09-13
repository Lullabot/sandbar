---
id: 15
group: "image-build"
dependencies: [3]
status: "pending"
created: 2026-09-13
model: "sonnet"
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

- [ ] `qemu-img convert` uses `-o compression_type=zstd` (with a documented fallback if the host's QEMU is too old to support it).
- [ ] dpkg path-exclusions are effective for `/usr/share/doc` and `/usr/share/man` — the base role already attempts this but **30M of doc and 15M of man survived into the built image**, so the current exclusion is leaking and must be fixed, not merely re-added.
- [ ] `/usr/share/locale` is trimmed to the configured locale(s) only, keeping `en_CA` and `en_US` (the base role configures `en_CA.UTF-8`).
- [ ] `/usr/share/i18n` locale *source* definitions are removed after `locale-gen` has run.
- [ ] `/usr/share/go-1.24/test` and `/usr/share/go-1.24/api` are removed.
- [ ] The `ieee-data` package (or its `/usr/share/ieee-data` payload) is removed.
- [ ] **No tool is removed.** The JDK and cloudflared are explicitly retained by user decision. `/usr/include`, GCC, and Go's `src` tree are explicitly retained (see Technical Requirements).
- [ ] Verification: build the image and paste the final compressed size in bytes and MiB, alongside the pre-optimization baseline of **1,229,651,968 bytes (1172.68 MiB)**. State the delta in MiB and as a percentage.
- [ ] Verification: after the build, mount the image and confirm every tool still works — `chroot` in and run `node --version`, `go version`, `java -version`, `docker --version`, `ddev --version`, `glab --version`, `gh --version`, `uv --version`, and confirm `claude`, `codex`, `drupalorg`, `mkcert` and `cloudflared` are present. Paste the output. A smaller image that lost a tool is a failed task.
- [ ] Verification: confirm the configured locale still works — `locale -a` inside the image lists `en_CA.utf8`. Paste it.
- [ ] Verification: build twice and confirm the size remains reproducible (within ~1 MiB), so the optimization did not reintroduce the nondeterminism that task 03 fixed.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- **Do not remove `/usr/include` or GCC.** `node-gyp` and Python C extensions compile against those headers; removing them breaks native module builds. Measured at 97M + 133M, and deliberately retained.
- **Do not remove Go's `src` tree** (`/usr/share/go-1.24/src`, 140M). Since Go 1.20 the standard library is compiled from source on demand, so deleting it breaks `go build`. Only `test` and `api` may go.
- **Do not remove the JDK or cloudflared** — both retained by explicit user decision.
- Locale trimming must not break `locale-gen`'s output; trim *after* the locale is generated, and verify `en_CA.utf8` survives.
- `compression_type=zstd` requires a reasonably modern `qemu-img`. Detect support and fall back to zlib with a clear warning rather than failing the build outright, so the script stays usable on older hosts.
- Keep the free-space zeroing fix from task 03 intact — it is what made the size reproducible, and it must run before compression regardless of algorithm.

## Input Dependencies

- Task 03's `scripts/build-base-image.sh`, including its free-space zeroing fix and its size gate.

## Output Artifacts

- A smaller published image and an updated build script — consumed by task 05 (CI) and task 04 (the hygiene gate, which must still pass).

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

**Measured starting point.** The image was analysed by mounting run 3's output. Total filesystem content is 3.0 GB, compressing to 1172.68 MiB (2.6x). The breakdown that matters:

Inherent and untouchable (vendor binaries, ~1.1 GB uncompressed): Codex 318M (a 251M binary plus a 67M helper), JDK 286M, Go 282M, Claude 214M (single binary, single version — no duplicate versions to clean), the Docker stack ~300M, GCC + headers 230M, node 121M, then glab 49M / uv 48M / gh 41M / ddev 40M / cloudflared 38M.

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

**Do not chase the vendor binaries.** Codex's 251M binary and Claude's 214M binary are single upstream artifacts; there is nothing to strip without breaking them, and they are Rust/Go static binaries that already compress ~2.5-3x. Resist the temptation to `upx` them — it breaks signature checks and slows startup.

</details>
