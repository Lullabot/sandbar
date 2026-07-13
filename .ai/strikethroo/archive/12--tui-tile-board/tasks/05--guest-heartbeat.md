---
id: 5
group: "live-behaviour"
dependencies: [1]
status: "completed"
created: 2026-07-12
model: "opus"
effort: "xhigh"
complexity_score: 9
complexity_notes: "One long-lived streaming SSH connection and one goroutine per running VM, with strict idle-gating and clean teardown when a VM stops underneath the stream. Concurrency plus an unverified Lima assumption — a leaked goroutine or a stuck gauge is a failure, not a cosmetic issue."
skills:
  - go
  - concurrency
---
# The guest heartbeat: real cpu% and mem from inside each running VM

## Objective

Put **real** utilization on the tiles, cheaply enough to run continuously and honestly enough
to be trusted.

Today `vm.VM` carries only **allocations** (`CPUs`, `Memory`). Rendering an allocation as a
utilization bar implies telemetry the tool does not have, and **it must not ship**. Get real
numbers by streaming them from inside the guest: for each **running** VM, open **one**
long-lived `limactl shell` via the existing `lima.Runner.Stream`, running a trivial in-guest
loop that emits `/proc/stat` and `/proc/meminfo` every N seconds, and parse the stream into a
live cpu percentage and mem used/total.

## Skills Required

`go`, `concurrency` — long-lived streaming subprocess management, goroutine lifecycle, and
race-free delivery of samples into the Bubble Tea update loop.

## Acceptance Criteria

- [x] A new `internal/ui/heartbeat.go` opens **one** streaming shell per **running** VM via
      `lima.Client.ShellStreamOut` / `Runner.Stream` (the interface already exists — do **not**
      spawn a `limactl shell` per sample tick; each spawn costs 150–400ms and a fresh SSH
      connection).
- [x] A sample type carries cpu% and mem used/total. cpu% is computed from **successive**
      `/proc/stat` deltas (a single reading is meaningless — it is cumulative since boot).
- [x] **Parser is unit-tested against real captured `/proc/stat` and `/proc/meminfo` text**
      (testdata fixture), including: the delta computation across two samples, a `MemAvailable`-
      based used calculation (not `MemFree` — `MemFree` excludes cache and reads as a false
      near-OOM), and output split across read-buffer boundaries.
- [x] **Stopped VMs get no heartbeat at all**, and therefore no sample. The tile's job (task 07)
      is to render *absence*; this task's job is to guarantee there is genuinely nothing to
      render — not a zero.
- [x] **Strictly idle-gated.** The heartbeat pauses when the board is not the active screen and
      when the app is idle, so `sand` over SSH or on battery is not quietly holding N connections
      open and burning CPU. Extend the discipline the spinner already uses (`spinner.TickMsg` is
      gated by `if !m.running && !m.acting { return m, nil }`).
- [x] **Clean teardown when a VM stops underneath the stream.** The heartbeat terminates, the
      goroutine is reaped, and the tile falls back to its stopped rendering — **no stuck gauge, no
      leaked goroutine**. Explicit test; a leak here is a failure, not a cosmetic issue.
- [x] A heartbeat starts when a VM transitions to Running and stops when it leaves Running,
      driven off the existing `vmsLoadedMsg` refresh.
- [x] `go test -race ./...` passes. Leak-check the goroutines (e.g. compare
      `runtime.NumGoroutine()` before/after, or use `go.uber.org/goleak` if adding it is cheap).
- [x] The Lima-side assumptions are **verified against a real Lima VM** (limactl 2.1.3 is on this
      host) — not taken on faith. The real-Lima e2e test is task 10, but this task must have
      *run* a streaming shell against a real VM and reported what it actually observed.

## Technical Requirements

- `lima.Runner` (already exists, from `internal/lima/runner.go`):
  ```go
  Stream(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error
  StreamOut(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error
  ```
  and `Client.ShellStreamOut(ctx, name string, stdin io.Reader, out io.Writer, argv ...string) error`.
- The in-guest loop should be a trivial shell one-liner emitting a delimited record, e.g. a
  `while true; do cat /proc/stat /proc/meminfo; echo '---'; sleep N; done`-shaped script. Keep the
  guest side as dumb as possible and do all parsing on the host — a clever guest script is a thing
  that breaks on a distro you did not test.
- cpu%: `/proc/stat`'s first `cpu` line gives cumulative jiffies. `busy = total - idle - iowait`;
  the percentage is `Δbusy / Δtotal` between consecutive samples. The **first** sample after
  connecting therefore yields no percentage — render nothing, not zero.
- mem: `/proc/meminfo`'s `MemTotal` and `MemAvailable`. `used = MemTotal - MemAvailable`.
- Rejected approaches, recorded so they are not re-litigated: **host-side sampling of the QEMU
  process tree** via `~/.lima/<name>/ha.pid` gives a good CPU number and a **bad memory number**
  (QEMU RSS counts *touched* guest pages; without free-page-reporting it ratchets to the full
  allocation and never returns — a high-water mark cosplaying as utilization). **Lima's guest agent
  socket** (`ga.sock`) is port-forwarding only and exposes **no metrics**.

## Input Dependencies

Task 01 — Charm v2.

## Output Artifacts

- `internal/ui/heartbeat.go` — the per-VM streaming sampler, its lifecycle, and its idle gating.
- The **sample type** the tile renderer (task 07) consumes to draw the cpu and mem gauges.
- A parser with real-`/proc` testdata fixtures.
- An observed-behaviour report on a real Lima VM: does the streaming shell survive? how does it
  die when the VM stops? what is the actual idle cost?

## Implementation Notes

<details>
<summary>Guidance</summary>

The cost model that justifies this design: one SSH connection and one goroutine per **running**
VM. At the plan's scale assumption — 90% of users have 1–3 VMs, power users top out around 10 —
that is negligible. It would not be at 100 VMs, and the plan explicitly does not care about 100
VMs.

Verify against real Lima **early**, not at the end. The whole design rests on "a long-lived
`limactl shell` streams reliably and dies cleanly", and that is an assumption, not a fact, until
you have watched it. Spin up a VM, run the stream, `limactl stop` it out from under the reader,
and see what actually happens to the goroutine and the `error` returned. Report what you see —
including if it contradicts the plan.

Idle-gating is a hard requirement, not a polish item: `sand` left open on a backgrounded terminal
over SSH must not hold N connections open. Verify the idle case draws no measurable CPU (task 12
re-checks this as a self-validation step).

**Test philosophy**: write a few tests, mostly integration. Meaningful tests verify custom business
logic, critical paths, and edge cases — test *your* code, not the framework. Here: the `/proc`
parsers against real captured text, the cpu delta computation, the buffer-boundary split, the
start/stop lifecycle on VM state transitions, and the no-leak teardown. Not: a test per getter.

Per `PRE_TASK_EXECUTION.md`, RED → GREEN → REFACTOR.

The parse of **real** `/proc/stat` and `/proc/meminfo` from a **live guest** is proven in task 10's
real-Lima e2e, not here. In-process tests against a fake `Runner` are necessary but not sufficient
— that is the plan's central lesson, and this is exactly the kind of claim (it crosses a process
and a machine boundary) that has to be tested at the far side.
</details>
