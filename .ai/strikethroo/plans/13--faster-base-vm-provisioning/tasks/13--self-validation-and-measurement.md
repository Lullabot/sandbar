---
id: 13
group: "validation"
dependencies: [11, 12]
status: "pending"
created: 2026-07-13
model: "sonnet"
effort: "high"
skills:
  - lima
  - ansible
complexity_score: 7
complexity_notes: "Verification gate for the whole plan. A cheap model rubber-stamping this defeats the gate."
---
# Execute the plan's Self Validation and record the before/after numbers

## Objective

Prove the plan actually delivered, on real hardware, and report the numbers honestly — including any tier whose win turned out to be negligible.

## Skills Required

- **lima** — driving real base builds, clones, and inspecting instance config.
- **ansible** — reading task profiles and play recaps.

## Acceptance Criteria

Execute every step of the plan's **Self Validation** section and capture the evidence. Specifically:

- [ ] **Before/after table.** Cold-build and warm-create wall-clock, measured after each tier, attributing the change to each. If the Tier 2 delta is negligible, **say so explicitly** — the plan's own diagnosis is that bandwidth is not the bottleneck, so this is a legitimate outcome, not a failure to hide.
- [ ] **Bootstrap slimmed**: `dpkg -l ansible ansible-core` in a fresh base shows `ansible-core` installed and the `ansible` bundle absent; the collections tree is gone (or present only in explicit profiling mode).
- [ ] **One apt transaction**: from the profiled task list of a cold build, exactly one `apt-get update`-bearing task and one install task ran in the base phase. Show the grep and its count.
- [ ] **Idempotence**: a second consecutive base-phase run reports `changed=0` — in particular for the replaced `locale-gen` and `authorized_keys` tasks.
- [ ] **Inner loop**: with a base built, a trivial playbook edit + `sand create` **re-applies in place** (no Debian image download, no base deletion) in a small fraction of the cold-build time. `sand create --rebuild` still destroys and rebuilds.
- [ ] **Stamp works outside git**: build from a non-git playbook dir (or the embedded FS); a stamp is written; an unchanged playbook does not rebuild; a changed playbook file does re-apply. And in a git checkout, a commit touching no playbook file does **not** mark the base stale.
- [ ] **Tool-set**: create with Java de-selected → `command -v java` absent, base build time down. Re-select → base reported stale and converged. De-select → the shrink advisory prints (pointing at the rebuild toggle / `--rebuild`) rather than silently leaving it installed. `tmux` present under every selection and `sand shell` still attaches.
- [ ] **Clones skip the upgrade**: a clone from a fresh base runs no `apt upgrade`/`dist-upgrade`. Artificially age the base stamp → the upgrade runs once **on the base**, announced, and the stamp is refreshed.
- [ ] **Bounce**: a create needing no reboot performs no stop+start (the phase is absent from the timing summary). `Reset` does not silently destroy a live tmux session. Touching `/var/run/reboot-required` does produce a bounce.
- [ ] **THE SECURITY INVARIANT**: for a cloned work VM, no *writable* host mount is present (`mount | grep -Ei 'virtiofs|9p|sshfs'`); the clone's `lima.yaml` has **no** cache-mount entry while the base's does. The automated test asserting this exists, passes, **and can fail** — remove the strip, watch it go red, restore it. Report that you did this.
- [ ] **Cache**: with a warm host cache, a `--rebuild` re-downloads no (or negligibly few) `.deb` files; the host cache dir is populated (show `ls` and its size).
- [ ] **Concurrency**: two concurrent `sand create` runs against a stale/over-age base — one takes the lock and re-applies/refreshes while the other reports waiting; the waiter, on acquiring the lock, observes the fresh stamp and does **not** redo the work; no base deletion occurs while the other is cloning. Repeat with one passing `--rebuild`. Then cancel a create mid-re-apply and confirm the lock is released and a subsequent create proceeds rather than hanging.
- [ ] **Timings visible**: in the TUI, press `l` to reopen the job log and confirm the per-phase durations and end-of-run summary are present; confirm the tile's progress bar still advances during the build (the timing lines did not reset it). Headlessly, the same summary appears on stdout.
- [ ] **CI parity**: `go vet ./...`, `go test ./...`, and the Ansible syntax check are green; `lima-e2e` passes on both the cold `--rebuild` build and the reuse create, with the `_apt` keyring assertion intact.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Requires a working Lima + QEMU/KVM host. Ideally both a Linux and a macOS host, since the Tier 2 mount decision hinges on macOS reverse-sshfs behavior.
- Enough disk for a base plus clones (cloning doubles the qcow2 footprint on non-CoW filesystems).

## Input Dependencies

- Tasks 11 and 12: everything is implemented and documented.

## Output Artifacts

- A before/after measurement table appended to the plan's Notes.
- A written record of every validation step's evidence.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**If no Lima host is available in this environment, say so plainly and do not fabricate results.** Report exactly which criteria could be verified statically (tests, greps, syntax checks, code inspection) and which require real hardware, and mark the latter as unverified. A validation gate that invents numbers is worse than no gate.

**Report negative results.** The plan explicitly anticipates that Tier 2's win may be negligible once Tier 1 has taken the network off the critical path. If that is what the numbers say, that is the finding. Write it down. Do not reach for a flattering framing.

**The security test must be shown to be able to fail.** Assert-only-green is not evidence. Delete the mount strip, run the test, confirm it goes red, restore the strip. Report having done it.

</details>
