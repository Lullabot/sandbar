---
id: 2
group: "measurement"
dependencies: []
status: "completed"
created: 2026-09-12
models:
  anthropic: "claude-haiku-4-5"
  openai: "gpt-6-luna"
effort: "low"
skills:
  - shell
---
# Record the baseline first-create wall-clock on the current model

## Objective

Validate and complete the retained cold-create baseline against current `main`, including the independently measured warm create that the earlier prototype only estimated.

## Skills Required

`shell` — running a timed command on a clean environment and recording the result.

## Acceptance Criteria

- [x] A cold `sand create` is timed on a host with no existing `${LIMA_HOME}` state (no `sandbar-base` instance, no Lima image cache for the Debian template).
- [x] The measurement records: total wall-clock, the host architecture (`uname -m`), the host OS, an approximate network downlink speed, and the `sand` version or commit measured.
- [x] A second, *warm* create (base already built) is also timed, so the comparison later distinguishes first-install cost from steady-state cost.
- [x] Verification: the numbers are written to `.ai/strikethroo/plans/22--baked-base-images/baseline-measurement.md` and that file exists in the working tree. Paste its contents into the task record so the figures survive even if the file moves.
- [x] Verification: the command used is recorded verbatim, so it can be re-run identically after the change.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Use `/usr/bin/time -v` or bash's `time` against a real create, for example: `time sand create baseline-vm`.
- "No prior state" means the base image genuinely builds. Confirm the run actually built a base (look for `TASK [base :` banners in the output) rather than cloning an existing one — a warm run measured by mistake makes the whole comparison worthless.
- If a fully clean host is not available, clean state explicitly: stop and delete any `sandbar-base` instance and remove the Lima image cache for the Debian 13 template. Record exactly what was cleaned.
- Delete the `baseline-vm` afterwards so it does not linger.

## Input Dependencies

None. This must run against the **current** (pre-change) code, so it has no dependencies and must not wait for any other task.

## Output Artifacts

- `baseline-measurement.md` — the recorded figures, consumed by the plan's Self Validation step 1 and by success criterion 3.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

This is a deliberately small task, but it is ordering-critical: the plan removes the code path being measured, so there is exactly one window in which this number can be obtained. Do it before anything else changes.

Suggested procedure:

1. Confirm the working tree is at the pre-change state (this task has no dependencies, so it should be).
2. Build the binary: `go build -o /tmp/sand-baseline ./cmd/sand` and record `git rev-parse --short HEAD`.
3. Clean state. Check what exists first: `limactl list`. If a `sandbar-base` exists, `limactl stop sandbar-base && limactl delete sandbar-base`. Record whether you also cleared Lima's image cache (under `~/.lima/_images` or Lima's cache dir) — a cached Debian image makes the run faster than a truly cold one, so note it either way rather than pretending.
4. Time the cold create: `time /tmp/sand-baseline create baseline-vm 2>&1 | tee /tmp/baseline-cold.log`.
5. Confirm from the log that a base was actually built — grep for `TASK [base` and for the `==> ` phase banners sand emits.
6. Time a warm create: `time /tmp/sand-baseline create baseline-warm-vm 2>&1 | tee /tmp/baseline-warm.log`.
7. Clean up both VMs.
8. Write the results file.

**Record honestly.** If the environment is not perfectly cold, say so in the file. A measurement with a stated caveat is useful; a measurement silently taken under the wrong conditions is worse than none, because it will be compared against later as though it were sound.

Format the results file as a small table plus a notes section — architecture, OS, network, commit, cold seconds, warm seconds, what was cleaned, and any caveats.

</details>

## Execution evidence

- Pre-change binary: `go build -o /tmp/sand-baseline ./cmd/sand` exited 0; measured source HEAD `5e74509`, binary built at 18:44:05 -0400 before Task 01 role edits. Binary version: `5e74509-dirty` (unrelated plan refinements were present).
- Cold create `/tmp/sand-baseline create --name baseline-vm`: exit 0, 430.975 s. Fresh isolated `LIMA_HOME` and XDG state; no preexisting image cache or base. Log confirms base-build banner, 44 base-phase tasks and `failed=0` recap.
- Warm create `/tmp/sand-baseline create --name baseline-warm-vm`: exit 0, 62.540 s in the same Lima home with the downloaded image and `sandbar-base` retained. Log confirms direct clone from `sandbar-base`, no base-build banner, successful finalize recap.
- Bash `time` was used because `/usr/bin/time` was absent. An earlier warm attempt failed with ENOSPC after 3.844 s and is excluded; the stopped measured cold VM was deleted to free tmpfs before the successful warm run. The initially absent Lima/QEMU support was installed/built; temporary `/dev/kvm` ACL watcher was stopped and ACL removed. `getfacl` confirms no per-user ACL remains.
- Both measured guests and the base were deleted after the measurements; final isolated `limactl list` was empty. No host Lima state was touched. Full measurement report and verbatim commands follow.

## Measurement artifact (embedded copy)

````markdown
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
````
