---
id: 1
group: "tier-0-measurement"
dependencies: []
status: "completed"
created: 2026-07-13
model: "sonnet"
effort: "medium"
skills:
  - go
  - ansible
---
# Tier 0: per-phase wall-clock timings and Ansible task profiling

## Objective

Make base VM provisioning cost attributable. Emit per-phase wall-clock durations from the Go provisioner plus a compact end-of-run summary, and enable the `profile_tasks` Ansible callback so per-task timings are visible. Every optimization in this plan is a hypothesis until this histogram exists.

## Skills Required

- **go** — instrumenting `internal/provision/provision.go`'s lifecycle phases.
- **ansible** — enabling the `profile_tasks` callback in `ansible.cfg`.

## Acceptance Criteria

- [ ] `sand create` prints a per-phase duration line for each of: base image creation, base playbook run, base stop, clone, clone start, finalize playbook run, and the bounce (when it runs).
- [ ] A compact end-of-run summary block (phase → duration) is printed at the end of the run.
- [ ] Timing lines are **plain writes** to the provisioner's `io.Writer` — they MUST NOT go through `step()`. Verify by grepping: the timing code contains no `step(` call, and no timing line begins with `==> `.
- [ ] Running `go test ./...` and `go vet ./...` is green.
- [ ] A unit test asserts the summary contains one entry per executed phase and that durations are non-negative.
- [ ] `ansible.cfg` sets `callbacks_enabled = profile_tasks` (or `callback_whitelist` for older syntax) under `[defaults]`, and `ansible-playbook --syntax-check site.yml` still passes.
- [ ] If a Lima host is available: run `sand create --rebuild` and record the cold-build baseline (per-phase durations + the Ansible task profile) into the plan's notes. If no Lima host is available, state so explicitly rather than fabricating numbers.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- `internal/provision/provision.go` — `step()` (line ~101) writes `"\n==> " + format + "\n"`. `internal/ui/ansible.go` parses a `==> ` prefix as `stepPrefix` and **resets the entire progress struct** (clearing Role/Task/Index/Total). Routing timing lines through `step()` would therefore blank the TUI tile's progress bar mid-run. This is the single most important constraint of this task.
- The provisioner writes to a single `io.Writer` per job: in the TUI it is an `io.Pipe` whose bytes are retained in full in `job.output` (`internal/ui/jobs.go`, viewable with `l`); headless it is `os.Stdout`. Plain writes land in both.
- `ansible.cfg` currently contains only `[ssh_connection] ssh_args = ...`. Note the playbook runs with `--connection=local`, so SSH tuning is inert — but **callback plugins still work** under a local connection.
- `profile_tasks` ships in the `ansible.posix` collection, which is vendored by Debian's fat `ansible` package. That package is what the Lima dependency script installs **today**, which is exactly why this task must land BEFORE task 2 swaps to `ansible-core`.

## Input Dependencies

None. This is the first task and changes no behavior.

## Output Artifacts

- Per-phase timing instrumentation in `internal/provision/provision.go`.
- `profile_tasks` enabled in `ansible.cfg`.
- A recorded cold-build + warm-create baseline (if a Lima host is available) for later tiers to be measured against.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Go side.**

1. Add a small phase-timer helper in `internal/provision/`. Something like:

```go
type phaseTimer struct {
    out    io.Writer
    phases []phaseDuration
}

type phaseDuration struct {
    Name string
    D    time.Duration
}

// time runs fn, records its duration under name, and prints a plain line.
func (t *phaseTimer) time(name string, fn func() error) error {
    start := time.Now()
    err := fn()
    d := time.Since(start)
    t.phases = append(t.phases, phaseDuration{name, d})
    // PLAIN write — deliberately NOT step(). A "==> " prefix would reset the
    // TUI tile's progress bar (see internal/ui/ansible.go stepPrefix).
    fmt.Fprintf(t.out, "    [timing] %s: %s\n", name, d.Round(time.Millisecond))
    return err
}

func (t *phaseTimer) summary() {
    fmt.Fprintf(t.out, "\n    [timing] summary\n")
    var total time.Duration
    for _, p := range t.phases {
        fmt.Fprintf(t.out, "    [timing]   %-24s %s\n", p.Name, p.D.Round(time.Millisecond))
        total += p.D
    }
    fmt.Fprintf(t.out, "    [timing]   %-24s %s\n", "TOTAL", total.Round(time.Millisecond))
}
```

2. Wrap each lifecycle phase in `createVM` / `prepareBaseAndClone` / `BuildBase` with `timer.time("base image creation", ...)` etc. The phases to cover, named exactly: `base image creation`, `base playbook`, `base stop`, `clone`, `clone start`, `finalize playbook`, `bounce`.

3. Call `timer.summary()` at the end of a successful `createVM`.

4. **Do not** add a `step()` call for timings. Add a code comment at the timing write site explaining WHY (`==> ` resets tile progress), because a later "tidy-up" that converts these to `step()` would silently break the progress bar.

**Ansible side.**

Add to `ansible.cfg`:

```ini
[defaults]
callbacks_enabled = profile_tasks
```

(If the installed Ansible is older, the key is `callback_whitelist`. Setting both is harmless — unknown keys are ignored.)

Verify with `ansible-playbook --syntax-check site.yml`.

**Testing.** A table-driven Go test on the `phaseTimer` is enough: record two phases, assert `summary()` output contains both names and a TOTAL line, and assert no emitted line starts with `==> `. Do not build a comprehensive test suite here — this is instrumentation.

</details>
