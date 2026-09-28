---
id: 1
group: "release-checking"
dependencies: []
status: "completed"
created: 2026-09-28
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
skills:
  - go-concurrency
  - http-state
complexity_score: 9
complexity_notes: "Cross-process reservation, atomic cache merging, trusted URL construction, and quiet network failure behavior make this concurrency-sensitive infrastructure."
---
# Implement the release checker and cache

## Objective
Implement the dependency-free release-checking subsystem that normalizes Sand versions, builds trusted release-note links, reserves at most one GitHub check per 24-hour window across processes, performs the timeout-bounded request, and preserves the last valid result across quiet failures.

## Skills Required
Go concurrency and cross-process state coordination; standard-library HTTP/JSON and durable cache handling.

## Acceptance Criteria
- [ ] Stable `vMAJOR.MINOR.PATCH` and `MAJOR.MINOR.PATCH` versions compare numerically; development, dirty, malformed, draft, and prerelease identities are never update-eligible.
- [ ] Installed and latest release-note URLs are fixed to the HTTPS `github.com/Lullabot/sandbar` release namespace and unvalidated response strings never reach an OSC 8 or browser target.
- [ ] `${XDG_CACHE_HOME:-~/.cache}/sandbar/release-check.json` stores only the last attempt and last validated release metadata through tolerant reads and atomic writes.
- [ ] A due check atomically reserves its attempt under an acquired/not-acquired cross-process lock before HTTP; concurrent callers produce one reservation/request, and lock or reservation-write failure skips networking.
- [ ] Success merges a validated stable release without rolling back a newer reservation; timeout, offline, HTTP, rate-limit, and malformed-response failures retain the previous valid release, remain quiet, and consume the same 24-hour window.
- [ ] The current fail-open contract of `statelock.Acquire` remains unchanged for registry and secrets callers; any strict lock API is additive and directly tested.
- [ ] `go test ./internal/statelock ./internal/releasecheck` exits 0 and includes deterministic concurrent-reservation, failed-attempt throttle, corrupt/unwritable cache, URL rejection, and HTTP-server cases.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
Use only the Go standard library plus existing repository packages. Provide a small package boundary consumable by the TUI, with injectable clock, HTTP endpoint/client, cache path, and lock behavior for tests. Use a bounded HTTP timeout and GitHub's latest published release endpoint. Do not run network requests from package initialization or from render functions.

## Input Dependencies
Plan 23's refined cache, comparison, trust-boundary, concurrency, and quiet-failure contracts; existing `internal/statelock` and atomic store patterns.

## Output Artifacts
A tested internal release-checking package and any additive strict lock primitive required for fail-closed optional reservations.

## Implementation Notes
<details>
<summary>Execution guidance</summary>

Keep the API plain: cached release metadata, a startup/due result, and comparison/link helpers are sufficient. Under the cache lock, re-read the file, decide due status, and persist `last_attempt` before releasing. A caller that cannot prove the reservation was persisted must not call GitHub. After HTTP, lock and re-read again before merging the response so a stale completion cannot overwrite a newer timestamp. Treat a missing/corrupt cache as empty, but do not quarantine or mutate existing profile/registry/secrets files.

Meaningful tests verify custom business logic, critical paths, edge cases, and integration points. Test this package's version comparison, state transition, locking, and HTTP behavior; do not test `net/http`, JSON, or filesystem library functionality in isolation. Favor table tests and a local HTTP server, and use channels/barriers rather than sleeps for the reservation race.

</details>
