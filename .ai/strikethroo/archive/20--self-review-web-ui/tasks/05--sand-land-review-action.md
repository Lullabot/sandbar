---
id: 5
group: "sand-commands"
dependencies: [3, 4]
status: "completed"
created: 2026-08-04
model: "opus"
effort: "xhigh"
complexity_score: 9
complexity_notes: "Concurrent lifecycle management of two child processes across a provider seam, with context cancellation, a readiness race, and teardown guarantees. The linchpin task — task 6 reuses its orchestration."
skills:
  - go
  - concurrency
---
# `sand land NAME PATH --review` action

## Objective

Add `--review` as a third action on `sand land`, orchestrating the guest review
server, the optional forwarder, browser launch, and clean teardown, and blocking
until the review is submitted.

## Skills Required

- **go** — extending `runLand` along the existing `--pr` / `--web` shape.
- **concurrency** — supervising two child processes with context cancellation,
  a readiness wait, and guaranteed teardown on every exit path.

## Acceptance Criteria

- [ ] `sand land --help` documents `--review` alongside `--pr` and `--web`, and
      states that it needs no pushed branch.
- [ ] `sand land NAME PATH --review` combined with `--pr` or `--web` is refused
      with a clear error, matching the existing mutual-exclusion behavior.
- [ ] `--review` without a PATH is refused with the existing
      "run 'sand land NAME' to list them" guidance.
- [ ] `go test ./cmd/sand/... -race -v -run Review` passes, driving the action
      through a fake `Provider` + fake `ghActions` with **no** real VM, ssh,
      browser or network, and covering: the happy path, a `ForwardArgv`-nil
      backend, a `ForwardArgv`-non-nil backend, a server that never becomes
      ready, and a context cancellation mid-review.
- [ ] A test asserts the guest command is built with the checkout path and port
      as **discrete argv elements** — never interpolated into a shell string.
- [ ] A test asserts that on every exit path (success, readiness timeout, ctrl-C)
      both the server command's context is cancelled and the forwarder child, if
      started, is killed — no orphaned processes.
- [ ] Extending `ghActions`/the provider fakes does not break existing land
      tests: `go test ./... -race` passes.
- [ ] On success the command prints the absolute guest path of the written
      `review.xml` and exits 0.
- [ ] Coverage over `./internal/...` stays at or above `COVERAGE_FLOOR` in
      `.github/workflows/test.yml`.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Reuse `runLand`'s existing sweep (`checkouts.BuildSweepCommand` /
  `ParseSweep`) and `findCheckout` — do not add a second discovery path.
- Add `--review` to `reorderLandFlags` so `sand land NAME PATH --review` parses
  identically to the flag-first form.
- Unlike `--pr` / `--web`, `--review` must **not** require
  `PushStatePushed` or a known `OrgRepo`: reviewing uncommitted or unpushed work
  is the primary use case.
- Port selection: pick one port free on the workstation and use that same number
  for both host and guest (local Lima's forward is same-port).
- Open the browser through the existing injected `OpenInBrowser` seam, not a new
  mechanism.
- The orchestration must be factored so task 6 (TUI) can call it without
  duplicating logic.

## Input Dependencies

- Task 3: the guest install path of the web app and the `--with-review` gate.
- Task 4: `Provider.ForwardArgv` and its nil-means-already-reachable contract.

## Output Artifacts

- The `--review` flag, its validation, and its action in `cmd/sand/land.go`
- A reusable orchestration function (port pick → server start → forward →
  readiness → browser → wait → teardown) that task 6 consumes
- Tests driving every branch through fakes

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Read `cmd/sand/land.go` end to end first.** It is heavily commented and
explains why `landPR`/`landWeb` take injected dependencies (`ghActions`, a
`tty bool`, a `confirmOpen func() bool`) instead of reaching for globals — so
the branching is testable with no real gh, terminal or browser. Follow that
discipline exactly: the new action must take its dependencies as parameters.

**Shape of the orchestration.** Write it as a function that accepts the pieces
it needs (provider, checkout, an opener, a port picker, a readiness prober) and
returns the written `review.xml` path:

1. **Pick a port.** Listen on `127.0.0.1:0`, read the assigned port, close the
   listener, use that number. Inject this as a `func() (int, error)` so tests can
   force a fixed port and an error.
2. **Start the guest server.** `Provider.Shell` blocks until the guest command
   exits, which is exactly the completion signal wanted — run it in a goroutine
   with a cancellable context derived from `runLand`'s existing
   `signal.NotifyContext`. Build the argv as discrete elements:

   ```go
   argv := []string{"node", serverPath, "--repo", co.Path, "--port", strconv.Itoa(port)}
   ```

   Never build a `sh -c` string with `co.Path` in it: sweep-discovered paths must
   not reach a guest shell as text. This is the same rule `Provider.RunArgv`'s
   separate `workdir` parameter exists to enforce.
3. **Start the forwarder.** Call `ForwardArgv`; if it returns nil there is
   nothing to do (local Lima). Otherwise `exec.CommandContext` it and ensure it
   is killed on every return path (`defer`).
4. **Wait for readiness.** Poll `net.DialTimeout` against `127.0.0.1:<port>`
   until it accepts, with a bounded overall timeout and a short sleep between
   attempts. On timeout, return an error that names the likely cause — including
   that the VM may have been built without `--with-review`. Also stop waiting
   early if the server goroutine has already exited with an error, so a missing
   `node` or missing install path surfaces its real message instead of a generic
   timeout.
5. **Open the browser** at `http://127.0.0.1:<port>` via the injected opener.
6. **Wait** for the server goroutine to finish. A clean exit means the review was
   submitted; report `filepath.Join(co.Path, "review.xml")`. A non-zero exit or
   a cancelled context is an error.

**Diff range.** Default to the checkout's branch against its merge-base with the
repository's default branch — what the change would land as. Compute it in the
guest and pass it to the server as `--diff-args`. Keep the mechanism simple and
argv-safe; if the base cannot be determined, fall back to the server's own
default (the working tree) rather than failing the command.

**Teardown discipline — this is the part most likely to be got wrong.** Use
`defer` for the forwarder kill and the context cancel so they run on the error
paths too, and make sure the server goroutine cannot leak: give it a channel the
caller always drains, or a `sync.WaitGroup` the function waits on before
returning. Test cancellation explicitly with a context you cancel from the test.

**Factoring for task 6.** The TUI cannot block its event loop, so keep the
orchestration free of `os.Stdout`/`os.Stderr` writes and of `signal.NotifyContext`
— take a `context.Context` and an `io.Writer` as parameters and let `runLand`
supply the CLI's versions. Task 6 will call the same function from a Bubble Tea
command.

**Testing.** Extend the existing land fakes rather than writing new ones. The
provider fake needs to record the `Shell` argv and return a controllable
exit; the readiness prober and port picker are injected funcs. Follow the plan's
test philosophy: cover the branching and the teardown guarantees, not `net.Dial`
or `os/exec` themselves.

</details>
