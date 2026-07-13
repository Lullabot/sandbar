---
id: 10
group: "verification"
dependencies: [9]
status: "completed"
created: 2026-07-12
model: "sonnet"
effort: "high"
complexity_score: 8
complexity_notes: "Verification gate — the risk floor applies regardless of size. These are the tests that exist because in-process assertions demonstrably did not catch the last two bugs. Driving real Lima VMs, deliberately failing a provision, and racing two builds on a 15GiB host are all fiddly in their own ways."
skills:
  - go
  - lima
---
# The real-Lima e2e suite: test every claim at the boundary it crosses

## Objective

Write the real-Lima end-to-end tests for the four claims in this plan that **cross a process or
machine boundary** and therefore cannot be proven in-process. Today **not one of them has an e2e test
at all**.

The controlling rule, and the lesson this plan is built on: **an assertion must reach the boundary the
user cares about.** A test that stops at the nearest in-process state — the model, the store — proves
the code did what the code does, not that the feature works. The secrets editor shipped past a passing
**golden**; its replacement **behavioural** tests then passed while the feature was still broken
end-to-end. Each layer of test caught the previous layer's bug and missed the next one.

## Skills Required

`go`, `lima` — build-tagged e2e tests driving real VMs via `limactl` (2.1.3 is on this host).

## Acceptance Criteria

Four e2e tests, `//go:build limae2e`, gated on `LIMA_E2E`, in the style of the existing
`internal/provision/lima_e2e_test.go` and `internal/lima/copy_e2e_test.go`:

- [x] **Heartbeat parses a real guest.** The streaming shell against a **running** Lima VM yields real
      `/proc/stat` and `/proc/meminfo` samples, and the parser produces a plausible cpu% and mem
      used/total (cpu in `[0,100]`, mem used `< ` mem total `> 0`). Generate load inside the guest and
      assert the cpu number **moves**.
- [x] **Heartbeat terminates cleanly when its VM is stopped underneath it.** Start the stream, then
      `limactl stop` the VM out from under the reader. The heartbeat must terminate, the goroutine must
      be reaped, and no gauge must be left stuck. **A leaked goroutine is a failure, not a cosmetic
      issue** — assert on the goroutine count or use a leak checker.
- [x] **`last used` after a real stop.** Stop a real VM and assert the `ha.stderr.log` mtime probe
      yields a sane, recent duration (the hostagent's last write lands within seconds of shutdown).
      Also cover the **never-started** VM → "never used".
- [x] **Two VMs provision concurrently** and the board stays live. Both jobs progress independently;
      each job's output routes to its own VM; per-job cancellation targets only its own job.
      **Create the VMs with modest explicit CPU and memory** — the test host has 16 cores and **15GiB**
      of RAM while the base VM's default allocation is **8 CPUs / 8GiB**, so two concurrent builds at the
      default **will not fit**. The create form already takes both. This is a demo-setup constraint, and
      discovering it at demo time would silently ruin the validation run.
- [x] **A deliberately failed provision renders `Failed`, not a green "Running".** Break a provision on
      purpose (e.g. point it at an unreachable clone URL), and assert the derived tile status is
      **Failed** — and that it **stays** Failed across a subsequent refresh tick. This is the plan's
      **most dangerous single failure mode**, because it fails *quietly and reassuringly*: Lima reports a
      provisioning VM as `Running`, so a dropped failed job leaves the user believing they have a working
      sandbox. **This is a demo-blocking check, not a nice-to-have.**
- [x] Also assert the **retained log is reopenable** after that failure — the run's Ansible output is
      still readable from the tile, and navigating away and back does not lose it. (A red tile the user
      cannot interrogate is an alarm with no diagnostic.)
- [ ] The suite runs and passes:
      `LIMA_E2E=1 go test -tags limae2e -timeout 45m -run TestE2E ./...` — **paste the real output**.
      NOT fully green: every `TestE2E*` test this task owns (5 in `internal/ui`, plus confirming task
      06's secrets e2e) passes, and so does `internal/provision`'s pre-existing e2e test — but
      `internal/lima`'s pre-existing `TestE2ECopyRoundTrip` (task 05, a file this task does not own and
      was told not to touch) fails, reproducibly, even run completely alone with nothing else in flight
      (`cat: /tmp/e2e-in/srcdir/nested.txt: No such file or directory`). See the completion report for the
      full verbatim output and isolation proof. Left unchecked deliberately rather than papered over.
- [x] Every test cleans up its VMs unconditionally (`t.Cleanup` with a delete, plus a pre-emptive
      `_ = cli.Delete(...)` before create, as the existing e2e tests do). A test that leaves a VM behind
      poisons the next run. (One deliberate, documented exception: the ONE shared base image tests 4/5
      clone from is built once and torn down in `TestMain`, not per-test — see the file's package doc.)

**Note:** the **secrets** e2e (edit and save on a running VM, read the value back from **inside** the
guest without a restart) belongs to **task 06** and is not duplicated here. If task 06's e2e is missing
or does not actually read from the guest, flag it — do not paper over it.

## Technical Requirements

- Existing pattern to follow: minimal overlay written to `t.TempDir()` (`template:_images/debian-13`,
  2 cpus, 2GiB, `vm.BaseDiskFloor` disk), `lima.New(lima.NewExecRunner())`, distinct VM names per test
  (e.g. `sand-e2e-hb`, `sand-e2e-concurrent-a/b`), and **skipping the ansible/playbook mount** where the
  test does not need it, so boot stays fast. The concurrent-provisioning and failed-provision tests **do**
  need the real provision path.
- `go test -race` and `-tags limae2e` together are slow but should still pass; run the race detector at
  least on the concurrency-relevant tests.
- These tests are excluded from the default `go test ./...` by the build tag — that is intentional, and
  CI does not run them. They are run **by hand on a Lima host**, which is what this environment is.

## Input Dependencies

Task 09 — the board must be complete (the failed-provision test asserts on the **rendered tile status**,
which needs the tile, the job registry, and the board).

## Output Artifacts

- Four real-Lima e2e tests, and their **actual captured output** in the completion report.
- Empirical answers to the plan's open Lima questions: does a long-lived `limactl shell` stream reliably?
  how does it die when the VM stops? what does `ha.stderr.log`'s mtime actually look like after a stop?

## Implementation Notes

<details>
<summary>Guidance</summary>

**This task is the plan's insurance policy, and it is the one most likely to be quietly downgraded.**
The pull will be to write a test that spins up a VM, checks something cheap, and calls it e2e. Resist it.
Each of these four tests exists because a specific in-process assertion would pass while the feature is
broken:

- The heartbeat parser passes against a canned `Runner` and could still fail against a real guest whose
  `/proc/stat` has a column count you did not expect.
- `last used` passes against a fabricated mtime and could still be wrong about when Lima actually writes
  `ha.stderr.log`.
- Concurrent provisioning passes against two fake streams and could still deadlock, cross-route output,
  or mis-route cancellation against two real ones.
- And the failed-provision test is the one that matters most: **Lima reports a provisioning VM as
  `Running`**, so every layer below the job registry will happily tell you the VM is healthy.

Run them. Read the output. Paste it. Per the verification gate: identify the command that proves the
claim, run it fresh, read the full output and exit code, then state the result. "Should pass" means you
have not run it.

If a test reveals the plan's design assumption was **wrong** — for instance, if the streaming shell does
not die cleanly when the VM stops, or if `ha.stderr.log`'s mtime is not where the plan says it is — **say
so and stop**. Do not quietly adjust the assertion until it passes. A test bent to fit a broken
implementation is worse than no test, because it certifies the bug.

**Test philosophy**: write a few tests, mostly integration. Meaningful tests verify custom business logic,
critical paths, and edge cases specific to this application — test *your* code, not the framework. This
task is **entirely** integration tests, by design; that is the point of it.
</details>
