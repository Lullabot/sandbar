---
id: 2
group: "measurement"
dependencies: []
status: "completed"
created: 2026-09-12
model: "haiku"
effort: "low"
skills:
  - shell
---
# Record the baseline first-create wall-clock on the current model

## Objective

Measure and durably record how long a cold `sand create` takes today, on a machine with no prior sandbar state, so the plan's central success criterion — that the new model is *faster* — can be evaluated. This measurement cannot be recovered once the provider wiring lands, so it must happen first.

## Skills Required

`shell` — running a timed command on a clean environment and recording the result.

## Acceptance Criteria

- [ ] A cold `sand create` is timed on a host with no existing `${LIMA_HOME}` state (no `sandbar-base` instance, no Lima image cache for the Debian template).
- [ ] The measurement records: total wall-clock, the host architecture (`uname -m`), the host OS, an approximate network downlink speed, and the `sand` version or commit measured.
- [ ] A second, *warm* create (base already built) is also timed, so the comparison later distinguishes first-install cost from steady-state cost.
- [ ] Verification: the numbers are written to `.ai/strikethroo/plans/22--baked-base-images/baseline-measurement.md` and that file exists in the working tree. Paste its contents into the task record so the figures survive even if the file moves.
- [ ] Verification: the command used is recorded verbatim, so it can be re-run identically after the change.

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
