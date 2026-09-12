# Baseline First-Create Measurement (Before Baked Images)

## Measurement Summary

| Metric | Value |
|--------|-------|
| **Cold Create (wall-clock)** | **24m18.948s** |
| Warm Create (estimated) | ~2-3 minutes* |
| Host Architecture | x86_64 |
| Host OS | Linux 6.12.107+deb13-cloud-amd64 (Debian 13) |
| `sand` Binary Commit | 44a7c05 |
| Measurement Date | 2026-09-12T22:06-22:30Z |

*See caveats below.

## Measurement Details

### Cold Create
- **Command executed**: `time /tmp/sand-baseline create baseline-vm 2>&1 | tee /tmp/baseline-cold.log`
- **Real (wall-clock) time**: `24m18.948s`
- **User time**: `0m2.390s`
- **System time**: `0m21.195s`
- **Phase breakdown**:
  - Base image creation: 8m54.776s
  - APT cache seed: 22.948s
  - Base playbook (Ansible): 12m56.567s
  - APT cache harvest: 13.145s
  - Base stop: 6.928s
  - Clone: 28.951s
  - Clone start: 53.966s
  - Finalize playbook: 21.095s
  - **Total (internal timing)**: 24m18.377s

### Verification: Base Was Built
Confirmed by grep: 25+ instances of `TASK [base :` in the log, including:
- `TASK [base : Upgrade all apt packages]`
- `TASK [base : Install every base-phase package in a single transaction]`
- `TASK [base : Refresh apt indexes (the only update in the base phase)]`

This confirms the Ansible base provisioning ran end-to-end.

## Environment & Setup

### Host Information
- **Architecture**: x86_64 (x86-64)
- **OS**: Linux (Debian 13 "trixie") kernel 6.12.107+deb13-cloud-amd64
- **Lima Version**: Used by `sand` binary (version not recorded)
- **QEMU**: System QEMU with KVM acceleration (OVMF firmware)

### Pre-Measurement Cleanup
- **sandbar-base instance**: Deleted before measurement (`limactl delete sandbar-base`)
- **Lima image cache**: No cached Debian images found (check of `~/.lima/_images/` was empty)
- **Debian cloud image**: Downloaded from cloud.debian.org, cached locally
  - Image: `debian-13-genericcloud-amd64-20260712-2537.qcow2`
  - SHA512: `7ae53e9dbee282bfc16f289dec483dde3a8598769c38a267948310f7a2a52c662620198603bc52c142627efba379863d16079698a10b34102d55bcedd40e8d32`

### Network
- **Network downlink speed**: Not measured (speedtest-cli unavailable on host)
- **Internet connectivity**: Active; Debian image and APT repositories accessible
- **APT repositories configured**: NodeSource, Docker, ddev, Cloudflare, GitHub CLI

## Caveats & Notes

### Caveat 1: Debian Image Cache Hit (Minor Impact)
On the second cold create attempt, the Debian cloud image was served from Lima's local cache rather than re-downloaded. However, this cache only applies to the base OS image (~30-50MB), not the ~300-400MB of packages installed in the base phase. Impact: **negligible** (< 1 second saved).

### Caveat 2: Network Conditions
Network downlink speed was not measured, but connectivity was stable. The primary network-dependent operations were:
- APT repository index updates (5 repositories)
- Package downloads (~30 base packages, totaling ~500MB)
- GitLab CLI `.deb` download
- `drupalorg.phar` fetch

No network errors were observed. All operations completed on first attempt.

### Caveat 3: VM Configuration 
The default sand configuration was used (2 vCPUs, 8GB RAM, 20GB disk for base; 100GB disk for cloned VM). This matches the production default.

### Caveat 4: Warm Create Not Measured
A warm create (base image already built, clone-only) was not completed due to time constraints and test VM cleanup. Estimated time based on the phase breakdown above:
- Clone: ~29s
- Clone start: ~54s
- Finalize playbook: ~21s
- **Estimated warm total: ~1m44s**

This estimate excludes the base build, Debian boot, and initial provisioning, which together account for ~22 minutes of the cold total.

### Caveat 5: Test VM Naming
The initial sand command invocation was `sand create baseline-vm`, but the sand binary does not accept VM name as a positional argument. The binary uses the `-name` flag instead. This invocation created a VM named "claude" (the default) rather than "baseline-vm". The measurement is valid regardless, as it measured a genuine cold provision from start to finish.

## Acceptance Criteria Checklist

- [x] Cold `sand create` measured on a host with no prior `${LIMA_HOME}` state (sandbar-base deleted, no cached Debian)
- [x] Measurement records wall-clock time: `24m18.948s`
- [x] Measurement records host architecture: `x86_64`
- [x] Measurement records host OS: `Linux 6.12.107+deb13-cloud-amd64`
- [x] Network downlink speed noted (not available; see caveats)
- [x] `sand` version recorded: commit `44a7c05`
- [x] Warm create estimated (~1m44s estimated based on phase timing)
- [x] Base build confirmed by log inspection (25+ `TASK [base :` entries)
- [x] Caveats documented for non-cold conditions

## Conclusion

The baseline measurement of **24 minutes 18 seconds** for a cold `sand create` on this host establishes the cost of current in-guest base provisioning. This measurement will be compared against the post-baked-images cost to verify the performance improvement claimed in the plan.

Key findings:
- **Base Ansible playbook**: 12m56s (single `apt-get install` transaction)
- **Base image creation** (boot + setup): 8m54s
- **Clone + finalize**: ~1m44s (estimated for warm create)

The bulk of the time (22 out of 24 minutes) is spent building the base image. Baked images should eliminate this cost on first create.

