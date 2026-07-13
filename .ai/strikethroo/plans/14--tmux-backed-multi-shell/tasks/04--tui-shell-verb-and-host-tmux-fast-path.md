---
id: 4
group: "tui"
dependencies: [2]
status: "completed"
created: 2026-07-13
model: "sonnet"
effort: "high"
complexity_score: 8
complexity_notes: "Two structurally different kinds of tea.Cmd behind one registry action; conflating them silently defeats the fast path's entire purpose. Also touches the registry that drives the footer, the ? screen, and three invariant tests plus two golden snapshots."
skills:
  - go
  - bubbletea
---
# The `S` verb: tmux attach, `--workdir`, and the host-`$TMUX` fast path

## Objective

Make `S` do the best available thing given the host's terminal. It attaches to
the guest's persistent tmux session instead of a bare login shell, opens in the
guest home so no shell greets the user with `bash: cd: … No such file or
directory`, and — when the TUI is itself running inside host tmux — opens a new
**host** window without suspending the TUI at all, so the live board and its
in-flight job progress bars stay on screen next to the shell.

This task adds **no verb, no key, and no screen.** It rewrites one registry
entry's `action` and `about`, and changes what `shellCmd` execs. If you find
yourself adding a `vmCommand`, you have drifted.

## Skills Required

- `go` — `os/exec`, `os.Getenv`, `os.Executable`.
- `bubbletea` — `tea.ExecProcess` vs an ordinary `tea.Cmd`, alt-screen handling,
  and the `teatest` golden-snapshot workflow in `internal/ui`.

## Acceptance Criteria

- [ ] `shellCmd` (`internal/ui/commands.go:210-219`) builds its argv from **task
      2's builder** and from `guestHome` — it constructs no tmux command of its
      own and hardcodes no `limactl shell <name>`. Grep it: the literal `tmux`
      must not appear in `internal/ui/commands.go`.
- [ ] `shellCmd` branches on `os.Getenv("TMUX")`:
      - **unset** (common case): `tea.ExecProcess` as today — suspend, hand over
        the terminal, resume on detach or exit.
      - **set**: run `tmux new-window <abs-path-to-sand> shell <name>` on the
        **host** as an ordinary `tea.Cmd` (a plain `exec.Command(...).Run()`
        folded into an `actionDoneMsg`). It must **NOT** be wrapped in
        `tea.ExecProcess` — that would suspend the TUI to run a command that
        needs no suspension, defeating the branch's entire purpose. There is a
        unit test asserting the two branches return different kinds of command.
- [ ] The fast path uses the **running binary's own resolved path**
      (`os.Executable()`), not the bare word `sand` — `sand` may not be on
      `PATH` (absolute invocation, `go run`).
- [ ] The `S` entry in `internal/ui/commandreg.go:165-179`:
      - `binding` — **unchanged**: still `S`, still the two-word footer label
        `shell`. (Relabelling it would move the footer and break the board
        goldens.)
      - `enabledFor` — **unchanged, deliberately**:
        `notBuilding(m, v) && v.Status == limaRunning`. The gate's second
        rationale (a reset force-deleting the VM out from under a live session)
        is unaffected by whether the TUI suspends. Leave a comment saying this
        was considered and kept, so a later reader does not "fix" it.
      - `about` — **rewritten**. The current sentence ("sand steps aside until you
        exit it") becomes false on both branches. The new one is what a user reads
        in the `?` screen, so it must teach the one thing they must know: the
        session **persists**, and `C-a d` detaches.
      - `action` — **rewritten** to pick the branch and set a per-branch log
        message. The current copy (`"opening a shell in X — the TUI resumes when
        you exit"`, via `internal/ui/messages.go:38`) is wrong on both branches:
        the suspend branch now resumes on *detach or exit*, and the fast path does
        not suspend at all.
- [ ] `go test ./internal/ui` passes **without** `-update`, and the board goldens
      (`TestTUIBoardGolden80x24`, `TestTUIBoardGoldenWide`) are **UNCHANGED**. A
      golden diff means the implementation added or relabelled a verb it was not
      supposed to — **investigate it as a review failure; do not reflexively
      regenerate the snapshot.**
- [ ] The three registry invariant tests still pass:
      `TestBoardHelpAndDispatchAgree`, `TestHelpScreenDescribesEveryVerb` (a verb
      with no `about` fails the build — rewrite it, never delete it), and
      `TestBoardVerbsFireOnlyWhenEnabledForTheFocusedVM`.
- [ ] `gofmt -l .` empty, `go vet ./...` clean, `go test ./...` passes.

## Technical Requirements

- Guest home comes from `guestHome` in **`internal/ui/transfer.go:238`** (reads
  Lima's generated `cloud-config.yaml`), **not** the same-named function in
  `internal/provision/staging.go:67`. The guest home is `/home/<user>.guest`, not
  `/home/<user>` — it cannot be reconstructed from the username. If task 3 lifted
  this helper to a shared package, call it there rather than duplicating it.
- The registry's `action func(m *model, v vm.VM) tea.Cmd` signature already
  accommodates both branch kinds. **No registry field is added.**
- Do not touch `internal/ui/heartbeat.go`. The attach is a distinct `limactl
  shell` with its own TTY and does not route through the heartbeat's `Runner`, so
  they should not collide by construction — task 6 observes this rather than
  assuming it.

## Input Dependencies

- Task 2: the attach-command builder (and, via its Change Log entry, the verified
  attach mechanism).
- Task 1 (transitively): a running probe VM, if you want to eyeball the result.

## Output Artifacts

- A rewritten `shellCmd` and `S` registry entry, plus unit tests asserting the
  branch selection and the argv. Documented by task 5; validated on a real VM and
  under host tmux by task 6.

## Implementation Notes

<details>
<summary>The two branches are different KINDS of tea.Cmd — this is the trap</summary>

`tea.ExecProcess` **suspends the entire program**, tears down input handling,
leaves the alt-screen, hands the real stdin/stdout to one child, and blocks until
it exits. It is single-shot and it is the right wrapper for the suspend branch —
an interactive attach needs the actual TTY, which only a real process attached to
stdin/stdout can provide.

`tmux new-window` on the host is **fire-and-forget**: it returns immediately, and
the new window's TTY is tmux's problem, not ours. Wrapping it in
`tea.ExecProcess` would blank the board to run a command that finishes in
milliseconds — the exact cost the branch exists to avoid. Return an ordinary
`tea.Cmd` instead:

```go
func shellCmd(name, guestHome string) tea.Cmd {
    argv := lima.AttachArgv(name, guestHome)          // task 2's builder — the ONE seam

    if os.Getenv("TMUX") != "" {                      // host tmux: new window, no suspend
        self, err := os.Executable()                  // NOT the bare word "sand"
        if err != nil {
            self = "sand"                             // last resort; PATH lookup
        }
        return func() tea.Msg {
            c := exec.Command("tmux", "new-window", self+" shell "+name)
            err := c.Run()
            return actionDoneMsg{action: "shell", name: name, err: err}
        }
    }

    c := exec.Command(argv[0], argv[1:]...)           // suspend branch: unchanged mechanics
    return tea.ExecProcess(c, func(err error) tea.Msg {
        return actionDoneMsg{action: "shell", name: name, err: err}
    })
}
```

Mind the quoting on the `new-window` argument: tmux takes the command as a single
shell-word string, so a path with spaces needs quoting. Prefer passing the
command as separate argv after `new-window` if tmux accepts it in your version,
or quote deliberately — and note the fast path re-enters through `sand shell`
(task 3), which is exactly the anti-drift property this design wants: the new
host window goes through the same subcommand a user would type.

A unit test can distinguish the two branches by setting/unsetting `TMUX` with
`t.Setenv` and asserting on the returned `tea.Cmd`'s behavior — at minimum that
the fast path's command is *not* an exec-process message. Assert on the **argv**,
never on a real exec: no test may require a real `limactl` (AGENTS.md).
</details>

<details>
<summary>The copy — this is user-facing documentation, not comments</summary>

The `about` sentence renders in the `?` screen (`internal/ui/help.go`). It is not
snapshotted (there is no `?`-screen golden), so rewriting it is free — but the
help screen's closing sentence already promises that "a stopped VM offers no
shell", which stays true only because `enabledFor` is being kept. Confirm it.

Suggested `about` (say persistence and the detach key; do not oversell):

> Attach a shell to the guest's persistent tmux session. Work keeps running after
> you detach (`C-a d`) or close the terminal; `C-a c` opens another window.

Per-branch log message (`m.logMsg(...)`), since the current one is wrong on both:

- suspend branch: `"attaching to " + v.Name + " — C-a d detaches; the TUI resumes when you detach or exit"`
- fast path: `"opened " + v.Name + " in a new tmux window — the board keeps running"`
</details>

<details>
<summary>The golden snapshots are a signal, not an obstacle</summary>

The board goldens render the footer, which is **derived from the command
registry**, so their last two rows are the verb list. The 80x24 footer already
wraps onto two lines and is clipped centrally — an added verb could push a
binding off-screen entirely.

This task adds **no** registry entry and does not change the `S` binding's label,
so the goldens must come out **unchanged**. If they change, something you did
added or relabelled a verb. Find out what. Do not run with `-update` to make the
red go away.
</details>
