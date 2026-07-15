---
id: 7
group: "ci-wiring"
dependencies: []
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "medium"
skills:
  - github-actions
  - go-mutation-testing
---
# CI: scheduled gremlins mutation-testing job (advisory, core packages)

## Objective
Add a mutation-testing job (gremlins) to `.github/workflows/test.yml` that runs on a weekly schedule + `workflow_dispatch`, scoped to the core packages, reporting a mutation score as an advisory (non-blocking) signal. CI-only.

## Skills Required
`github-actions`, `go-mutation-testing` (gremlins config + invocation).

## Acceptance Criteria
- [ ] A new `mutation` job triggered only by `schedule` (weekly cron) and `workflow_dispatch` — **not** on push/PR.
- [ ] It installs and runs gremlins over the core packages `internal/provision`, `internal/registry`, `internal/vm`, `internal/lima` (a `.gremlins.yaml` scoping config or explicit paths), reporting the mutation score.
- [ ] The job is **advisory/non-blocking** (does not fail the workflow on surviving mutants initially — e.g. `continue-on-error` or a non-gating threshold), and `internal/ui` is excluded.
- [ ] Verification: `yamllint`/`actionlint` (or `gh workflow view`) shows the job parses; the job's trigger set contains only `schedule` + `workflow_dispatch`. Document the local invocation command in the job or task output.
- [ ] No production `.go` files changed.

## Technical Requirements
- gremlins: https://github.com/go-gremlins/gremlins — `gremlins unleash --tags '' ./internal/provision/... ` etc., or a `.gremlins.yaml` with `unleash` scope.
- Weekly cron (e.g. `cron: "0 6 * * 1"`) + `workflow_dispatch`. Reuse `actions/setup-go` with caching.

## Input Dependencies
None.

## Output Artifacts
A `mutation` job in `test.yml`; optional `.gremlins.yaml`; documented local usage (feeds task 9's README).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- Add the trigger to the workflow's top-level `on:` (it already has `push`/`pull_request`/`workflow_dispatch`; add `schedule`). Gate the mutation job with `if: github.event_name == 'schedule' || github.event_name == 'workflow_dispatch'` so it never runs on PRs.
- Install gremlins via `go install github.com/go-gremlins/gremlins/cmd/gremlins@latest` (pin a version) and run it per core package or via a config file. Keep runtime bounded — mutation testing recompiles per mutant.
- Make it advisory: either `continue-on-error: true` or set gremlins' thresholds to non-failing. Do not block merges.
- Do not scope gremlins to `internal/ui` — the 7k-line Bubble Tea view layer against goldens yields mostly equivalent/timeout mutants.
</details>
