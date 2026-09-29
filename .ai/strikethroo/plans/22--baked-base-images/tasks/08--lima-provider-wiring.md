---
id: 8
group: "provider-wiring"
dependencies: [6, 7]
status: "completed"
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

- [x] `RenderBaseOverlay` (`internal/provision/overlay.go:122-133`) emits an `images:` block with the host-matching entry carrying `location`, `arch` and `digest`, instead of `base: [template:_images/debian-13]`.
- [x] `buildBase` (`internal/provision/provision.go:216-304`) no longer calls the base-phase playbook. It creates the instance from the image, stops it, and writes the version stamp.
- [x] The base version stamp records the **image version** from the manifest, not a playbook content hash.
- [x] The `/mnt/playbook` read-only mount and the `overlayProvision` bootstrap remain in place for the finalize phase; the bootstrap's idempotent guard now finds its dependencies already present.
- [x] The finalize phase is unchanged — hostname, git identity, timezone, tmux config, selected coding-agent installation, and optional repo clone still apply per VM.
- [x] Verification: `go build ./...` and `go vet ./...` pass; `go test ./internal/provision/...` passes. Paste the output.
- [x] Verification: a real `sand create` on a clean host produces a working VM. Confirm from the output that **no** `TASK [base :` banners appear, and that a download-and-clone happened instead. Paste the relevant output.
- [x] Verification: shell into the VM and confirm baked dependencies are present (`node --version`, `docker --version`, `ddev --version`, `go version`, `java -version`), a selected coding agent was installed during finalize, an unselected one was not, and per-VM identity applied.
- [x] Verification: create a **second** VM and confirm it reuses the cached image and existing base — no second download, materially faster. Paste the timing.

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
- A Lima-only provider PR branching from Task 07's shared foundation. It is a sibling of Task 09's Proxmox PR, not its parent or child.

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

## Execution Evidence

Validation on 2026-09-29:

```text
$ go test ./internal/provision/...
ok  github.com/lullabot/sandbar/internal/provision  9.496s
$ go build ./...
# exit 0
$ go vet ./...
# exit 0
$ go test ./...
# all packages passed
$ gofmt -l .
# no output
```

The isolated amd64 host used `/var/tmp/sandbar-lima-verify-2026.09.29.151245/bin/limactl` and separate `LIMA_HOME`/XDG directories under `/var/tmp/sandbar-task08-lima-20260929`. The initial acquisition downloaded and verified 960 MiB from the pinned release:

```text
==> downloading base image: sandbar-base-debian-13-amd64.qcow2
==> downloading base image: 960 / 960 MiB
==> base image verified: .../images/base-image-2026.09.29.164715/sandbar-base-debian-13-amd64.qcow2
```

After resolving the host's transient KVM permission and the image's missing 9p modules, a clean successful create of `task08a` used the verified cached download, built a base instance, cloned it, and ran **only finalize-phase Ansible**:

```text
==> base image cache hit: .../sandbar-base-debian-13-amd64.qcow2
==> Creating base instance "task08-base" from verified image base-image-2026.09.29.164715…
==> Stopping base image "task08-base" (making it idle for cloning)…
==> Cloning "task08a" from base image "task08-base"…
==> Provisioning "task08a" (finalize phase, Ansible)…
PLAY RECAP: ok=34 changed=13 unreachable=0 failed=0 skipped=123
[timing]   TOTAL                    3m51.37s
```

The task's literal “no `TASK [base :` banners” criterion needs a distinction: **no base-phase playbook ran during base construction**, but the unchanged finalize play still lists `TASK [base : ...]` entries because it applies per-VM hostname and network identity and reports its skipped base-only tasks. Removing those banners would require changing finalize, which this task explicitly forbids.

The first guest reported Node `v24.21.0`, Docker `29.8.1`, DDEV `v1.25.4`, Go `go1.24.4`, OpenJDK `21.0.12.1`, and `codex-cli 0.159.0`; `~/.local/bin/claude` was absent. Git identity was `Task Eight <task08@example.invalid>`, hostname `task08a`, timezone `America/Toronto`, and both `~/.tmux.conf` and `/mnt/playbook/site.yml` were present. A second create of `task08b` logged **only** clone, clone start, and finalize, with no acquisition or base creation, and completed in `1m44.419s`. Its Git identity was `Second VM <second@example.invalid>`; Codex was absent as selected.

## Noteworthy Events

- [2026-09-29] The published Debian cloud kernel lacks 9p modules, so Lima's default mount type left `/mnt/playbook` empty and finalize could not find `site.yml`. Pinning `mountType: reverse-sshfs` restored the read-only mount on base and clones; Lima prepared sshfs during the first base boot.
- [2026-09-29] Lima removed the temporary `/dev/kvm` ACL during base shutdown, causing the initial clone boot to fail. The successful isolated run executed as the same user with primary group `kvm`; no persistent account or device permission change remains. All three disposable VMs were deleted.
- [2026-09-29] After finalize, `id andrew` included `docker`, but the already-open Lima SSH ControlMaster session did not; `docker ps` in that session received a socket permission error. This is a separate post-finalize session-refresh issue, not a missing baked Docker executable. The requested Docker version check passed.
- [2026-09-29] Apt-cache seed and harvest are skipped on the published-image path: there is no base-phase apt playbook to benefit from them. The older convergence machinery remains available pending its separate cleanup.
