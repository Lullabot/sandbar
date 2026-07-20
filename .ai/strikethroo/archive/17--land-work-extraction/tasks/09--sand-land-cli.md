---
id: 9
group: "cli"
dependencies: [2, 6]
status: "completed"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Headless CLI parity: its own one-shot sweep + host gh actions + TTY-vs-pipe exit semantics for the gh-optional path. Integration wiring reusing shared code."
skills:
  - go
  - cli
---
# `sand land NAME` headless CLI

## Objective
Give land headless parity with the pane, mirroring the `create`/`shell`
single-profile dispatch in `cmd/sand/main.go`. `sand land NAME` runs its **own
one-shot sweep** (read-only, guest) to find checkouts/branches, then acts
host-side via the gh adapter (task 6). Detection and PR logic are the **shared
code** the pane also uses.

## Skills Required
- **go** — CLI dispatch, flag parsing, TTY detection, exit codes.
- **cli** — the `sand` subcommand conventions and single-profile resolution.

## Acceptance Criteria
- [ ] `sand land NAME` lists the VM's checkouts + branch/push/PR state (own
      one-shot sweep via task 2 + `gh.PRState` via task 6).
- [ ] `sand land NAME <path> --pr` opens a **one-shot draft PR** for that
      checkout's pushed branch via host `gh` (task 6). Without gh: it **prints the
      compare URL** and, on a **TTY**, offers to open it; in a **script/pipe** it
      exits **non-zero** with the URL on **stderr** so automation can react.
- [ ] `sand land NAME <path> --web` opens the branch's PR (or the branch) in a
      browser — **gh-free** (constructed URL + OS opener, task 6).
- [ ] Dispatch mirrors `create`/`shell`: a case in `cmd/sand/main.go`'s switch
      resolving the single profile the same way.
- [ ] The one-shot sweep reuses `checkouts.BuildSweepCommand`/`ParseSweep`
      (task 2) — no duplicated detection logic — and runs against a **running** VM
      (matching pane gating).
- [ ] `go test ./...` passes with tests for: the one-shot listing output, the
      `--pr` gh-absent TTY vs pipe branching (fake gh + fake TTY), and `--web`
      URL targeting.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Reuse the shared detection (task 2) and gh adapter (task 6); this task is
  wiring + CLI ergonomics, not new detection or new gh logic.
- Run the one-shot sweep by executing `BuildSweepCommand`'s output once via a
  `limactl shell` (single invocation, not the long-lived loop) and parsing with
  `ParseSweep`.
- Exit-code contract matters: `--pr` without gh must be non-zero in a pipe (URL
  on stderr) but interactive/offer-to-open on a TTY.

## Input Dependencies
- Task 2: `BuildSweepCommand` / `ParseSweep` for the one-shot sweep.
- Task 6: `PRState`, `CreateDraftPR`, `CompareURL`, `PRURL`, `OpenInBrowser`,
  `Available`.

## Output Artifacts
- A `land` subcommand in `cmd/sand/` (dispatch + a `runLand`-style handler) with
  `--pr`/`--web` flags and tests.

## Implementation Notes
<details>
<summary>Detailed implementation guidance</summary>

1. Add a `case "land":` to the switch in `cmd/sand/main.go` mirroring
   `create`/`shell`; resolve the single profile the same way those do.
2. `runLand(name, path, flags)`:
   - Resolve the VM; require it running (match pane gating).
   - One-shot sweep: open a single `limactl shell`, run
     `checkouts.BuildSweepCommand()`, capture output, `checkouts.ParseSweep`.
   - No path/flags: print a table of checkouts with branch/push state; call
     `gh.PRState` per pushed branch to annotate PR state.
   - `--pr <path>`: find the checkout; if pushed, `gh.CreateDraftPR`. If
     `!gh.Available()`: print `gh.CompareURL(...)`; if `isatty(stdout)` offer to
     `OpenInBrowser`; else exit non-zero with the URL on stderr.
   - `--web <path>`: `OpenInBrowser(PRURL or CompareURL)` — gh-free.
3. TTY detection: use the project's existing isatty approach if one exists
   (grep), else `golang.org/x/term.IsTerminal` on the fd — check go.mod for an
   available dependency before adding one.
4. Tests: fake the gh adapter + opener (task 6 made them injectable) and the TTY
   signal; assert stdout/stderr/exit for each branch. Keep detection logic tested
   in task 2, not re-tested here.
</details>
