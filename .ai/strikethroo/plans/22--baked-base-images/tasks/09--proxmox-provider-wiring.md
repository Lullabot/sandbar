---
id: 9
group: "provider-wiring"
dependencies: [7]
status: "pending"
created: 2026-09-12
model: "sonnet"
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

- [ ] `ensureCloudImage` (`internal/provider/proxmoxprovision.go:494-544`) resolves its URL, filename and SHA-256 from the pinned manifest rather than the removed constants.
- [ ] `provisionBase` (`internal/provider/proxmoxprovision.go:386-432`) no longer calls `runPlaybookPhase(base)`.
- [ ] The base template version (`templateVersion`, `:481-491`) records the image version instead of a playbook content hash; `templateGeneration` is bumped so existing templates rebuild once.
- [ ] `Profile.BaseImage` (`internal/profiles/profiles.go:73`) still works as a per-profile override, and the manifest supplies only the default.
- [ ] The finalize phase is unchanged: cloud-init identity, disk resize, start, then `runPlaybookPhase(finalize)`.
- [ ] Verification: `go build ./...` and `go vet ./...` pass; `go test ./internal/provider/...` passes against the mock PVE server. Paste the output.
- [ ] Verification: the opt-in e2e suite passes against a real PVE target — `PROXMOX_E2E=1 go test -tags proxmoxe2e ./internal/provider/...` with the environment from `e2e.env`. Paste the result.
- [ ] Verification: from the e2e run or a manual create, confirm no base-phase Ansible ran — grep the provisioning output for `TASK [base` and show it is absent, while finalize tasks are present.
- [ ] Verification: shell into a created Proxmox VM and confirm the baked tools are present (`node --version`, `go version`, `claude --version`). Paste the output.

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
