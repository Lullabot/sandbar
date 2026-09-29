---
id: 6
group: "verification"
dependencies: [5]
status: "failed"
created: 2026-09-12
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
complexity_score: 7
complexity_notes: "Verification gate whose failure invalidates a core plan assumption; the rubric's risk floor requires capable models and high effort."
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

- [x] The corrected published amd64 image boots as a Lima instance on an amd64 host, reaching a usable shell with Lima's default cloud-init user and no explicit user override.
- [ ] The published arm64 image boots as a Lima instance on an arm64 host (Apple Silicon or Linux/arm64), reaching a usable shell.
- [ ] The published amd64 image imports on a real Proxmox VE target, starts, and its guest agent reports an IP address.
- [x] SSH host keys are confirmed to **regenerate on first boot** — they were removed by generalization, so their absence at boot must be self-healing, not a broken sshd.
- [x] `/etc/machine-id` is confirmed non-empty *after* first boot (systemd repopulates it), and two instances booted from the same image have **different** machine-ids.
- [ ] Verification: paste `limactl list` showing the instance running, plus in-guest `uname -m`, `systemctl is-system-running`, and `ls /etc/ssh/ssh_host_*` output for each arch.
- [ ] Verification: paste the PVE side — the import command, the VM starting, and `qm agent <vmid> network-get-interfaces` returning an address.
- [x] Verification: paste the two differing machine-ids from two instances of the same image.
- [x] A written finding records whether one image serves both consumers. If it does **not**, the finding states precisely what failed and what the per-provider variant would need to differ in — that is the deliverable in the negative case, and it is a successful outcome for this task.

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
- A merge-gate decision for PR 206: a positive finding makes the producer PR ready to merge; a negative finding keeps it open for a builder fix and a new immutable image release before provider wiring begins.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

**This task is a gate, and its most valuable outcome might be a failure.** Run it before tasks 08/09 begin so a negative result can redirect them cheaply. Do not paper over a partial success.

**PR boundary.** PR 206 stays open only through this verification. Do not keep it as the base of the entire implementation stack. Once this task passes, merge the producer independently; if a later integration exposes a different image defect, address that in a focused producer follow-up and publish a new tag.

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

## Verification finding (2026-09-29)

### Corrected release re-verification

`base-image-2026.09.29.164715`, built from producer commit `64a87ec`, fixes the
UID collision described below. A minimal Lima 2.1.3 YAML with no `user:` block
booted two independent x86_64 QEMU/KVM instances to `READY` as the host-derived
`andrew:1000` account. In both guests cloud-init reported `done`, systemd
reported `running`, the transient `claude` user and group were absent, the
expected `/etc/skel` payload had populated the new home, and the root filesystem
grew to the requested 24 GiB. The only cloud-init recoverable errors were
deprecation warnings in Lima-generated cloud config; there were no failed
systemd units.

The release URL, `manifest.json`, checksum sidecar, and GitHub asset metadata
all agree on amd64 SHA-256
`1db1103bf8095f03ac383b226653087f0eaef9d3d235d8c7ee6fc155b770d5bf`
and size 1,007,550,464 bytes. Lima accepted that digest before booting.

```text
$ limactl list
NAME           STATUS     SSH                VMTYPE    ARCH      CPUS    MEMORY    DISK
img-fixed-a    Running    127.0.0.1:34771    qemu      x86_64    2       4GiB      24GiB
img-fixed-b    Running    127.0.0.1:46745    qemu      x86_64    2       4GiB      24GiB

$ limactl shell img-fixed-a id -un
andrew
$ limactl shell img-fixed-a uname -m
x86_64
$ limactl shell img-fixed-a systemctl is-system-running
running
$ limactl shell img-fixed-a cat /etc/machine-id
1601a38b52fb4890ad1e41bdb78bcf1c
$ limactl shell img-fixed-b cat /etc/machine-id
5d1f5012638e4ff8afb6f85a8ec5f86b
$ limactl shell img-fixed-a sudo ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
256 SHA256:2NzfSBEZZT6WZQO35kSdYxqOOUkI3f2qHOkXMiG3csk root@lima-img-fixed-a (ED25519)
$ limactl shell img-fixed-b sudo ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
256 SHA256:QbTTF1TEWuKuoInKPlVesvPO+GqsdZ+8yU06aVZ38hg root@lima-img-fixed-b (ED25519)
```

The two machine IDs and host keys differ. Both disposable instances were
deleted after verification, and the temporary host KVM ACL was restored. The
amd64 Lima portion of this gate now passes without a consumer workaround.
Arm64 Lima and real Proxmox remain outstanding, so the task remains failed as a
whole until those external checks pass.

### Superseded first-release finding

The published amd64 asset from `base-image-2026.09.29.151245` is bootable by
Lima 2.1.3 on an x86_64 QEMU/KVM host. Lima fetched the release URL and accepted
the manifest digest
`sha256:20813c0d17cd67c81dd96535285701a9415d519931059813c6d80f27719196db`.
With the baked `claude` account selected explicitly, two independent instances
reached SSH and `systemctl is-system-running` returned `running`. The 20 GiB
source filesystem grew to the requested 24 GiB disk. Fresh SSH host keys and
different machine IDs appeared on each boot.

The default Lima user setting fails on this image. The published image contains
`claude:1000:1000:/home/claude` in `/etc/passwd`, while Lima generated
`name: andrew`, `uid: "1000"` for this host. The first boot's serial log reports
`Failed to create user andrew`; subsequent Lima boot scripts report
`usermod: user 'andrew' does not exist`, and SSH remains denied. The source
image's `/etc/machine-id` was empty, `/etc/ssh/ssh_host_*` absent, and
`/var/lib/cloud/instances` absent, so this is an account collision rather than
stale cloud-init state. The Lima consumer in task 08 must specify the baked
`claude` user, UID 1000, and home `/home/claude` (or the producer must change
the baked account layout); merely replacing the `images:` block as currently
described in task 08 will fail for normal UID-1000 workstation users.

The arm64 Lima and real Proxmox acceptance checks were not run: this host is
`x86_64`, and no arm64 host or configured Proxmox connection profile/PVE CLI
was available. The image contains the `qemu-guest-agent` package and Debian's
udev rule that starts its service when `org.qemu.guest_agent.0` appears, but
this is **not** evidence of a successful PVE import or agent IP report. One
image serving both consumers remains unproven; PR 206's dual-consumption merge
gate has not passed. No per-provider image variant is justified yet by the
available evidence.

### Commands and evidence

The disposable Lima 2.1.3 binary came from
`lima-vm/lima` release `v2.1.3`; `sha256sum -c SHA256SUMS --ignore-missing`
reported `lima-2.1.3-Linux-x86_64.tar.gz: OK`. `LIMA_HOME` and
`XDG_CACHE_HOME` pointed inside
`/var/tmp/sandbar-lima-verify-2026.09.29.151245` for every Lima command.
The initial YAML used the published amd64 URL, the digest above, `vmType: qemu`,
`arch: x86_64`, `cpus: 2`, `memory: 4GiB`, and `disk: 24GiB`. The successful
YAML added:

```yaml
containerd:
  system: false
  user: false
user:
  name: claude
  uid: "1000"
  home: /home/claude
```

Commands: `limactl start --name img-test-a --tty=false img-test.yaml`
(default user), then `limactl start --name img-test-claude --tty=false
img-test-claude.yaml` and `limactl start --name img-test-claude2 --tty=false
img-test-claude.yaml` (explicit baked user). The successful starts ended in
`READY. Run limactl shell ... to open the shell.` The default-user boot reached
multi-user mode, but Lima kept reporting `Permission denied (publickey)` for
`andrew` and never reached `READY`.

```text
$ limactl list
NAME                STATUS     SSH                VMTYPE    ARCH      CPUS    MEMORY    DISK
img-test-a          Stopped    127.0.0.1:41571    qemu      x86_64    2       4GiB      24GiB
img-test-claude     Running    127.0.0.1:39329    qemu      x86_64    2       4GiB      24GiB
img-test-claude2    Running    127.0.0.1:36735    qemu      x86_64    2       4GiB      24GiB

$ limactl shell img-test-claude uname -m
x86_64
$ limactl shell img-test-claude systemctl is-system-running
running
$ limactl shell img-test-claude ls /etc/ssh/ssh_host_*
/etc/ssh/ssh_host_ecdsa_key
/etc/ssh/ssh_host_ecdsa_key.pub
/etc/ssh/ssh_host_ed25519_key
/etc/ssh/ssh_host_ed25519_key.pub
/etc/ssh/ssh_host_rsa_key
/etc/ssh/ssh_host_rsa_key.pub
$ limactl shell img-test-claude df -h /
Filesystem      Size  Used Avail Use% Mounted on
/dev/vda1        24G  2.5G   20G  12% /

$ limactl shell img-test-claude cat /etc/machine-id
726c6ff974624b7e930a4086bc97e408
$ limactl shell img-test-claude2 cat /etc/machine-id
b5df1a9769bd4ee5a9cc73f9ffc9c1cb
$ limactl shell img-test-claude ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
256 SHA256:+w+KQHdvTLz3h4f0ubbZuZ0LzJb1VqRaxUndwAzbpbg root@lima-img-test-claude (ED25519)
$ limactl shell img-test-claude2 ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
256 SHA256:idEC2Vg3zdHlOm0upX5AVDHCYGzaGU9Mku9Vys72lDM root@lima-img-test-claude2 (ED25519)
```

`img-test-claude2` separately returned `x86_64`, `running`, the same six
host-key paths, and a 24 GiB root filesystem. No `qm importdisk`, PVE VM start,
or `qm agent ... network-get-interfaces` command was run.

## Noteworthy Events

- [2026-09-29] `base-image-2026.09.29.164715` passed the amd64 Lima gate with
  the default cloud-init user; no task 08 user override is required. Task 06
  remains failed only because arm64 Lima and real PVE checks require the
  external targets offered by the user.
- [2026-09-29] The superseded first release failed for Lima's default account
  because it baked `claude:1000`; producer commit `64a87ec` generalized the
  login into `/etc/skel` and the corrected release above verifies the fix.
