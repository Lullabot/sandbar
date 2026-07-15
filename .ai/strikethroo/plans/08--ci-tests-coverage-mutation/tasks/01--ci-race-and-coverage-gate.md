---
id: 1
group: "ci-wiring"
dependencies: []
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "high"
skills:
  - github-actions
  - go-coverage
---
# CI: add -race and a self-contained coverage gate to the `unit` job

## Objective
Extend the existing blocking `unit` job in `.github/workflows/test.yml` to run the Go suite with the race detector and enforce a self-contained coverage floor, uploading the profile + HTML report as build artifacts. No third-party coverage service. This is CI-only — no production Go code is edited.

## Skills Required
`github-actions` (edit the workflow job), `go-coverage` (`-coverpkg`, profile filtering, `go tool cover`).

## Acceptance Criteria
- [ ] The `unit` job runs `go test ./... -race -covermode=atomic -coverpkg=./internal/... -coverprofile=coverage.out` from the repo root.
- [ ] A committed floor value (a file or an inline step var, e.g. `COVERAGE_FLOOR=85`) is compared against the measured `./internal/...` total; the job **fails** when the total is below the floor.
- [ ] The raw `coverage.out` and a generated `coverage.html` (`go tool cover -html`) are uploaded via `actions/upload-artifact`.
- [ ] Verification: running the job's exact commands locally prints a total ≥ the floor and exits 0. Then temporarily set the floor to `99` and confirm the gate step exits non-zero (prove the gate bites); restore to the real floor.
- [ ] `limae2e`-tagged tests remain excluded (no `-tags limae2e` on this job); `lint` and `lima-e2e` jobs are unchanged.

## Technical Requirements
- Module is at the repo root (`github.com/lullabot/sandbar`, go 1.25). The `unit` job today runs `go vet ./...` then `go test ./...` with no `-race` and no coverage.
- Gated denominator is `./internal/...` only (excludes the `cmd/sand` main glue), which currently totals **86.6%**; set the floor just under it at **85**.
- `-covermode=atomic` is required under `-race`.

## Input Dependencies
None.

## Output Artifacts
An updated `unit` job with `-race` + coverage gate; a committed floor value that task 9 later bumps.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- Edit only `.github/workflows/test.yml`. Keep `go vet ./...` as a prior step.
- Compute the total with a small shell step:
  ```bash
  go test ./... -race -covermode=atomic -coverpkg=./internal/... -coverprofile=coverage.out
  total=$(go tool cover -func=coverage.out | awk '/^total:/ {print substr($3, 1, length($3)-1)}')
  go tool cover -html=coverage.out -o coverage.html
  awk -v t="$total" -v f="$COVERAGE_FLOOR" 'BEGIN{ if (t+0 < f+0) { printf "coverage %.1f%% below floor %s%%\n", t, f; exit 1 } printf "coverage %.1f%% >= floor %s%%\n", t, f }'
  ```
- Store the floor as a job-level `env: COVERAGE_FLOOR: "85"` (a repo-committed value that a human bumps in a PR — task 9 does the first bump). Do NOT auto-commit from CI.
- Upload `coverage.out` and `coverage.html` with `actions/upload-artifact@v4` (`if: always()` so a failing gate still surfaces the report).
- Use Go module/build caching (`actions/setup-go` with `cache: true` via `go.mod`).
- Do not add a `-tags limae2e` flag; the tag auto-excludes the real-VM tests from this job.
</details>
