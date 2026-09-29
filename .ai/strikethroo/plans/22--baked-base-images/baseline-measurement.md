# Baseline first-create measurement

Measured 2026-09-28 against the pre-Task-01 `sand` binary built from HEAD
`5e74509`. The checkout contained unrelated plan refinements, so the version
string reported `5e74509-dirty`; the measured binary was built before the
Task 01 role and documentation edits (binary build: 18:44:05.579 -0400; role
edit: 18:44:44.087 -0400; documentation edit: 18:45:44.279 -0400).

| Measurement | Result |
| --- | --- |
| Host architecture | `x86_64` |
| Host OS | Debian GNU/Linux 13 (trixie), Linux 6.12.107+deb13-cloud-amd64 |
| Approximate downlink | 10,000,000 bytes in 0.197479 s (~405 Mbps), one-shot Cloudflare download probe |
| Measured sand | HEAD `5e74509`; `/tmp/sand-baseline --version` → `5e74509-dirty` |
| Cold create wall clock | 430.975 s (7m 10.975s) |
| Warm create wall clock | 62.540 s (1m 2.540s) |

## Commands and conditions

The binary was built with `go build -o /tmp/sand-baseline ./cmd/sand` (exit
0). The checkout's plan refinements made the version dirty, but the binary was
built before the Task 01 role edits. The actual measured commands were:

```sh
TIMEFORMAT='wall_seconds=%3R user_seconds=%3U system_seconds=%3S cpu_pct=%P'
time /tmp/sand-baseline create --name baseline-vm 2>&1 | tee /tmp/sand-baseline-task02-final-cold.4k6M9j/cold.log
time /tmp/sand-baseline create --name baseline-warm-vm 2>&1 | tee /tmp/sand-baseline-task02-final-cold.4k6M9j/warm-success.log
```

Each run used a fresh isolated `LIMA_HOME`, XDG data/config/state/cache dirs,
and `TMPDIR` beneath `/tmp/sand-baseline-task02-final-cold.4k6M9j`; no existing
Lima image cache or base was present before cold create. Lima 2.1.3 and the
Debian 13 image were acquired into that isolated state. The cold log shows the
base-image build banner and a distinct base-phase Ansible play with 44
`TASK [base : ...]` entries and a successful recap (`failed=0`), followed by
the clone/finalize phases. The successful warm log begins by cloning
`baseline-warm-vm` from the existing `sandbar-base`, has no base-build banner,
and its finalize play recap is successful (`failed=0`).

The warm time is an independent successful run from the same Lima home after
the base was built. An earlier warm attempt in that home failed after 3.844 s
because the 16 GiB `/tmp` tmpfs ran out of space; it is not included in the
warm measurement. Per approval, the measured stopped cold VM was deleted to
free tmpfs, while `sandbar-base` and the downloaded image cache were retained
for the successful warm run. The cold and warm VMs and then the base were
deleted after measurement. Final `limactl list` was empty (exit 0).

This machine initially lacked `limactl`, QEMU system binaries, OVMF, sshfs,
and xorriso. Lima 2.1.3 was built in `/tmp`; QEMU 10.0.13, OVMF, sshfs and
xorriso were installed to enable the measurement. `/dev/kvm` required a
temporary per-user ACL for `andrew`; an ACL watcher was used during create
because the ACL disappeared between QEMU launches. The watcher was stopped
after create and the ACL was removed with `sudo setfacl -x u:andrew /dev/kvm`
(exit 0); final `getfacl` confirmed no named `andrew` entry remained.

Measured shell wall times (Bash `time`, because `/usr/bin/time` was absent):

| Run | Exit | Wall | User | System | CPU |
| --- | ---: | ---: | ---: | ---: | ---: |
| Cold create | 0 | 430.975 s | 3.257 s | 8.825 s | 2.80% |
| Warm create | 0 | 62.540 s | 0.674 s | 4.120 s | 7.66% |

Internal phase timing corroborated the wall clock. Cold: base image creation
112.730 s, base playbook 242.163 s, apt cache harvest 10.321 s, stop 5.651 s,
clone 4.476 s, clone start 27.709 s, finalize playbook 27.093 s. Warm: clone
3.619 s, clone start 30.204 s, finalize playbook 27.998 s. A failed preflight
attempt caused by missing OVMF and a KVM-permission retry are not measured
runs. No host Lima state was used or modified.
