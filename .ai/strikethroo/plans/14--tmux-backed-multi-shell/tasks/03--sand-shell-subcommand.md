---
id: 3
group: "cli"
dependencies: [2]
status: "pending"
created: 2026-07-13
model: "sonnet"
effort: "medium"
complexity_score: 5
complexity_notes: "Standard stdlib-flag subcommand following the existing `create` case; the only subtlety is exec'ing with the real TTY and refusing a non-running VM in words rather than a raw limactl error."
skills:
  - go
  - cli
---
# `sand shell <name>` subcommand

## Objective

Give a second terminal a real, discoverable entrypoint into a VM, so "open
another window" is a command a user can type rather than a `limactl` incantation
they must know. `sand shell <name>` resolves the guest home, builds the attach
command from task 2's builder, and hands the real TTY to it.

## Skills Required

- `go` — stdlib `flag.NewFlagSet`, `os/exec` with inherited stdio, process exit
  codes.
- `cli` — subcommand dispatch, usage strings, actionable error messages.

## Acceptance Criteria

- [ ] `cmd/sand/main.go`'s `switch os.Args[1]` gains a `case "shell":` following
      the exact shape of the existing `create` case, delegating to a `runShell`
      function in a new `cmd/sand/shell.go` (alongside `runCreate` in
      `create.go`).
- [ ] The instance name is a **positional** argument. Flags are parsed with
      `flag.NewFlagSet("shell", flag.ContinueOnError)` — no cobra, urfave or
      pflag; none are in `go.mod` and this task adds none.
- [ ] The attach argv comes from **task 2's builder**. `runShell` constructs no
      tmux command of its own — grep the file: the string `tmux` must not appear
      in it.
- [ ] The child process inherits the real `os.Stdin`/`os.Stdout`/`os.Stderr` (a
      tmux attach needs the actual TTY) and `sand` exits with the child's exit
      code. There is no Bubble Tea program in this path to suspend.
- [ ] A VM that is **not running** produces a clear, actionable message —
      e.g. `sand: VM "foo" is not running (status: Stopped); start it first` —
      not a raw `limactl` error. This mirrors the TUI's `enabledFor` guard, which
      simply withholds the verb; the CLI has no footer to withhold it from, so it
      must say so in words.
- [ ] An unknown / non-sand instance name fails cleanly with a readable message,
      not a stack trace or a raw exec error.
- [ ] The usage string in the `default:` case of the switch — today
      `sand` / `sand create ...` — **lists `shell`**. It is the tool's only
      discovery surface for someone who typed something wrong. Verify:
      `go run ./cmd/sand bogus 2>&1 | grep -q 'sand shell'`.
- [ ] Unit tests in `cmd/sand/` (following `create_test.go`'s stub pattern) cover
      the not-running message and the missing-name usage error, with **no real
      `limactl`** (AGENTS.md, hard rule).
- [ ] `go build ./cmd/sand`, `gofmt -l .` (empty), `go vet ./...`, `go test ./...`
      all pass.

## Technical Requirements

- Guest home: use the `guestHome` helper that reads Lima's generated
  `cloud-config.yaml` — the one in **`internal/ui`** (`transfer.go:238`). There is
  a second, unrelated `guestHome` in `internal/provision/staging.go:67` that
  shells out to `getent passwd`; **picking the wrong one puts `--workdir` at a
  path that does not exist and reintroduces the exact `cd` error this plan sets
  out to fix.** The `internal/ui` one is currently unexported — export it, or lift
  it to a package both `cmd/sand` and `internal/ui` can import (e.g. alongside the
  builder in `internal/lima`). Do not copy-paste it: one implementation, one call
  site each. Note the guest home is `/home/<user>.guest`, not `/home/<user>`.
- Status check: `lima.Client.Status(name)` (see `limaBaseDeleter` in
  `cmd/sand/create.go` for the narrow-interface testing pattern this repo uses).
- The running status string is `"Running"` (`limaRunning` in `internal/ui`).

## Input Dependencies

- Task 2: the attach-command builder, and its verified mechanism.

## Output Artifacts

- `cmd/sand/shell.go` (`runShell`), the `shell` case in `main.go`, an updated
  usage string, and unit tests. Documented by task 5; validated on a real VM by
  task 6.

## Implementation Notes

<details>
<summary>Shape</summary>

Follow `cmd/sand/create.go` closely — it is the precedent for everything here
(FlagSet construction, `fs.Usage`, error-returning `runX(args []string) error`,
narrow interfaces for testability).

```go
// cmd/sand/shell.go
func runShell(args []string) error {
    fs := flag.NewFlagSet("shell", flag.ContinueOnError)
    fs.Usage = func() { /* Usage: sand shell <name> … mention C-a d detaches */ }
    if err := fs.Parse(args); err != nil { return err }
    if fs.NArg() != 1 { fs.Usage(); return errors.New("sand shell: need exactly one VM name") }
    name := fs.Arg(0)

    cli := lima.New(lima.NewExecRunner())
    if err := cli.Preflight(); err != nil { return err }

    st, err := cli.Status(name)          // clean message for unknown instance
    if err != nil { return fmt.Errorf("sand shell: %w", err) }
    if st != "Running" {
        return fmt.Errorf("sand shell: VM %q is not running (status: %s); start it first", name, st)
    }

    argv := lima.AttachArgv(name, guestHome(instanceDir(name)))   // task 2's builder
    c := exec.Command(argv[0], argv[1:]...)
    c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
    return c.Run()                        // real TTY; exit code propagates
}
```

Propagate the child's exit code rather than collapsing every failure to 1 — an
`*exec.ExitError` carries it. `main.go` already does `os.Exit(1)` on a returned
error, so if you want fidelity, exit inside `runShell` or return a typed error
`main` can unwrap; either is fine, just be deliberate.

The usage string in `main.go`'s `default:` case must become something like:

```
Usage:
  sand              interactive TUI
  sand create ...   headless create (see 'sand create -h')
  sand shell NAME   attach a shell to a VM (see 'sand shell -h')
```
</details>

<details>
<summary>Testing (TDD — see PRE_TASK_EXECUTION)</summary>

Test philosophy for this repo: **write a few tests, mostly integration.**
Meaningful tests verify custom business logic, critical paths, and edge cases
specific to this application — test *your* code, not the framework or library.

**Write tests for:** custom business logic and algorithms; critical user
workflows and data transformations; edge cases and error conditions for core
functionality; integration points between components; complex validation logic.

**Do NOT write tests for:** third-party library functionality; framework
features; simple CRUD without custom logic; trivial getters/setters or static
configuration; obvious functionality that would break immediately if incorrect.

Concretely here: the *dispatch and refusal logic* is worth testing (the
not-running message, the arg-count error, the usage string listing `shell`). The
`exec.Command` hand-off is **not** — it needs a real TTY and a real VM, which is
task 6's job in the `limae2e`-tagged tests. Factor `runShell` so the decision
part takes an injected status-source interface (as `create.go` does with
`limaBaseDeleter`) and the exec is the last, untested line.
</details>
