---
id: 5
group: "image-build"
dependencies: [3, 4, 15]
status: "completed"
created: 2026-09-12
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
complexity_score: 7
complexity_notes: "Publishes bootable public artifacts and their trust manifest across two architectures; release ordering or checksum mistakes are supply-chain failures."
skills:
  - github-actions
---
# Build both architectures in CI and publish the images with a manifest

## Objective

Rework `.github/workflows/base-image.yml` from a single amd64 job that bakes one package into a two-architecture matrix that calls the build script, runs the hygiene gate, and publishes both images plus checksums and a `manifest.json` to a UTC-timestamped `base-image-YYYY.MM.DD.HHMMSS` release.

## Skills Required

`github-actions` — matrix jobs, native per-arch runners, artifact passing between jobs, and release publishing under this repo's immutable-release constraint.

## Acceptance Criteria

- [x] The build job is a matrix over `{amd64 → ubuntu-24.04, arm64 → ubuntu-24.04-arm}`, each calling `scripts/build-base-image.sh` for its own architecture.
- [x] Each matrix leg runs `scripts/check-base-image.sh` against its output and fails the job if the gate fails.
- [x] A separate publish job collects both legs' artifacts and creates one release containing: both qcow2 assets, a `.sha256` per asset, and a `manifest.json` carrying, per architecture, the asset URL, size in bytes, and SHA-256, plus the image version string.
- [x] Publishing follows the existing draft-then-flip pattern: `gh release create --draft`, upload all assets, then `gh release edit --draft=false` — because this repo has immutable releases and assets cannot be added post-publish.
- [x] New tags use `base-image-YYYY.MM.DD.HHMMSS` in UTC so multiple immutable images can ship in one day; legacy `base-image-YYYY.MM.DD` tags remain accepted. The namespace stays disjoint from release-please's `vX.Y.Z`, and the existing `workflow_dispatch` tag input is honoured.
- [x] Verification: `gh workflow run base-image.yml` completes with both matrix legs green. Paste the run URL and `gh run view <id>` output showing both legs succeeded.
- [x] Verification: `gh release view <tag> --json assets` lists exactly the expected assets for both arches plus the manifest. Paste it.
- [x] Verification: download both images and confirm `sha256sum -c` passes against the published checksums, and that each asset is under 2 GiB. Paste `ls -l` and the checksum results.
- [x] Verification: `manifest.json`'s recorded SHA-256 for each arch matches the published `.sha256` file for that arch — a mismatch here would poison every downstream download.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- `ubuntu-24.04-arm` runners are free **only for public repositories**; workflows in private repos using that label fail. This repo is public — note the constraint in a comment so it is not lost.
- Neither runner exposes `/dev/kvm`. The build must not need it (it does not — it never boots a guest).
- The two matrix legs run on different machines, so their outputs must reach the publish job via `actions/upload-artifact` / `download-artifact`. Mind the artifact size — these are ~1.5 GiB files.
- Keep `concurrency: group: base-image` so two image builds cannot race on the same tag.
- Keep the monthly `schedule` trigger and the `workflow_dispatch` tag input.
- The `manifest.json` schema is consumed by task 07's generator — agree the field names there and keep them stable.

## Input Dependencies

- Task 03's `scripts/build-base-image.sh`.
- Task 04's `scripts/check-base-image.sh`.

## Output Artifacts

- The reworked `.github/workflows/base-image.yml`.
- A published `base-image-YYYY.MM.DD.HHMMSS` release with both images, checksums and `manifest.json` — consumed by task 06 (verification), task 07 (manifest pinning) and tasks 08/09 (provider wiring).

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

**Read the existing workflow's header comment first.** It documents three constraints that still apply and are easy to rediscover painfully:

1. *Immutable releases.* Assets cannot be added after publish (a 422). Hence create as draft, upload everything, then flip to published. Do not restructure this into a plain `gh release create` with assets — the draft dance is load-bearing.
2. *Tag namespace.* New releases use `base-image-YYYY.MM.DD.HHMMSS` in UTC, while existing date-only tags remain valid. The `base-image-` namespace is deliberately disjoint from release-please's `vX.Y.Z` so the two release pipelines never collide.
3. *`--repo` is explicit* because the publish job has no checkout and `gh` cannot infer the repository from a git remote. If you add a checkout step, you may drop it — but adding a checkout to a job handling multi-gigabyte artifacts is not obviously worth it.

**Suggested job shape:**

```
jobs:
  build:
    strategy:
      fail-fast: false
      matrix:
        include:
          - arch: amd64
            runner: ubuntu-24.04
          - arch: arm64
            runner: ubuntu-24.04-arm
    runs-on: ${{ matrix.runner }}
    steps: checkout → install qemu-utils → build → check → upload-artifact
  publish:
    needs: build
    runs-on: ubuntu-24.04
    steps: download-artifact (both) → compute manifest → draft release → upload → flip
```

`fail-fast: false` matters: if arm64 breaks you want to see whether amd64 also broke, not have it cancelled.

**Manifest shape.** Keep it small and boring. Something like:

```json
{
  "version": "base-image-2026.09.12.143205",
  "images": {
    "amd64": { "url": "https://github.com/.../sandbar-base-debian-13-amd64.qcow2", "sha256": "...", "size": 1523456789 },
    "arm64": { "url": "...", "sha256": "...", "size": 1498765432 }
  }
}
```

The URL must be the final published asset URL, which is predictable from the tag and asset name — build it rather than trying to read it back from a draft release.

**Watch the artifact hop.** Uploading and re-downloading two ~1.5 GiB files between jobs costs real time and counts against artifact storage. An alternative is to have each matrix leg upload its own asset directly to the draft release (created by a preceding job) and have a final job write the manifest and flip the draft. That avoids the double transfer. Either shape is acceptable — pick one and comment why.

**Do not** change the amd64 asset name gratuitously. `internal/provider/proxmoxprovision.go` derives its import filename from it today; task 09 will move that to the manifest, but keeping the name stable avoids a needless flag day.

</details>

## Execution Notes

- Implemented the native architecture matrix, image hygiene gate, and durable
  `upload-artifact`/`download-artifact` transfer. Artifact compression is off
  because the qcow2 files are already compressed. Both jobs use disk-backed
  `/var/tmp` paths.
- `scripts/write_base_image_manifest.py` defines the stable schema used by task
  07: `version`, then `images.amd64` and `images.arm64`, each with `url`,
  `sha256`, and `size` in bytes. It rejects missing or extra assets, malformed
  checksums, digest mismatch, and images at or above the 2 GiB release limit
  before any draft release is created.
- The manifest tests were written first and failed because the writer did not
  exist. After implementation, all six passed; the full Python suite ran 13
  tests and passed. `actionlint v1.7.8` passed on
  `.github/workflows/base-image.yml`; `git diff --check` passed.
- Live workflow, release asset, and published checksum verification remain
  pending until this implementation is committed and dispatched on GitHub.
- The implementation was committed as `187a813` and pushed to
  `origin/worktree-plan-baked-images`. Dispatching
  `base-image-2026.09.29` failed before a run was created: GitHub returned
  `HTTP 403: Resource not accessible by personal access token` for the workflow
  dispatch endpoint. The active credential comes only from `GH_TOKEN`; no
  stored `gh` credential is available when that override is removed. A fresh
  run listing was empty and the release tag remained unused. Retry this task
  after providing a token with Actions workflow-dispatch permission; no code
  remediation or release cleanup is currently required.
- A review follow-up removed the one-release-per-day collision: an empty tag
  input now resolves to `base-image-YYYY.MM.DD.HHMMSS` in UTC. The manifest
  validator strictly validates both the calendar date and clock time while
  retaining legacy date-only tags for the already-published image. Focused
  tests cover the timestamped happy path, legacy compatibility, an invalid
  clock time, and an incomplete timestamp.
- Review follow-up `580eacd` also fixed both amd64 jobs to Ubuntu 24.04 and
  SHA-pinned every third-party action, matching the rest of the repository.
- Workflow run https://github.com/Lullabot/sandbar/actions/runs/36587865806
  completed successfully at PR 206 commit `6a61c25`: `Build amd64`, `Build
  arm64`, and `Publish both architectures` all passed. It published
  https://github.com/Lullabot/sandbar/releases/tag/base-image-2026.09.29.151245
  with exactly the two qcow2 files, two checksum files, and `manifest.json`.
- Downloaded publication verification passed:
  `sandbar-base-debian-13-amd64.qcow2: OK` and
  `sandbar-base-debian-13-arm64.qcow2: OK`. Sizes were 1,006,632,960 and
  974,258,176 bytes respectively, both images passed `qemu-img check`, and the
  manifest/checksum digests matched GitHub's server-computed asset digests.
