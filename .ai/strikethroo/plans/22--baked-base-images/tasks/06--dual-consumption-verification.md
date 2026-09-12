---
id: 6
group: "verification"
dependencies: [5]
status: "pending"
created: 2026-09-12
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Verification gate whose failure invalidates a core plan assumption; per the rubric's risk floor, gates never go below sonnet + high."
skills:
  - lima
  - proxmox
---
# Verify one image boots under Lima and imports on Proxmox

## Objective

Prove the plan's central architectural assumption before any provider wiring is built on it: that a single published image is consumable by both Lima (on both architectures) and Proxmox. If it is not, the fallback is per-provider variants from one build — a change that is far cheaper to discover now than after tasks 08 and 09 are written.

## Skills Required

`lima` for booting the image as a Lima instance; `proxmox` for importing it on a real PVE target.

## Acceptance Criteria

- [ ] The published amd64 image boots as a Lima instance on an amd64 host, reaching a usable shell.
- [ ] The published arm64 image boots as a Lima instance on an arm64 host (Apple Silicon or Linux/arm64), reaching a usable shell.
- [ ] The published amd64 image imports on a real Proxmox VE target, starts, and its guest agent reports an IP address.
- [ ] SSH host keys are confirmed to **regenerate on first boot** — they were removed by generalization, so their absence at boot must be self-healing, not a broken sshd.
- [ ] `/etc/machine-id` is confirmed non-empty *after* first boot (systemd repopulates it), and two instances booted from the same image have **different** machine-ids.
- [ ] Verification: paste `limactl list` showing the instance running, plus in-guest `uname -m`, `systemctl is-system-running`, and `ls /etc/ssh/ssh_host_*` output for each arch.
- [ ] Verification: paste the PVE side — the import command, the VM starting, and `qm agent <vmid> network-get-interfaces` returning an address.
- [ ] Verification: paste the two differing machine-ids from two instances of the same image.
- [ ] A written finding records whether one image serves both consumers. If it does **not**, the finding states precisely what failed and what the per-provider variant would need to differ in — that is the deliverable in the negative case, and it is a successful outcome for this task.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Lima expects a cloud-init-capable image with a working serial console and functional `growpart`/`resize` on first boot. Debian genericcloud nominally provides all three — this task is what turns "nominally" into evidence.
- PVE expects an importable disk with `qemu-guest-agent` already present, because sand learns a VM's IP **only** from the guest agent (the only address a pure-API PVE client can read). The agent is installed by the base playbook now rather than by the old narrow bake — confirm it is actually there and enabled.
- Test by pointing Lima at the image directly (a minimal hand-written Lima YAML with an `images:` block and the published URL plus digest) rather than waiting for task 08's overlay changes — this task must be able to fail *before* that work is done.
- For PVE, a manual `qm importdisk` / `qm set` against the downloaded image is sufficient; do not wait for task 09.

## Input Dependencies

- Task 05's published release with both images and their digests.

## Output Artifacts

- A written verification finding — consumed by tasks 08 and 09 as confirmation (or as a revised requirement) that one image serves both providers.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

**This task is a gate, and its most valuable outcome might be a failure.** Run it before tasks 08/09 begin so a negative result can redirect them cheaply. Do not paper over a partial success.

**Minimal Lima test.** Write a throwaway YAML like:

```yaml
images:
- location: "https://github.com/Lullabot/sandbar/releases/download/<tag>/sandbar-base-debian-13-amd64.qcow2"
  arch: "x86_64"
  digest: "sha256:<digest>"
cpus: 2
memory: "4GiB"
disk: "20GiB"
```

then `limactl start --name img-test --tty=false ./img-test.yaml`. Lima will download, verify the digest, and boot. A digest mismatch here is itself a useful finding (it would mean the manifest and the asset disagree — check against task 05's last acceptance criterion).

**Things most likely to go wrong, in rough order of likelihood:**

1. **Disk growth.** The image was resized during the build; confirm the guest filesystem actually fills the configured disk on first boot (`df -h /`). If `growpart` does not run, Lima instances will be mysteriously small.
2. **SSH host keys.** Generalization removed them. On Debian the `ssh` unit regenerates via `ssh-keygen -A` in a systemd generator or `ExecStartPre`. If the image was built in a way that disabled that, sshd will fail to start and Lima will hang waiting for SSH — which looks like a boot failure, not a key problem. Check `journalctl -u ssh` in the guest console if it hangs.
3. **cloud-init state.** The build never boots the guest specifically so cloud-init's first-boot behaviour is untouched. If something during the chroot build ran cloud-init or wrote `/var/lib/cloud`, first boot will skip its setup. Confirm `/var/lib/cloud/instances` is empty in the built image if boot misbehaves.
4. **Guest agent on PVE.** Confirm `systemctl is-enabled qemu-guest-agent` inside the image. The old workflow enabled it explicitly with `systemctl --root=`; make sure the new build still does (this is exactly the kind of thing task 01's audit should have caught).
5. **arm64 console.** Serial console setup differs on arm64. If the arm64 instance boots but Lima cannot reach it, suspect console/serial configuration before suspecting the tools.

**Machine-id check.** Boot two instances from the same image and compare `cat /etc/machine-id`. If they match, the generalization did not take (or systemd is not regenerating), and every clone will fight over the same DHCP lease — the exact bug `generalizeScript` was written to fix. This is a hard failure, not a nit.

**Record everything.** Tasks 08 and 09 will be written against this finding, so ambiguity here becomes rework there.

</details>
