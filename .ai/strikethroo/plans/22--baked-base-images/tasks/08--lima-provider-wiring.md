---
id: 8
group: "provider-wiring"
dependencies: [6, 7]
status: "pending"
created: 2026-09-12
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
complexity_score: 8
complexity_notes: "Rewires the core create path: overlay rendering, base construction and version stamping all change together, and a mistake makes every VM creation fail."
skills:
  - go
  - lima
---
# Create the Lima base from the downloaded image instead of building it in-guest

## Objective

Point Lima at sand's published image rather than Lima's stock `template:_images/debian-13`, and stop running the base-phase playbook in the guest — `buildBase` becomes create-from-image, stop, and stamp the image version.

## Skills Required

`go` for the provisioning layer changes; `lima` for the overlay's `images:` block and instance lifecycle semantics.

## Acceptance Criteria

- [ ] `RenderBaseOverlay` (`internal/provision/overlay.go:122-133`) emits an `images:` block with the host-matching entry carrying `location`, `arch` and `digest`, instead of `base: [template:_images/debian-13]`.
- [ ] `buildBase` (`internal/provision/provision.go:216-304`) no longer calls the base-phase playbook. It creates the instance from the image, stops it, and writes the version stamp.
- [ ] The base version stamp records the **image version** from the manifest, not a playbook content hash.
- [ ] The `/mnt/playbook` read-only mount and the `overlayProvision` bootstrap remain in place for the finalize phase; the bootstrap's idempotent guard now finds its dependencies already present.
- [ ] The finalize phase is unchanged — hostname, git identity, timezone, tmux config, selected coding-agent installation, and optional repo clone still apply per VM.
- [ ] Verification: `go build ./...` and `go vet ./...` pass; `go test ./internal/provision/...` passes. Paste the output.
- [ ] Verification: a real `sand create` on a clean host produces a working VM. Confirm from the output that **no** `TASK [base :` banners appear, and that a download-and-clone happened instead. Paste the relevant output.
- [ ] Verification: shell into the VM and confirm baked dependencies are present (`node --version`, `docker --version`, `ddev --version`, `go version`, `java -version`), a selected coding agent was installed during finalize, an unselected one was not, and per-VM identity applied.
- [ ] Verification: create a **second** VM and confirm it reuses the cached image and existing base — no second download, materially faster. Paste the timing.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Resolve architecture on the host that runs `limactl` using task 07's helper, acquire only that manifest entry, and render only that entry. Never infer a remote Lima host's architecture from workstation `runtime.GOARCH`.
- Lima's `arch` values are `x86_64` and `aarch64`, not Go's `amd64`/`arm64`. Map correctly.
- `digest` is expressed as `sha256:<hex>`.
- The acquired `location` must be meaningful on the host running `limactl`: a workstation path for local Lima and a remote-host path for remote Lima.
- Keep the writable-mount strip in `Configure` (`internal/lima/client.go:217-223`); it is a standing guard unrelated to this change.
- Preserve the base advisory lock discipline in `prepareBaseAndClone` (`internal/provision/provision.go:519-545`).
- Do not remove the staleness machinery here — task 10 owns that. Do not disturb golden-template `CreateOptions.TemplateSource`; template-backed create/reset must continue bypassing shared-base preparation.

## Input Dependencies

- Task 07's acquisition helper and pinned manifest.
- Task 06's finding confirming the image boots under Lima on both architectures.

## Output Artifacts

- The rewired Lima create path — consumed by task 10 (which then removes the now-dead convergence machinery) and exercised by task 13's CI assertions.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

**The key simplification.** Today `buildBase` does: render overlay → create instance → seed apt cache → run base playbook → harvest apt cache → stop → stamp. The apt cache seeding and harvesting exist *only* to make the in-guest base playbook faster. With the playbook gone from this path, they go too — but check whether the finalize phase benefits from them before deleting; if it does, keep them, and if not, say so in the task record.

**Overlay shape.** Replace the `base:` line with one acquired, host-matching entry, for example:

```yaml
images:
- location: "/home/user/.cache/sandbar/images/sandbar-base-debian-13-amd64.qcow2"
  arch: "x86_64"
  digest: "sha256:..."
```

Sand resolves, acquires, and verifies that architecture on the same host where `limactl` runs, then emits that host-local path. For remote Lima, use the existing `lima.Host` seam for both `uname -m` and file access; never hand remote `limactl` a workstation path or assume both machines share an architecture.

**Version stamping.** `PlaybookVersion` currently returns `"v2:" + sha256(playbook fileset) + ":" + toolsetKey` (`internal/provision/baseversion.go:121-127`). For the base stamp, that becomes the manifest's image version string. Task 10 removes the toolset component entirely; here, just make sure the *base* stamp is the image version and that the comparison used to decide "do I need to rebuild the base" compares image versions.

**What stays.** The finalize phase is untouched and still runs in-guest from the located playbook — that is what preserves the contributor working-tree loop for finalize work, and what keeps the door open for the user's per-repo-Ansible idea. Do not be tempted to also rewrite finalize; it is explicitly out of scope.

**The bootstrap script.** `overlayProvision` (`internal/provision/overlay.go:51-101`) installs `ansible-core rsync curl gnupg ca-certificates python3-passlib` and reruns on every boot with an idempotent guard. All of those are now in the image, so the guard short-circuits. Leave the script in place — it costs nothing when everything is present, and it keeps the finalize phase working if someone points sand at a non-sandbar image.

**Sequencing.** Land this so the tree still builds and tests pass with task 10's machinery intact but partially unused. Making both changes at once produces a diff too large to review and makes a bisect useless if creates start failing.

</details>
