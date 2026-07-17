---
id: 2
group: "detection-registry"
dependencies: [1]
status: "completed"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Pure host-side business logic with many edge cases (worktree pointers, tracking-ref push classification, -u-less push, remote URL parsing, truncation). This is where the tests live."
skills:
  - go
  - git-porcelain
---
# Sweep output parser, git classification, and guest command builder

## Objective
Implement the pure, host-side logic that turns a single sweep of a guest's
filesystem into checkout registry rows: build the read-only guest command,
parse its streamed output, and classify each checkout (kind, parent, branch,
forge+org/repo, push state, dirty count, truncation). All business logic and its
tests live here so both the long-lived pane sweep (task 3) and the headless CLI
one-shot sweep (task 9) share one implementation.

## Skills Required
- **go** — string parsing, URL parsing, table-driven tests.
- **git-porcelain** — read-only `git` plumbing/porcelain semantics:
  `rev-list --count`, `status --porcelain`, remote-tracking refs vs configured
  upstream, `--no-optional-locks`.

## Acceptance Criteria
- [ ] A function builds the **guest command string**: a bounded `find` from the
      guest `$HOME` for `.git` entries matching **both directories** (normal
      checkouts) **and files** (worktree pointers), pruning noise
      (`node_modules`, common caches), honoring a **depth cap** and a **total
      count cap (~50)**, and for each checkout running a fixed set of
      `git --no-optional-locks` reads wrapped in `timeout`. Everything read-only;
      **no network call** (no `ls-remote`).
- [ ] A parser converts the streamed output into `[]checkouts.Checkout` rows,
      using a delimiter **distinct from the stats heartbeat's** delimiter.
- [ ] Push-state classification keys off the **remote-tracking ref**
      `refs/remotes/<remote>/<branch>` via `rev-list --count <tracking>..HEAD`:
      `0` ⇒ `pushed`; `>0` ⇒ `unpushed` (with the count as `Ahead`); **no
      tracking ref** ⇒ `never`. It must NOT key off the configured upstream (a
      `git push origin HEAD` without `-u` updates the tracking ref but sets no
      upstream).
- [ ] The configured **remote is read, not assumed to be `origin`**, and its URL
      is parsed to `(forge host, org/repo)` for both SSH
      (`git@github.com:org/repo.git`) and HTTPS
      (`https://github.com/org/repo.git`) forms; GitHub and GitLab hosts both
      classify.
- [ ] Worktree `.git` **files** are recorded as `Kind=worktree` and linked to
      their parent repo path; normal `.git` **dirs** as `Kind=repo`.
- [ ] Truncation (caps hit) is represented as a flag on the result, never
      silently dropped.
- [ ] `go test ./internal/checkouts/... -race` passes with table-driven cases
      for: pushed/unpushed/never (incl. the `-u`-less push case), SSH vs HTTPS
      remote URL parsing, GitHub vs GitLab host, a worktree pointer row, a
      non-`origin` remote, a dirty count, and a truncation flag.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Keep this package free of `limactl`/Bubble Tea imports — it operates on
  strings (the command it emits, the output it parses). The actual shell exec is
  tasks 3 and 9.
- Mirror the heartbeat's "deliberately dumb guest side" philosophy
  (`internal/ui/heartbeat.go` doc comment): a plain `find` + a handful of
  read-only `git` reads, no bespoke guest program. Escape/quote the guest
  command safely.
- Classification consumes and produces the `internal/checkouts` types from task 1.

## Input Dependencies
- Task 1: the `Checkout` / `VMCheckouts` types and `PushState`/`Kind` enums.

## Output Artifacts
- `internal/checkouts/sweep.go` (or similar): `BuildSweepCommand()` and
  `ParseSweep(raw string) VMCheckouts`, plus remote-URL and push-state helpers,
  all reused by tasks 3 and 9.

## Implementation Notes
<details>
<summary>Detailed implementation guidance</summary>

1. **Guest command** — emit a single shell string, e.g. a `find "$HOME" -maxdepth
   <N> \( -name node_modules -o -name .cache \) -prune -o -name .git -print` that
   yields both dir and file `.git` entries, capped. Then, for each discovered
   checkout dir, run a `timeout <sec> git --no-optional-locks -C <dir>` block
   emitting delimited key=value lines:
   - `branch`: `git ... symbolic-ref --short HEAD` (detached ⇒ empty/skip).
   - `remote`: `git ... config --get branch.<branch>.remote` (fallback: first
     remote from `git remote`); then `git ... remote get-url <remote>`.
   - `pushstate`: resolve tracking ref `refs/remotes/<remote>/<branch>`; if it
     exists, `git ... rev-list --count refs/remotes/<remote>/<branch>..HEAD` →
     ahead; also `..` reversed for behind if useful; if the ref is absent, mark
     `never`.
   - `dirty`: `git ... status --porcelain | wc -l`.
   Wrap the whole per-repo block in `timeout` so one pathological repo can't
   stall the sweep. Emit a per-checkout record delimiter and a final
   end-of-sweep sentinel; include a truncation marker line when the count cap
   trips.
2. **Parser** — split on your sweep delimiter (distinct from the stats stream's),
   build one `Checkout` per record, set `Kind` by whether the discovered `.git`
   was a file (worktree) or dir (repo), and link worktree parents (parse the
   `gitdir:` pointer or group by nearest enclosing repo).
3. **Remote URL parsing** — handle:
   - `git@github.com:org/repo.git` → forge `github.com`, orgrepo `org/repo`.
   - `https://github.com/org/repo(.git)` → same.
   - `https://gitlab.com/group/sub/repo.git` → forge `gitlab.com`,
     orgrepo `group/sub/repo` (GitLab allows nested groups; keep the full path).
   Strip a trailing `.git`.
4. **Push state** — the tracking-ref rule is load-bearing (see plan). Test the
   `-u`-less case explicitly: tracking ref present, no `branch.<b>.merge` config,
   still classified `pushed`.
5. Tests are the deliverable's core — table-driven, feeding synthetic raw sweep
   text through `ParseSweep`. Follow RED→GREEN→REFACTOR for the classification
   and URL-parsing logic. No test needed for the trivial command-string constant
   pieces beyond one golden assertion that caps/prunes are present.
</details>
