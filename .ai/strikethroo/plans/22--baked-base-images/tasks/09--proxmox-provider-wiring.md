---
id: 9
group: "provider-wiring"
dependencies: [6, 7]
status: "completed"
created: 2026-09-12
models:
  anthropic: "claude-sonnet-5"
  openai: "gpt-6-sol"
effort: "medium"
skills:
  - go
  - proxmox
---
# Point Proxmox at the manifest image and drop its base playbook run

## Objective

Move the Proxmox provider from its hardcoded image constants to the pinned manifest, and remove the base-phase playbook run from template construction — the image already contains everything that phase installed.

## Skills Required

`go` for the provider changes; `proxmox` for the import/template/clone lifecycle.

## Acceptance Criteria

- [x] The default URL, node-local filename and SHA-256 come from `baseimage.PinnedManifest.ForArch("amd64")`; `ensureCloudImage` keeps PVE's server-side download and SHA-256 verification.
- [x] `provisionBase` does not install bootstrap dependencies or call the base playbook phase.
- [x] `templateVersion` records the pinned image release (or custom image URL) plus `template-gen4`, replacing the playbook hash and invalidating prior stamps.
- [x] `Profile.BaseImage` remains a per-profile override; only the default uses the manifest.
- [x] Finalize still applies cloud-init identity, resizes, starts, and runs `runPlaybookPhase(finalize)`.
- [x] Mock-provider tests, `go build ./...`, and `go vet ./...` pass (evidence below).
- [x] The tagged e2e suite passes against `sandbar-test` (evidence below).
- [x] The live create log reports no `(base phase)` and does report `(finalize phase)`; the mock lifecycle test also rejects a base bootstrap command. A literal `TASK [base` grep is not a phase test here because the `base` role still has clone-specific tasks in finalize.
- [x] Live guest shell confirms baked dependencies, selected Pi, and absent unselected Claude (evidence below).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Proxmox is amd64-only; resolve the `amd64` manifest entry explicitly rather than using the host's `GOARCH` (the machine running `sand` may be arm64 while the PVE node is amd64 — this distinction matters and is easy to get wrong).
- PVE's `DownloadURL` performs a **server-side** download with a server-side SHA-256 verify. Keep using it — do not route the image through the user's machine.
- `acceptedImportExts` (`:83`) is `ova|ovf|qcow2|raw|vmdk`; `.img` is rejected by PVE. The published asset is `.qcow2`, so this is satisfied, but do not rename assets without checking it.
- `Cpu: "host"` is non-negotiable (kvm64 hides AVX2 and livelocks `claude install`) — leave it alone.
- `generalizeBase` / `generalizeScript` (`:437-457`): the published image is already generalized by the build, so decide whether this step is now redundant. If it is, remove it; if it still adds value for the template (e.g. after any per-template preparation), keep it and say why in the task record. Do not leave it running by accident without a decision.
- Preserve the `cloneSerial` serialization and the cross-process advisory lock (`:115`, `ensureBaseAndClone` `:218`) — parallel clones contend on PVE's server-side flock.

## Input Dependencies

- Task 07's pinned manifest.
- Task 06's finding confirming the image imports and runs on PVE.

## Output Artifacts

- The rewired Proxmox base path — consumed by task 10 (staleness cleanup) and exercised by the `proxmoxe2e` suite.
- A Proxmox-only provider PR branching from Task 07's shared foundation. It is a sibling of Task 08's Lima PR, not its parent or child.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

**This is the smaller of the two provider tasks**, because Proxmox *already* works the way the plan wants: it downloads a project-published golden image, verifies its checksum server-side, imports it, and templates it. The change is narrow — swap the constants for the manifest, and delete the base playbook call.

**The constants being replaced** are at `internal/provider/proxmoxprovision.go:61-77`:

```go
baseImageURL  = "https://github.com/Lullabot/sandbar/releases/download/base-image-2026.07.21/sandbar-base-debian-13-amd64.qcow2"
baseImageFile = "sandbar-base-debian-13-amd64.qcow2"
const defaultBaseImageSHA256 = "57500f86..."
```

with a comment explicitly warning that all three must be bumped together. That comment becomes obsolete — remove it along with the constants, and make sure task 07's regeneration target is mentioned in its place so the next person knows how to bump the image.

**`templateGeneration` is your flag day.** It exists precisely to force rebuilds of existing templates when provider-side preparation changes (`:475`, currently `":template-gen2"`). Bumping it to `gen3` makes every existing `sandbar-base` template on every PVE node rebuild once against the new image. That is the correct and intended behaviour here — a template built by the old path contains a base-playbook-provisioned guest, which is not what the new code expects.

**Deciding about `generalizeBase`.** The script truncates `/etc/machine-id` and re-links `/var/lib/dbus/machine-id` as the last in-guest step before templating. The published image now ships already generalized (task 03), *and* the template will have been booted and had a finalize-free base run... actually check this carefully: does `provisionBase` still boot the VM before templating after you remove the playbook call? If it boots, systemd will have repopulated `/etc/machine-id`, and generalizing before templating is still necessary. If it no longer boots at all, the step is redundant. Work out which, state it in the task record, and act accordingly — do not guess.

**The `PROXMOX_E2E_IMAGE` override.** Its documentation explicitly warns to leave it unset, because a stock image lacks `qemu-guest-agent` and most overrides hang the lifecycle test rather than teaching you anything. That warning is now even more true — a stock image also lacks every tool. Update the comment if its reasoning changed.

**E2E environment.** The suite needs `PROXMOX_E2E=1` plus HOST/NODE/POOL/STORAGE/BRIDGE/TOKEN_FILE/SSH_USER/SSH_IDENTITY, and skips cleanly when they are absent. A local `e2e.env` (gitignored) carries them. If no PVE target is reachable, say so plainly in the task record rather than marking the e2e criterion satisfied — an unrun test is not a passing test.

</details>

## Verification Evidence

RED: the new tests failed because `templateVersion` returned a playbook hash with `template-gen3`, and a cold base build logged both dependency installation and `Provisioning sandbar-base (base phase)`. GREEN/REFACTOR: `go test ./internal/provider/... -count=1` returned `ok github.com/lullabot/sandbar/internal/provider 5.601s`. `go build ./...`, `go vet ./...`, and `go test ./... -count=1` all passed; `gofmt -l` and `git diff --check` were clean.

Live setup: `e2e.env` pointed at `sandbar-test`; `PROXMOX_E2E=1`, `PROXMOX_E2E_INSECURE=1` (certificate SAN has a trailing dot), and `PROXMOX_E2E_IMAGE` unset. A fresh disposable Ed25519 key replaced the absent configured identity path. The refreshed 0600 token authenticated to the PVE node status API with HTTP 200. The live lifecycle command `go test -tags proxmoxe2e -timeout 45m -run TestE2EProxmoxLifecycle -v -count=1 ./internal/provider/` passed in 291.18s. It logged `Create phases: base=false, finalize=true` and the guest shell returned `/usr/bin/ansible-playbook`, `/usr/bin/rsync`, `/usr/bin/docker`, `/usr/bin/node`, and `pi-selected claude-absent`.

The full tagged run, `go test -tags proxmoxe2e -timeout 60m -run TestE2EProxmox -v -count=1 ./internal/provider/...`, returned `PASS` and `ok github.com/lullabot/sandbar/internal/provider 783.129s`. Lifecycle passed (282.64s); the hard-stop SSH test passed (session ended 2m9s after guest loss); two clones had distinct machine identities and leases (`192.168.30.68`, `192.168.30.63`) and passed the concurrent-stream check. Pool isolation skipped because no `PROXMOX_E2E_FOREIGN_VMID` is configured; it is outside this task's lifecycle criterion. After each run, the test clones were deleted, the test-created base template VMID 121 was purged, the pool was verified to contain zero QEMU resources, and the disposable local key/state directory was removed.

## Noteworthy Events

- [2026-09-29] Kept the base boot and `generalizeBase`: the boot repopulates `/etc/machine-id` even though the published image is generalized, so clearing machine ID and hostname immediately before templating is still required for distinct clone identities and DHCP leases.
- [2026-09-29] Task 07 had already wired the pinned amd64 manifest into `NewProxmox`, and commit `2af8e06` had versioned PVE's local import filename. This task reused both and removed only the obsolete base provisioning path; clone locks, host CPU, custom image override, and finalize ordering remain intact.
