---
id: 9
group: "board"
dependencies: [8]
status: "completed"
created: 2026-07-12
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Several distinct surfaces (header band, messages ring buffer, state-gated footer, refresh tick) plus the plan's two required goldens. Individually simple; collectively they are what makes the board feel finished, and the refresh tick has an idle-gating requirement that is easy to get wrong."
skills:
  - go
  - terminal-ui
---
# Board chrome: header band, messages strip, state-gated footer, refresh tick, goldens

## Objective

Finish the board: the **pinned header band** (fleet counts, host capacity, and the hidden count), the
**docked messages strip**, the **state-gated footer command bar**, and the **idle-gated refresh tick**
that keeps the board live. Then pin the responsive range with the plan's two required goldens.

## Skills Required

`go`, `terminal-ui` — lipgloss v2 layout, and the teatest golden harness.

## Acceptance Criteria

- [x] **Header band** — fleet counts plus **host capacity** (the "am I about to over-commit?"
      readout), built directly on the existing `hostMemBytes()` / `hostDiskFreeBytes()` probes.
- [x] **The header carries the hidden count.** The board shows managed clones only (task 08), so base
      images and unrelated Lima VMs get **no tile and no toggle**. The header must therefore report
      what is hidden — e.g. `3 sandboxes · 1 base, 2 external hidden`. This is the **entire mitigation**
      for the escape hatch the board removes: without it, the board silently misrepresents the host,
      which is a quieter version of the dishonesty this plan exists to avoid. **Do not remove it.**
      Test the count against a fleet containing a managed clone, a base image, and an unrelated VM.
- [x] **Messages strip** — a **bounded, session-only, in-memory ring buffer** of timestamped messages,
      **replacing** today's single overwritten `m.status` string. `grep -rn "m\.status" --include=*.go internal/ui`
      returns zero hits. Bounded because it must not grow without limit in a long-lived session;
      session-only because persisting it is the deferred run-history feature, **not this one**.
      Job lifecycle events (started, failed, finished) and action results write to it.
- [x] **Footer command bar** — renders **directly from the command registry** (task 02), filtered by
      the focused tile's VM through `enabledFor(vm)`. It therefore advertises **exactly** the verbs that
      will actually fire, and it **changes as the focused VM's state changes**.
      **Behavioural test:** focus a running VM → footer offers Stop, not Start. Stop it → the footer
      **updates**: Stop disappears, Start appears. This is self-validation step 3f and it must be a test,
      not just a demo step.
- [x] **Idle-gated refresh tick** — an interval timer re-runs `limactl list` and re-renders, on the
      **same gating discipline** as the heartbeat (task 05) and the spinner: an idle `sand` on a
      backgrounded terminal **must not poll**. Without this the board is a snapshot and every claim
      about it being "live" is false. Test that the tick stops when the board is not the active screen.
- [x] **Two goldens pin the responsive range**, per the plan: **80×24** and one **wide** size. They are
      the regression net for the magic offsets task 03 deleted. Follow the existing harness conventions
      exactly (`teatest.WithInitialTermSize`, `t.Setenv("XDG_DATA_HOME", t.TempDir())`, the canned
      `listFakeRunner`, `ansi.Strip` snapshots).
- [x] **There is no "terminal too small" wall at any size.** Assert it across the size sweep.
- [x] **`NO_COLOR` / monochrome**: every status remains distinguishable by **glyph and text label
      alone**. The `ansi.Strip`ped goldens are themselves partial evidence of this — make it explicit.
- [x] `go test ./...` and `go test -race ./...` pass.

## Technical Requirements

- Host probes already exist and are **unexported in package `ui`** — reuse, do not reimplement:
  `hostMemBytes() int64` (`hostres_linux.go` / `hostres_darwin.go`), `hostDiskFreeBytes(path string) int64`
  (`hostres_unix.go`). They currently have exactly one consumer (`form.go`).
- `humanizeBytes` (`format.go`) for every size string in the header.
- Today's status rendering is an ad-hoc `switch { confirm / acting / status }` **duplicated verbatim**
  in `listView` and `detailView`. The messages strip replaces the list side; extract the shared piece
  rather than leaving a third copy behind.
- The messages strip is the **first pane the layout classifier sheds** as the terminal contracts
  (task 03) — so the board must render correctly with the strip absent.
- The refresh tick, the heartbeat, and the spinner now all need gating on the same condition. Consider a
  single `shouldTick()` predicate rather than three copies of the rule — three copies of a gate is how
  the `defaultKeys`/`viewHelp` drift happened in the first place.

## Input Dependencies

Task 08 — the board grid, focus ring, and search must exist to hang chrome on.

## Output Artifacts

- The header band (with the hidden count), the messages ring buffer, the state-gated footer, and the
  refresh tick.
- Two new goldens: 80×24 and wide.
- A board that is complete enough to demo.

## Implementation Notes

<details>
<summary>Guidance</summary>

The **hidden count** is not a nice-to-have and it is not decoration. Task 08 deleted the `f` toggle and
took base images off the board entirely, which means the TUI can no longer show the user everything
`sand` put on their host. The plan accepted that trade **explicitly on the condition** that the header
tells the truth about what is missing. If you find yourself dropping the count to save a line at 80×24,
you have re-opened the exact hole the plan closed. Shed the messages strip first; the count stays.

The footer is the payoff for task 02's command registry: because keys, help, and `enabledFor` all derive
from one list, the footer **cannot** drift from the dispatcher. Today they have drifted — `viewHelp()`
offers `x stop` on a stopped VM. Prove the drift is now structurally impossible with the
focus-a-running-VM-then-stop-it test.

**Goldens are layout-regression insurance and nothing more.** The plan is emphatic about this, and the
history is why: the secrets editor shipped past a passing golden — it opened unfocused and silently
dropped every keystroke, which is precisely the bug class the harness's own comment says it exists to
catch. A golden asserts a screen *painted*. It never asserts the screen *works*. Every behavioural claim
in the acceptance criteria above needs a real assertion over real model state.

**Test philosophy**: write a few tests, mostly integration. Meaningful tests verify custom business
logic, critical paths, and edge cases specific to this application — test *your* code, not the framework.
Here: the hidden count against a mixed fleet, the footer updating on a state change, the refresh tick
gating when idle, the ring buffer bounding, and the two goldens. Not: a golden per pane.

Per `PRE_TASK_EXECUTION.md`, RED → GREEN → REFACTOR.
</details>
