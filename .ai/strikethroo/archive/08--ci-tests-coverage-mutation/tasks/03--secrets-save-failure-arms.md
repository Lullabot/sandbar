---
id: 3
group: "tier1-tests"
dependencies: []
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "high"
skills:
  - go-testing
---
# Tier-1: cover the secrets atomic-write failure arms

## Objective
Lock in the atomicity + no-world-readable-window guarantees of the on-disk secrets store by testing `save()`'s failure/cleanup arms, which are currently exercised only on the happy path. Drive them with filesystem-state fault injection — no production code edits. Security-sensitive.

## Skills Required
`go-testing` — stdlib tests with temp dirs, permission manipulation, and error-path assertions.

## Acceptance Criteria
- [ ] Tests in `internal/secrets/secrets_test.go` drive at least: (a) the `os.Chmod(dir, 0o700)` failure arm and (b) a temp-file/`os.Rename` failure arm, asserting `save()` returns an error and does not leave a partial or world-readable `secrets.json`.
- [ ] Each covered arm uses filesystem state to force the failure (e.g. a read-only parent directory, or a path collision where the target/temp name is a directory), not a production-code seam.
- [ ] Any arm that cannot be reached without adding a code seam is **explicitly flagged** in the test file (a comment) and in the task's Output note as a follow-up — it is NOT covered by editing `internal/secrets/secrets.go`.
- [ ] Verification: `go test ./internal/secrets/ -run Save -v` passes; `go test ./internal/secrets/ -cover` shows `save`-related lines increased (compare `go tool cover -func` before/after on `secrets.go:save`).
- [ ] No production `.go` files changed.

## Technical Requirements
- `internal/secrets/secrets.go`: `save()` at line 338 — `os.Chmod(dir, 0o700)` at 348; `os.CreateTemp` + `Write`/`Sync`/`Close` + `os.Rename(tmpName, s.path)` at ~359–378. Temp file is created at 0600 (comment in source) so there is no world-readable window on the happy path — the tests must prove the failure arms don't introduce one.
- Running the whole suite as an unprivileged user: a read-only dir (`0o500`) blocks create/rename; a directory placed where a file is expected forces `Rename`/`CreateTemp` errors. Note that `root` ignores mode bits — guard such tests with a skip if `os.Geteuid()==0`.

## Input Dependencies
None.

## Output Artifacts
Added failure-arm tests in `internal/secrets/secrets_test.go`; a documented list of any arm flagged as unreachable-without-a-seam (follow-up).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- **Test philosophy (write a few tests, mostly integration):** verify custom business logic, critical paths, and edge cases specific to this app — test our code, not the framework/stdlib. Combine related scenarios into one task; favor integration/critical-path over per-method units; don't test trivial getters or stdlib behavior.
- Construct a `Store` pointed at a path inside a temp dir (mirror existing `secrets_test.go` construction helpers). To force the chmod/create/rename failures, `os.Chmod(dir, 0o500)` the parent then attempt a `Set`/`SetAll` that triggers `save()`; assert the returned error is non-nil and that no `secrets.json` (or a valid prior one only) exists — never a half-written temp promoted to the real path.
- Add `if os.Geteuid() == 0 { t.Skip("permission arms need a non-root euid") }` to permission-based cases so CI (non-root) exercises them but a root run doesn't spuriously fail.
- If (e.g.) the `tmp.Sync()` arm genuinely can't be provoked from the filesystem, leave a `// FOLLOW-UP:` comment naming it and the seam it would need; do not edit `secrets.go`.
- No `t.Parallel()` — these mutate shared filesystem state and the suite is serial by design.
</details>
