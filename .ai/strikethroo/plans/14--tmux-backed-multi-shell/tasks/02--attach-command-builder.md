---
id: 2
group: "guest-attach"
dependencies: [1]
status: "completed"
created: 2026-07-13
model: "opus"
effort: "xhigh"
complexity_score: 9
complexity_notes: "Owns the tmux grouped-session semantics whose two failure modes are asymmetric: setting destroy-unattached on the wrong session silently destroys the user's long-running work — the exact disaster this plan exists to prevent — with no error message. This is the one seam both entrypoints depend on."
skills:
  - go
  - tmux
---
# The shared attach-command builder

## Objective

Build the **one seam** this plan hangs on: a pure Go function that turns
(instance name, guest home) into the argv that attaches a caller to the VM's
persistent guest tmux session with the correct sharing semantics. The TUI's `S`
verb (task 4) and `sand shell` (task 3) must both call it and neither may
construct a tmux command of its own — that is what stops the two entrypoints
drifting, exactly as `provision`/`registry` do for the create paths (see
AGENTS.md).

The function is pure and returns argv, so it is directly unit-testable without a
real `limactl` — which AGENTS.md requires as a hard rule.

## Skills Required

- `go` — a small, well-documented package-level function plus table-driven tests.
- `tmux` — session vs **grouped** session semantics, `destroy-unattached`, and
  client size-clamping. This is the least common expertise in the plan and the
  place a subtle bug is most likely to hide.

## Acceptance Criteria

- [ ] A pure function exists (suggested: `AttachArgv(name, guestHome string)
      []string` in `internal/lima`, next to the other `limactl` argv knowledge)
      that takes an instance name and a guest home and returns the full argv —
      no globals, no exec, no I/O.
- [ ] The argv it returns matches the mechanism **verified by task 1** and
      recorded in the plan's Change Log (`limactl shell …` or the `ssh -t`
      fallback). Read that entry before writing a line of code.
- [ ] The returned argv passes the guest home explicitly as the working directory
      (`--workdir <guestHome>` on the `limactl` path), and **degrades safely**:
      when `guestHome` is empty the flag is omitted entirely rather than emitting
      `--workdir ""`.
- [ ] The guest-side expression creates and attaches session `main` when `main`
      does not exist, and otherwise creates a **grouped** session linked to
      `main`.
- [ ] `destroy-unattached` is set **on the grouped session only, never on
      `main`**. There is a unit test that asserts this direction explicitly and
      whose failure message explains the stakes — reversing it silently converts
      this feature from "your work survives a closed laptop" into "your work dies
      when you look away".
- [ ] The grouped session's name is derived **in the guest at attach time** (e.g.
      from `$$` or tmux's own session list), not computed on the host, so two
      concurrent `sand shell` invocations cannot collide on a name.
- [ ] Table-driven unit tests cover: fresh attach argv, the guest-home-empty
      case, and the `destroy-unattached` asymmetry. `go test ./internal/lima/`
      passes.
- [ ] `gofmt -l .` is empty and `go vet ./...` passes.

## Technical Requirements

- Go stdlib only. No new module dependencies (`go.mod` gets no new lines).
- The guest branch decision (**does `main` exist?**) must run **in the guest** —
  the host cannot see the guest tmux server without a round trip that would race
  anyway. So the argv's tail is a small guest-side shell expression branching on
  `tmux has-session -t main`.
- The canonical session is named `main`. The guest ships a tuned `~/.tmux.conf`
  (`roles/user/templates/tmux.conf.j2`: `C-a` prefix, mouse, 50k scrollback,
  splits bound to `-c "#{pane_current_path}"`) and tmux picks it up on its own —
  do **not** pass `-f`.
- **Add no Ansible.** The plan states the guest side is already complete and that
  new roles are a signal of drift. Inline the guest expression in the Go builder.

## Input Dependencies

- Task 1's Change Log entry in the plan: the verified attach mechanism
  (`limactl shell` vs `ssh -t`) and confirmation that `--workdir` exists.

## Output Artifacts

- The exported builder function — **the single place in the codebase that knows
  tmux exists**. Consumed by task 3 (`sand shell`) and task 4 (the TUI's `S`).
- Its unit tests, which are also the executable specification of the
  `destroy-unattached` asymmetry.

## Implementation Notes

<details>
<summary>Why a plain `tmux new-session -A -s main` is NOT sufficient</summary>

Two clients attached to the *same* tmux session are **mirrored**: they follow
each other's window switches and the display is clamped to the smallest attached
client. That makes a second terminal useless for looking at a second window —
which is the entire point of this plan. A **grouped** session (`tmux new-session
-t main`) shares the window set with `main` — same windows, same running
processes — but tracks its **own current window** and is not size-clamped. That
is precisely the "two terminals, two different windows, one VM" behavior the user
asked for.

So the shape of the guest expression is:

```sh
if tmux has-session -t main 2>/dev/null; then
  # someone is already here: join the group with our OWN session, and make it
  # evaporate when we leave so orphans do not accumulate.
  tmux new-session -t main -s "sand-$$" \; set-option destroy-unattached on
else
  tmux new-session -s main
fi
```

Two details this owns, both called out in the plan's risks:

1. **`destroy-unattached` goes on the grouped session, on ITSELF — never on
   `main`.** `main` must survive detach; that is the whole feature. Note that
   `set-option destroy-unattached on` in the `\;`-chained form applies to the
   session just created, which is what you want — but *verify* that reading of
   your final command rather than trusting it, because getting it backwards
   destroys user work with no error message. The plan calls this out three times
   on purpose.
2. **Unique naming happens in the guest.** `sand-$$` (the attaching shell's PID)
   is chosen atomically with respect to the tmux server that owns it; a
   host-computed counter can race between two `sand shell` invocations.

Mind the quoting: this expression is passed as **one argv element** to
`limactl shell <name> --` (or `ssh -t`), so it must survive one level of shell
parsing in the guest and none on the host — build it as a single Go string and
pass it as a single argv element. Do not `exec.Command("sh", "-c", …)` on the
*host*.
</details>

<details>
<summary>Testing (TDD — see PRE_TASK_EXECUTION)</summary>

This is exactly the "custom logic / critical path" the test philosophy says to
test, so write the tests first:

- RED: `TestAttachArgv` asserting the full argv for a fresh attach, including
  `--workdir /home/andrew.guest`.
- RED: a test asserting the emitted guest expression sets `destroy-unattached`
  in the grouped branch and **does not** mention it anywhere in the `main`
  branch. Assert on both directions — presence in one, absence in the other.
- RED: `guestHome == ""` omits `--workdir` rather than emitting an empty value.

Then GREEN, then refactor. Assert on the **argv**, never on an exec: no test may
require a real `limactl` (AGENTS.md, hard rule). Real-VM behavior is task 6's
job, in the `limae2e`-tagged tests.

Note the guest home is `/home/<user>.guest`, **not** `/home/<user>`, so it can
never be reconstructed from a username — it is always passed in by the caller.
</details>
