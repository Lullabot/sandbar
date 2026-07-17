---
id: 7
group: "verification"
dependencies: [1, 4, 5, 6]
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "End-to-end verification gate across local+remote transports, plus a lifecycle/limactl-tolerance guard; must exercise real behavior, not mocks only."
skills:
  - go
  - molecule
---
# Integration tests: convergence, lifecycle, adoption, limactl tolerance, recreate

## Objective
Prove the plan's success criteria end-to-end: cross-mode and two-controller
convergence on the same managed set, deleted-for-free lifecycle, idempotent
adoption, the `limactl` tolerance guard, recreate correctness, and the
`LIMA_HOME` fix — exercising real behavior across local and remote(-loopback)
transports, not mocks alone.

## Skills Required
- `go` — integration tests in the existing harness.
- `molecule` — the project's Lima/loopback e2e harness (see `molecule/` and the
  remote-Lima-over-SSH loopback e2e).

## Acceptance Criteria
- [ ] Convergence test: a VM created in local mode is seen as managed via the
  remote provider pointed at the same host/user (marker at
  `<LimaHome>/<name>/sandbar.json` exists and decodes to the expected base +
  config); and a second controller with an empty `XDG_DATA_HOME` (no registry)
  sees the same managed set.
- [ ] Lifecycle test: after `limactl delete` (or sand delete), the marker is
  gone; a new VM reusing the name is listed but shows unmanaged until marked.
- [ ] Adoption test wired at integration level: seed a legacy registry, confirm
  adoption marks the live VM and is idempotent.
- [ ] `limactl` tolerance guard: with a marker present in a real instance dir,
  `limactl list --format json` and `limactl list <name>` both succeed and
  enumerate/parse the instance — asserted as a test so a Lima upgrade breaking
  tolerance fails CI.
- [ ] Recreate correctness: reset/recreate against a marked VM clones from the
  recorded base; with the marker removed, recreate is refused.
- [ ] `LIMA_HOME` assertion: with a non-default remote `lima_home`, discovery and
  marker reads resolve the same directory.
- [ ] Verification command: the new integration test target passes locally
  (e.g. `go test ./... -run 'Provenance|Convergence|Adopt|Tolerance' ` or the
  molecule scenario) with exit 0; state the exact command run and its result.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Extend the existing e2e/loopback harness rather than inventing a new one.
- Requires `limactl` available on the runner (present in this environment:
  2.1.3).
- Reuse the second-user loopback pattern for the two-controller/remote cases.

## Input Dependencies
- Task 1 (LIMA_HOME fix), Task 4 (manage/CLI rewire), Task 5 (UI rewire),
  Task 6 (adoption). This is the verification gate over the whole change.

## Output Artifacts
- Integration/e2e tests + a CI-wired `limactl` tolerance guard.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

**Test philosophy — "write a few tests, mostly integration."** Meaningful tests
verify custom business logic, critical paths, and edge cases specific to this
application. Test *your* code, not Lima. Favor integration/critical-path coverage
over per-method units; combine related scenarios into single tests rather than
one-per-operation. Do not add tests for third-party/framework behavior or trivial
getters. The scenarios below are the critical paths for THIS change; keep them
integration-first.

**Tolerance guard (fast, no full VM needed).** Mirror the manual check performed
during planning: create a temp dir shaped like a Lima home with a stopped/dummy
instance dir containing `sandbar.json`, run `limactl list` against it (or, in the
loopback e2e, drop a marker into a real instance dir), assert exit 0 and the
instance still parses. This is the guard that a future Lima version can't quietly
break the instance-dir marker choice.

**Convergence.** In the loopback harness: create via local provider; construct a
remote provider (loopback ssh, same user) and assert its `Provenance()`/board
roster includes the VM. For two-controller: point a second controller at a clean
`XDG_DATA_HOME` so its registry is empty, and assert the managed set matches —
proving markers, not the registry, drive convergence.

**Lifecycle.** Delete and assert `stat <LimaHome>/<name>/sandbar.json` →
not-exist; recreate same name unmarked → listed but roster shows unmanaged.

**Recreate.** Drive `RecreateBase`/reset against a marked VM → succeeds from the
marker's base; remove the marker → refused. Can be an integration test at the
`manage` level with a real Lima provider if a full VM is too slow.

**LIMA_HOME.** With `lima_home` set to a non-default remote path, assert the
remote `limactl list` and marker reads resolve the same dir (build on task 1's
behavior).

State the exact command(s) you ran and the observed pass/fail — a report is not
evidence. If a scenario is infeasible on the CI runner (e.g. no nested virt),
implement it against the loopback/local path and note the limitation explicitly
rather than skipping silently.
</details>
