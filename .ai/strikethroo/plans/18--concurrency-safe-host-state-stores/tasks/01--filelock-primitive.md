---
id: 1
group: "lock-primitive"
dependencies: []
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "high"
skills:
  - go
  - file-locking
---
# internal/filelock cross-process lock primitive

## Objective

Create a new `internal/filelock` package that provides the single cross-process
mutual-exclusion primitive the registry, secrets, and profiles stores will
share — a best-effort, bounded-blocking advisory file lock — without coupling
those low-level store packages to the `lima` abstraction.

## Skills Required

- `go` — new package, `syscall.Flock`, fd lifetime management.
- `file-locking` — advisory (`flock`) lock semantics, best-effort posture.

## Acceptance Criteria

- [ ] New package `internal/filelock` exposes a minimal API: acquire an
      exclusive advisory lock on a given lock-file path, returning a release
      function (`func()`) and an error. Suggested shape:
      `func Acquire(lockPath string) (release func(), err error)`.
- [ ] Acquire opens (creating if needed, mode 0600) the lock file and takes an
      exclusive advisory lock via `syscall.Flock(fd, LOCK_EX)`; the release
      function unlocks and closes the fd.
- [ ] **Best-effort posture**: any failure to create/open/lock the file returns
      a release that is a safe no-op plus a sentinel the caller can detect (e.g.
      a non-nil error together with a usable no-op release), so the caller
      proceeds *unserialized* rather than failing. Acquire must never make the
      caller's operation impossible.
- [ ] **Bounded acquire**: the exclusive acquire is bounded by a short deadline
      (e.g. a few seconds) — on expiry it degrades to the best-effort no-op
      path rather than blocking forever.
- [ ] **Non-re-entrant by contract**: documented in the package/func doc that a
      second Acquire of the same path from the same process (a distinct open
      file description) will block; callers must acquire exactly once per
      operation and never re-acquire while holding it.
- [ ] Package doc comment explains the local-only scope, the best-effort
      posture, and that it deliberately does **not** share code with
      `internal/provision/baselock.go` (which needs the remote-SSH variant).
- [ ] Unit test: two sequential Acquire/release cycles on one path succeed; an
      Acquire against a path whose parent dir cannot hold a lock file (inject a
      failure / use an unwritable dir) returns the best-effort no-op + error and
      does NOT panic. Verify with `go test ./internal/filelock/...`.
- [ ] `go build ./...` and `go vet ./...` pass.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Model the implementation on `internal/lima/hostfiles.go:146-165` (`localLock`,
  which does `os.OpenFile` + `syscall.Flock`) — copy the *pattern*, not import
  it. Use the standard library `syscall` package (as `hostfiles.go` does), not
  `golang.org/x/sys`.
- Unlike `baselock` (which polls `TryLock(LOCK_EX|LOCK_NB)` every 250ms while
  honoring a `context`), this primitive guards a sub-millisecond write, so a
  simple bounded blocking `LOCK_EX` is sufficient and it does NOT take a
  `context` argument.
- No dependency on `lima`, `lima.HostFiles`, or any remote/SSH path.

## Input Dependencies

None — this is the foundation package.

## Output Artifacts

- `internal/filelock/filelock.go` (+ `filelock_test.go`) exposing the
  `Acquire` API and best-effort/no-op sentinel that Tasks 2, 3, and 4 consume.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

Create `internal/filelock/filelock.go`:

```go
// Package filelock provides a small, local-only, best-effort cross-process
// advisory file lock used to serialize read-modify-write updates to sand's
// host-side state stores (registry, secrets, profiles).
//
// It deliberately does not share code with internal/provision/baselock.go:
// baselock must also work over SSH for remote Lima hosts, whereas these three
// stores are always local to the machine running the binary.
//
// Posture: best-effort. If the lock cannot be created or acquired, Acquire
// returns a non-nil error together with a no-op release so the caller proceeds
// UNSERIALIZED (today's behavior) rather than failing the underlying write.
//
// Non-re-entrant: a second Acquire of the same path from the same process uses
// a distinct open file description and will block. Acquire exactly once per
// operation; never re-acquire while holding the lock.
package filelock
```

- `Acquire(lockPath string) (func(), error)`:
  - `os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)`. On error → return
    `func(){}, err`.
  - Take the lock with a bounded wait. Simplest robust approach: run
    `syscall.Flock(fd, syscall.LOCK_EX)` in a goroutine and select against a
    `time.After(deadline)`; on timeout close the fd and return the no-op+err.
    (A `LOCK_EX|LOCK_NB` poll loop with a short sleep + deadline is an equally
    acceptable implementation.)
  - On success return a release closure that does
    `syscall.Flock(fd, syscall.LOCK_UN); f.Close()` exactly once (guard with a
    `sync.Once`).
- The release returned on the failure path must be a safe no-op (never nil), so
  callers can `defer release()` unconditionally.
- Test seam: to test the failure/degradation path deterministically, allow the
  open to fail naturally by pointing at a path under a non-existent, non-creatable
  directory, OR factor the `os.OpenFile` call behind a package var the test can
  override. Assert: mutation-side callers still get a usable no-op release.

Keep it tiny — this is one file plus one test file.
</details>
