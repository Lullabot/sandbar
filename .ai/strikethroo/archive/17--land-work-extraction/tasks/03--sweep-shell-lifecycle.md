---
id: 3
group: "detection-registry"
dependencies: [1, 2]
status: "completed"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 8
complexity_notes: "Second long-lived limactl shell + goroutine per VM; concurrency + Lima orphaned-ssh teardown; feeds the registry under the model's mutex contract. Concurrency risk floor applies."
skills:
  - go
  - lima-integration
---
# Sweep shell lifecycle: a second limactl shell + goroutine per running VM

## Objective
Run the checkout sweep as a **sibling of the stats heartbeat**: its own
long-lived `limactl shell`, its own goroutine, its own connection and parser
delimiter, at a slow (~60s) cadence — so a slow sweep can never stall the 2s
CPU/mem/disk gauges. Each sweep result updates the per-VM checkout registry
(task 1) via a message applied in `Update` under the mutex, reusing the
heartbeat's `waitDelay`/orphaned-ssh teardown so nothing leaks on cancel or VM
stop.

## Skills Required
- **go** — goroutines, channels, `context` cancellation, Bubble Tea
  model/update/message architecture.
- **lima-integration** — the `limactl shell` streaming pattern and its
  orphaned-ssh hazard (`lima.waitDelay`).

## Acceptance Criteria
- [ ] A sweep shell is started **per running VM**, independently of the stats
      heartbeat, using the guest command from task 2 in a `while true; do
      <sweep>; sleep ~60; done` loop, with its own writer/parser.
- [ ] Sweep output is parsed with `checkouts.ParseSweep` (task 2) and applied to
      the registry (task 1) **only via a message handled in `Update`** (never a
      direct write from the goroutine), honoring the pointer-held/mutex-guarded
      contract.
- [ ] The sweep starts/stops on the same running-state transitions the heartbeat
      uses; on VM stop or context cancel it tears down cleanly and orphans no ssh
      (reuse the heartbeat's `waitDelay` handling).
- [ ] Registry rows are host-persisted (task 1) after each successful sweep, with
      `LastSeen`/`SweptAt` updated, so the badge and delete guard have data even
      when the VM later stops.
- [ ] Cadence and caps are configurable constants (documented), not magic
      numbers scattered in code.
- [ ] `go test ./internal/ui/... -race` passes, including a lifecycle test
      (start → simulated stop/cancel → no goroutine/ssh leak) modeled on the
      existing `heartbeat_lifecycle_test.go`.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Model the implementation on `internal/ui/heartbeat.go` (the long-lived shell,
  the `sampleWriter` streaming parse, the `waitDelay`/orphaned-ssh teardown, the
  start/stop gating). Do **not** inject the sweep into the heartbeat's sequential
  loop — it must be a separate shell/goroutine (the plan's central constraint:
  the heartbeat loop is sequential and a heavy command would freeze the gauges).
- Use a **distinct stream delimiter** from the stats heartbeat so the two
  parsers never cross-talk.
- All registry mutation flows through the Bubble Tea message path into `Update`.

## Input Dependencies
- Task 1: registry store + accessors.
- Task 2: `BuildSweepCommand` / `ParseSweep`.

## Output Artifacts
- A `sweep`-shell subsystem in `internal/ui/` (e.g. `sweep.go` +
  `sweep_test.go`) wired into the model lifecycle alongside the heartbeat, plus
  the message type that carries parsed results into `Update`.

## Implementation Notes
<details>
<summary>Detailed implementation guidance</summary>

1. Copy the shape of the heartbeat: a function that, for a running VM, opens a
   `limactl shell` (via the same `shellFor`/guestShell resolution the heartbeat
   uses) and runs the `while true; do <BuildSweepCommand output>; sleep 60; done`
   loop, streaming into a writer that accumulates a full sweep record and, on the
   end-of-sweep sentinel, emits a Bubble Tea `tea.Msg` (e.g. `sweepResultMsg{conn,
   vm, VMCheckouts}`).
2. In `Update`, handle `sweepResultMsg` by calling the registry `Set` (task 1),
   which persists. This is the ONLY place the registry is written.
3. Reuse the heartbeat's teardown exactly — the `waitDelay` reasoning and the
   orphaned-ssh notes in `heartbeat.go` are load-bearing; do not reinvent. On
   VM-stop / context-cancel the sweep shell must die without orphaning ssh.
4. Gate start/stop the same way the heartbeat gates on `Status == limaRunning`.
5. Constants: `sweepInterval` (~60s), depth cap, count cap (~50) — colocate with
   task 2's command builder or a shared config, documented.
6. Lifecycle test: mirror `heartbeat_lifecycle_test.go` — inject a fake
   shell/streamer, drive start then cancel, assert the goroutine exits and no
   leak. Run with `-race`.
7. Keep the parse/classification in task 2's package; this task only does I/O,
   lifecycle, and the message plumbing.
</details>
