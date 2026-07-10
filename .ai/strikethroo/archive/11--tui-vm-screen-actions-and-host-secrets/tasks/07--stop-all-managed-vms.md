---
id: 7
group: "tui-list"
dependencies: [6]
status: "completed"
created: 2026-07-09
model: "sonnet"
effort: "medium"
complexity_score: 5
complexity_notes: "Straightforward once the confirm overlay exists. The care is in scoping the target set correctly (managed, running, not a base image) and reporting partial failure by name."
skills:
  - go
  - bubbletea
---
# Stop all running sand-managed VMs

## Objective

Add `X` on the list screen: stop every VM that is both sand-managed and currently
`Running`, after a confirmation that names them. Never touch an unmanaged Lima
instance, and never touch a base image. Report by name any VM that failed to stop,
having still stopped the ones that succeeded.

## Skills Required

- **go** — error accumulation across a sequential loop.
- **bubbletea** — `tea.Cmd` construction, reusing the confirm overlay and the
  action spinner.

## Acceptance Criteria

- [ ] `X` on the list screen computes the target set: VMs where
      `m.reg.IsManaged(name)` is true, `Status == "Running"`, and
      `m.isBaseImage(name)` is false.
- [ ] When the target set is empty, `X` sets a status line saying so (e.g.
      `no running sand VMs to stop`) and dispatches nothing — no overlay is raised.
- [ ] Otherwise `X` raises the shared confirm overlay from task 6, whose prompt
      names the VMs. If the name list would exceed the terminal width, it is
      truncated for display (e.g. `web, api, db and 3 more`) — but **all** targets
      are still stopped.
- [ ] On `y`, a single `tea.Cmd` stops each target **sequentially** and returns one
      `actionDoneMsg`. Concurrency is deliberately avoided: `lima.Client` offers no
      concurrency guarantees and a serial loop yields a deterministic error report.
- [ ] Partial failure: the command accumulates per-VM errors and returns an error
      naming each VM that would not stop. VMs that stopped successfully stay
      stopped. The list reloads afterwards so the true state is visible regardless.
- [ ] The existing `beginAction` spinner covers the whole run (it may take tens of
      seconds).
- [ ] `X` is inert on the VM screen (it is not in `viewDetail`'s help, and
      `updateDetail` does not match it).
- [ ] Verification: `go test ./internal/ui/... -v` passes, including:
      ```
      go test ./internal/ui/... -run 'StopAll' -v
      ```
      Expected `PASS`, with tests asserting: (a) with one managed-running, one
      managed-stopped, one unmanaged-running, and one base-image VM loaded, the
      target set is exactly the managed-running one; (b) with no managed-running
      VMs, `X` raises no overlay and sets a status; (c) a fake client whose `Stop`
      fails for `bad` and succeeds for `good` produces an `actionDoneMsg` whose
      error message contains `bad` and does **not** contain `good`, and whose
      `Stop` was called for both.
- [ ] Verification: `go build ./... && go vet ./...` succeed.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Files: `internal/ui/list.go`, `internal/ui/commands.go`, and
  `internal/ui/model_test.go`.
- **Do not modify** `internal/ui/keys.go` — task 5 already bound `StopAll` to `X`
  and added it to `viewList`'s help.
- **Do not modify** `internal/ui/model.go`'s confirm value — task 6 made it
  reusable. Raise it exactly as the delete handler does.
- `m.isBaseImage(name)` already exists in `list.go`. A base image is kept
  `Stopped` by design, so it will rarely match `Running` — but exclude it
  explicitly rather than relying on that, because a mid-build base *is* running.

## Input Dependencies

- **Task 6**: the reusable `confirmState` overlay and the single `updateConfirm`
  entry point. This task raises the overlay with a different prompt and a
  different `run` command; it adds no overlay logic of its own.
- **Task 5**: the `StopAll` key binding and its presence in `viewList`'s help bar.

## Output Artifacts

- `stopAllCmd(cli *lima.Client, names []string) tea.Cmd` in `commands.go`.
- A working `X` on the list.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Read first:** `internal/ui/commands.go` (all of it — note how `stopCmd` wraps a
blocking `cli.Stop` in a `tea.Cmd` returning `actionDoneMsg`), `internal/ui/list.go`
`isBaseImage` and `beginAction`, and task 6's `confirmState`.

**Step 1 — the target set.** Add to `list.go`:

```go
// stopAllTargets returns the sand-managed VMs that are currently running. Base
// images are excluded: they are kept stopped by design and are a clone source,
// not a workspace — though a base mid-build is running, which is exactly why the
// exclusion is explicit rather than incidental.
func (m model) stopAllTargets() []string {
    var names []string
    for _, v := range m.vms {
        if v.Status != "Running" || !m.reg.IsManaged(v.Name) || m.isBaseImage(v.Name) {
            continue
        }
        names = append(names, v.Name)
    }
    return names
}
```

Note this walks `m.vms` (everything loaded), **not** the filtered table rows — a
managed VM hidden by an active `/` name filter or by `f` should still be stopped.
That is a deliberate choice: `X` says "stop all", not "stop what I can see". Say
so in a comment, because the opposite reading is defensible and a future reader
will wonder.

**Step 2 — the command.** In `commands.go`:

```go
// stopAllCmd stops each VM in turn, accumulating failures. Stopping is
// sequential rather than concurrent: limactl stop is I/O-heavy, lima.Client
// gives no concurrency guarantees, and a serial loop yields a deterministic
// error report. VMs that stop successfully stay stopped even if a later one
// fails.
func stopAllCmd(cli *lima.Client, names []string) tea.Cmd {
    return func() tea.Msg {
        var failed []string
        for _, n := range names {
            if err := cli.Stop(n); err != nil {
                failed = append(failed, n)
            }
        }
        var err error
        if len(failed) > 0 {
            err = fmt.Errorf("could not stop: %s", strings.Join(failed, ", "))
        }
        return actionDoneMsg{action: "stop all", name: "", err: err}
    }
}
```

⚠️ `actionDoneMsg`'s handler in `model.go` builds its label as
`msg.action + " " + msg.name`. With an empty `name` that yields a trailing space
(`"stop all "`). Either trim it, or pass a count as `name` (e.g.
`fmt.Sprintf("(%d VMs)", len(names))`). Pick one and make the status line read
naturally: `stop all (3 VMs) ok`. Also confirm the `case msg.action == "delete":`
arm cannot match `"stop all"` — it cannot, but the `default:` arm will set
`m.status = label + " ok"`, which is what we want.

**Step 3 — the key handler.** In `updateList`:

```go
case key.Matches(msg, m.keys.StopAll):
    targets := m.stopAllTargets()
    if len(targets) == 0 {
        m.status = "no running sand VMs to stop"
        return m, nil
    }
    m.confirm = &confirmState{
        prompt: fmt.Sprintf("Stop %d running sand VMs (%s)?", len(targets), summarizeNames(targets, m.width)),
        run:    stopAllCmd(m.cli, targets),
    }
    return m, nil
```

**Step 4 — `summarizeNames`.** Truncate for display only:

```go
// summarizeNames renders up to a width-appropriate number of names, summarizing
// the remainder as "and N more". Display only — every target is still stopped.
func summarizeNames(names []string, width int) string {
    // Reserve room for the surrounding prompt text; fall back to a sane default
    // when the terminal size has not been reported yet (width == 0).
    ...
}
```

Keep it simple: join names while the running length stays under, say,
`max(20, width-40)` characters, then append `and N more`. Test the truncation
boundary with a small width and many names.

**Step 5 — help bar.** Already done in task 5. Verify `X` appears on the list's
help and not on the VM screen's.

**Testing philosophy.** Write a few tests, mostly integration. Meaningful tests
verify custom business logic, critical paths, and edge cases specific to this
application — test *your* code, not the framework or library.

Write tests for: custom business logic and algorithms; critical user workflows
and data transformations; edge cases and error conditions for core functionality;
integration points between components; complex validation logic or calculations.

Do NOT write tests for: third-party library functionality; framework features;
simple CRUD operations without custom logic; trivial getters/setters or static
configuration; obvious functionality that would break immediately if incorrect.

Here that means: the target-set filter (the safety-critical bit — never stop an
unmanaged VM) and the partial-failure report are the two behaviours worth testing.
`summarizeNames`'s truncation gets one boundary case. Do not test `strings.Join`.

</details>
