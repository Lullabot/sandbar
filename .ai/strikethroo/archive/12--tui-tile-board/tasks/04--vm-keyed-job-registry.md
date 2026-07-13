---
id: 4
group: "live-behaviour"
dependencies: [1]
status: "completed"
created: 2026-07-12
model: "opus"
effort: "xhigh"
complexity_score: 9
complexity_notes: "The codebase's first real concurrency. Generalizes a single reader/output/cancel triple into a VM-keyed registry of concurrent jobs, adds Ansible progress parsing that does not exist today, and owns the Building/Failed statuses on which the whole board's honesty depends. Races, leaked jobs, and mis-routed cancellation are the failure modes — exactly the kind that pass unit tests and fail demos."
skills:
  - go
  - concurrency
---
# The VM-keyed concurrent job registry

## Objective

Un-freeze the UI during provisioning. This is the **load-bearing change** of the plan and
the demo depends on it.

Today a single `beginStream` reader/output/cancel triple serves exactly one job, and
`m.running` freezes every key for minutes. Generalize it into a registry **keyed by VM
name**, so several provisions can be in flight at once while the board stays fully
interactive — and so a building VM can render an in-place progress bar on its own tile.

The registry is also the **source of the tile's Building and Failed statuses**, not just its
progress bar. Lima reports only `Running` and `Stopped`; a provisioning VM is `Running` to
Lima and Ansible is just a process inside it. Without this registry, **a failed provision
renders as a reassuring green "Running" tile** — the single most dangerous failure mode in
the plan, because it fails quietly and reassuringly.

## Skills Required

`go`, `concurrency` — goroutine lifecycle, context cancellation, race-free state shared
between N readers and the Bubble Tea update loop.

## Acceptance Criteria

- [x] A new `internal/ui/jobs.go` defines a registry keyed by VM name. Each job carries: its
      state (`running` | `failed` | `succeeded`), its `context.CancelFunc`, its accumulated
      **log buffer**, and its parsed progress (current Ansible role/task and an `n/total`
      count).
- [x] **Concurrency is correct.** Two provisions can run simultaneously; each job's output
      routes to **its own** VM's buffer and to no other. Cancellation is **per-job** — cancelling
      one does not touch the other. `go test -race ./...` passes, and there is an explicit
      two-jobs-in-flight test.
- [x] **`m.running` no longer gates the keyboard.** `grep -rn "m.running" --include=*.go internal/ui`
      returns zero hits (or only within the job registry's own internals). Every key remains
      live while a job runs.
- [x] **Failed jobs are retained, not discarded** — a failed job stays in the registry, with its
      log, until the user acts on it. This is what makes the Failed status sticky. There is a
      test asserting a failed job survives a subsequent `vmsLoadedMsg` refresh tick.
- [x] **The retained run's log is reopenable.** A state-gated command (registered via task 02's
      command registry, `enabledFor` = "this VM has a retained run") reopens the last run's
      output in the existing progress viewport. Navigating away and back must not lose it.
- [x] **Ansible progress is parsed** — this does not exist today; the progress pane is currently a
      raw byte stream. Parse Ansible's output for the current play/task and a task count so a
      tile can render `ansible: docker · 7/19`. Unit-test the parser against **captured real
      Ansible output** (a testdata fixture), including the case where output is split across
      arbitrary read-buffer boundaries (the reader reads 4096 bytes at a time — a `TASK [...]`
      line **will** be split mid-token eventually, and a naive per-chunk parser will drop it).
- [x] **A job whose VM disappears does not leak.** If a VM is deleted or vanishes from
      `limactl list` while its job runs, the job is cancelled and reaped. Explicit test.
- [x] The status derivation is exposed as a **pure, testable function** for the tile renderer
      (task 07) to consume: given a `vm.VM` and the registry's snapshot for that name, return the
      derived status (`Building` / `Failed` / `Running` / `Stopped`). Consulted **job-registry
      first, Lima as fallback**.
- [x] `go test ./...` and `go test -race ./...` pass.


## Notes for downstream tasks

- **Ansible prints no task count anywhere in its output**, so there was no honest denominator for
  the tile's progress bar. The in-guest script (`internal/provision/provision.go`) now runs
  `ansible-playbook --list-tasks` and echoes `SAND_ANSIBLE_TASK_TOTAL=<n>` before each run. The
  count is **exact, not an estimate**: Ansible announces a TASK banner even for a task it goes on
  to skip, so the static list and the live banner count agree. Verified on real VMs — the guest
  reported 72 and the run then printed exactly 72 banners, in both phases.
- **Building and Failed derive from PROVISION jobs only.** A failed file transfer does not make its
  VM broken (it is a healthy running VM with a failed copy), so it must not redden the tile — that
  would be its own small lie. Transfer failures surface on the status line and in the reopenable log.
- `jobRegistry.names()` exists so the board (task 08) can raise a tile for a VM being **created**,
  which does not appear in `limactl list` until its clone lands, minutes into its own build.

## Technical Requirements

- Today's mechanism, all in `internal/ui/progress.go`, is the thing being generalized:
  `type readPipe struct{ r *io.PipeReader }`, `beginStream(title string, back view, run streamFunc) tea.Cmd`,
  `beginProvision(...)`, `readNextCmd(rp *readPipe) tea.Cmd`, and the model fields
  `reader`, `output`, `running`, `doneErr`, `cancel`, `canceled`, `provCfg`.
  Messages: `provisionOutputMsg string`, `provisionDoneMsg struct{ err error }`.
- **These messages must become VM-keyed.** `provisionOutputMsg` is a bare `string` today — it
  carries no VM name, which is precisely why only one job can exist. Both messages need the VM
  name, and `readNextCmd` needs to be per-job.
- Bubble Tea's `model` is **passed by value** and updated on a single goroutine. The registry
  must therefore be a **pointer** on the model (or hold its mutable state behind a mutex), and
  every read from a job goroutine must be race-free. This is the codebase's first real
  concurrency — the comment in `model.go` explicitly forbidding `strings.Builder` fields is a
  hint about how value-copying bites here.
- Preserve what already works: `beginProvision` sets `m.provCfg` *after* `beginStream` so only
  provisions get registry-recorded (transfers do not) — that distinction must survive.
  `provisionDoneMsg` currently triggers `manage.RecordSuccess`, seeds `GH_TOKEN` from
  `provCfg.CloneToken`, and batches `listCmd` + `applySecretsCmd`. All of that must still happen,
  per-job.
- Cancellation: today ctrl+c on `viewProgress` cancels the single run. With N jobs, cancellation
  must target a specific job.

## Input Dependencies

Task 01 — Charm v2.

## Output Artifacts

- `internal/ui/jobs.go` — the registry, the job type, per-job cancellation, the retained-run log.
- An Ansible progress parser with a real-output testdata fixture.
- The **derived-status function** the tile renderer (task 07) and the board (task 08) depend on.
- A reopen-last-run command wired into task 02's registry.

## Implementation Notes

<details>
<summary>Guidance</summary>

This is the task most likely to produce a bug that passes every unit test and ruins the demo.
Treat the race detector as a gate, not a formality, and write the two-jobs-in-flight test
**first**.

The signature demo moment this enables: a user presses `n`, fills the form, and instead of the
screen going dark with a full-screen Ansible dump, a new tile appears with a building badge and
a filling progress bar — and the user can **arrow away and start a second VM while the first one
builds**. If that does not work, this task is not done, regardless of what the tests say.

On the Ansible parser: capture real output first (`sand` provisioning a VM, or the playbook run
directly) and commit a trimmed fixture. Do not write the parser against imagined output. The
split-across-read-boundary case is the one that will actually bite — the read loop takes 4096
bytes at a time and a `TASK [role : name] ***` line will land astride a boundary. Buffer to line
boundaries.

**Scope discipline.** In scope: the last run per VM, **in memory**. Out of scope, explicitly and
deliberately: persistence across restarts, a multi-run history, a storage format, pruning, schema
versioning. The plan draws that line in its Decision Log; do not cross it.

**Test philosophy**: write a few tests, mostly integration. Meaningful tests verify custom
business logic, critical paths, and edge cases specific to this application — test *your* code,
not the framework. Here that means: two jobs in flight, per-job cancellation, a failed job
surviving a refresh, a job whose VM disappears, the Ansible parser against real captured output
and across buffer boundaries, and the derived-status function. That is the whole list — do not
write a test per accessor.

Per `PRE_TASK_EXECUTION.md`, RED → GREEN → REFACTOR on each of those.

The real-Lima end-to-end proof of this task (two VMs provisioning concurrently, and a
deliberately-failed provision rendering **Failed** rather than green "Running") is task 10.
In-process tests here are **necessary but not sufficient** — that is the plan's central lesson.
</details>
