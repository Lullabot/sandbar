---
id: 4
group: "tier2-tests"
dependencies: []
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "high"
skills:
  - go-testing
---
# Tier-2: base-version staleness I/O round-trip + lockBase cancel/degrade arms

## Objective
Cover two correctness-sensitive `internal/provision` areas the current tests bypass: the real base-version staleness-stamp file I/O (tests stub the read/write funcs today, so the real path derivation never runs), and the base-image lock's cancellation and graceful-degrade arms. Test-only; concurrency-adjacent.

## Skills Required
`go-testing` — temp-dir/`LIMA_HOME` isolation, `context` cancellation, and error-path assertions.

## Acceptance Criteria
- [ ] A round-trip test calls the **real** `writeBaseVersion` then `readBaseVersion` in a temp `LIMA_HOME`, asserting the value reads back identically and lands at the path `baseVersionPath` derives. (`writeBaseVersion` takes a `builtAt time.Time` in 0.4.0 — pass a fixed time.)
- [ ] `lockBase` tests cover: (a) ctx-cancellation while waiting for a held lock returns `ctx.Err()` promptly, and (b) the three "lock unavailable → proceed unserialized" degrade paths (`MkdirAll` failure, `OpenFile` failure, non-`EWOULDBLOCK` flock error) each return a **no-op release + nil error** so a create proceeds rather than failing.
- [ ] Verification: `go test ./internal/provision/ -run 'BaseVersion|LockBase' -race -v` passes; `go tool cover -func` shows `readBaseVersion`/`writeBaseVersion`/`baseVersionPath` and `lockBase` moved above their prior coverage (baseversion funcs were 0%; `lockBase` ~64%).
- [ ] No production `.go` files changed.

## Technical Requirements
- `internal/provision/baseversion.go`: `baseVersionPath` (~61), `readBaseVersion` (195), `writeBaseVersion(baseName, version, builtAt time.Time)` (237). The path derives from `LIMA_HOME`/`~/.lima`; isolate via `t.Setenv("LIMA_HOME", t.TempDir())`.
- `internal/provision/baselock.go`: `lockBase(ctx, baseName, out)` (74) — flock spin with a `baseLockPoll` ticker; ctx.Done arm at ~109; degrade arms at `MkdirAll` (~66), `OpenFile` (~70), non-`EWOULDBLOCK` (~85).
- The existing tests swap package vars `readBaseVersionFn`/`writeBaseVersionFn`; this task must exercise the **real** functions directly (do not go through the stubs).

## Input Dependencies
None.

## Output Artifacts
New/extended tests in `internal/provision` for baseversion I/O and lockBase edge cases.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- **Test philosophy (write a few tests, mostly integration):** verify custom business logic, critical paths, and edge cases specific to this app — test our code, not the framework/stdlib. Combine related scenarios into one task; favor integration/critical-path over per-method units; don't test trivial getters or stdlib behavior.
- baseversion: `t.Setenv("LIMA_HOME", t.TempDir())`, call `writeBaseVersion("claude-base", "abc123", fixedTime)`, then `readBaseVersion("claude-base")` and assert equality; also assert the file exists under the derived `baseVersionPath`.
- lockBase ctx-cancel: take the lock in the test goroutine (call `lockBase` once, hold the returned release), then call `lockBase` again with a context you cancel after a short delay; assert it returns `ctx.Err()` and does so well under a second. Use `context.WithCancel` and cancel from a `time.AfterFunc`, or cancel right before the call and assert prompt return.
- Degrade arms: for `MkdirAll`/`OpenFile` failure, point the lock dir at an un-creatable/un-openable path (e.g. a `LIMA_HOME` whose parent is a read-only dir, or where a path component is a file); assert `lockBase` returns a usable no-op `release` and `err == nil`. Guard with `os.Geteuid() != 0` where permission-based.
- Keep any concurrency deterministic where possible; if you must sleep, keep it small and run under `-race`. Do NOT add `t.Parallel()`.
</details>
