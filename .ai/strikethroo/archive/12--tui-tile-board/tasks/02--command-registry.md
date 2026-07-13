---
id: 2
group: "board-foundations"
dependencies: [1]
status: "completed"
created: 2026-07-12
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Collapses two hand-maintained parallel lists (defaultKeys + viewHelp) into one derived source of truth, and must rewire every key dispatch site without changing behaviour except where the current behaviour is the bug."
skills:
  - go
  - bubble-tea
---
# One command registry: keys, help, enabledFor(vm), and action in one place

## Objective

Replace `defaultKeys()` and `viewHelp()` — two hand-maintained parallel lists that
already disagree — with **one command registry**. Each command carries its key binding,
its help text, its `enabledFor(vm)` predicate, and its action. The keymap, the help/footer
bar, and the key dispatcher are all **derived** from that one list.

The bug this fixes is live on `main` today: `viewHelp()` returns Start, Stop, Restart,
Reset, Shell, Delete, Upload, Download, and Secrets **unconditionally** on the VM screen,
so a **stopped** VM's help bar advertises `x stop`.

## Skills Required

`go`, `bubble-tea` — key binding (`bubbles/v2/key`), help rendering, and the `Update`
dispatch path in `internal/ui`.

## Acceptance Criteria

- [x] A new `internal/ui/registry.go` (or `commands_registry.go` — do not collide with
      `internal/registry`, which is the VM registry) defines a command type roughly:
      `type command struct { key key.Binding; help string; enabledFor func(vm.VM, ...) bool; action func(*model, vm.VM) tea.Cmd }`
      and one package-level list of the commands `sand` actually has.
- [x] `defaultKeys()` and `viewHelp()` **no longer exist**.
      `grep -rn "defaultKeys\|viewHelp" --include=*.go .` returns zero hits.
- [x] The VM screen's key dispatch (`updateDetail`) derives from the registry: a key
      fires **iff** its command's `enabledFor` returns true for the focused VM.
- [x] The help bar is rendered from the same list, filtered by the same predicate — so it
      shows a verb **iff** pressing that verb's key would do something.
- [x] **Behavioural test (required, not a golden):** on a **stopped** VM, the help/footer
      does not contain `stop`, and sending `x` through the real `Update` path produces
      **no** command and no state change (`m.acting` stays false, no `stopCmd` issued).
      The mirror case on a **running** VM does fire Stop.
- [x] **Behavioural test:** a **running** VM offers `start`? No — assert Start is absent
      on a running VM and present on a stopped one. Shell is offered **only** when running
      (today `updateDetail` guards this inline; the guard moves into `enabledFor`).
- [x] `go test ./...` and `go test -race ./...` pass; the existing four goldens still pass
      (the help line they render may legitimately change — if it does, regenerate and
      explain the diff).
- [x] **Not in scope, and must not appear:** a fuzzy command palette; a general plugin
      framework; any command that `sand` does not have today.

## Technical Requirements

- Today's bindings (from `keys.go`): enter, n(new), s(start), x(stop), r(restart),
  d(delete), f(filter), /(search), S(shell), u(upload), g(download), X(stop all),
  R(reset), e(secrets), esc/backspace(back), q(quit), tab/shift+tab, up, down, ctrl+s
  (bound **twice** — Submit "create" and Save "save"), y(confirm), n/esc(cancel),
  ctrl+c(interrupt).
- Commands split into two kinds and the registry must model both: **per-VM verbs**
  (gated by `enabledFor(vm)`) and **global/chrome keys** (quit, back, new, stop-all,
  search) which have no VM. Keep this distinction explicit rather than passing a zero
  `vm.VM` to a global command's predicate.
- `f` (managed-only filter) is being **deleted** by the board task (08). Do **not** delete
  it here — it still has a live `list.go` behind it. Carry it in the registry as-is; task
  08 removes it. Note this in your completion report.
- Preserve `beginAction` (sets `m.acting`, batches the spinner tick, and no-ops the tick
  if already acting — this prevents a double-speed spinner). Actions still route through
  it.
- Preserve the `confirmState` flow for destructive verbs: Delete raises a confirm; the
  registry's `action` returns the command that *raises* the confirm, not the one that
  deletes.
- `g` (download) deliberately collides with `bubbles/table`'s GotoTop and is only matched
  in `updateDetail` today. Once `list.go` is deleted (task 08) the collision is moot;
  until then, do not break it.

## Input Dependencies

Task 01 — the codebase must be on Charm v2 first (`key.Binding` and the help model both
move).

## Output Artifacts

- `internal/ui/registry.go` — the single command list, its `enabledFor` predicates, and its
  actions.
- A derived keymap, a derived help/footer renderer, and a derived dispatcher, all consumed
  by the board (task 08) for its state-gated footer.

## Implementation Notes

<details>
<summary>Guidance</summary>

The registry's justification is narrow and it must stay narrow: it **removes a duplicated
predicate that already disagrees with itself**. If it starts growing plugin points, a
palette, or a generic dispatch framework, it has failed. One list, one predicate, the verbs
that exist today.

The board's footer is per-focused-tile and therefore state-gated no matter what — which
means `enabledFor(vm)` was going to be written regardless. This task's whole value is that
it gets written **once**.

Leave a seam for two predicates that later tasks need, and write them as `enabledFor`
functions that currently return a fixed value, with a TODO naming the owning task:
- `reopen last run's log` → enabled iff the job registry has a retained run for this VM
  (task 04 supplies it).
- verbs disabled while a VM is **building** (e.g. Delete) → the job registry supplies the
  building state (task 04).

Do not invent the job registry here. Take a small interface (e.g.
`type jobLookup interface { StateFor(name string) jobState }`) or a plain function field on
the model, so task 04 can plug in without reopening this file's structure.

**Test philosophy** (restated from the task-generation rules): write a few tests, mostly
integration. Meaningful tests verify custom business logic, critical paths, and edge cases
specific to this application — test *your* code, not the framework. Write tests for: the
`enabledFor` gating (this is the bug), and the derived help/dispatcher agreeing. Do **not**
write a test per binding.

Per `PRE_TASK_EXECUTION.md`, follow RED → GREEN → REFACTOR: the stopped-VM-offers-no-Stop
test should be written first and should **fail against the current code** (it is the live
bug), then pass.
</details>
