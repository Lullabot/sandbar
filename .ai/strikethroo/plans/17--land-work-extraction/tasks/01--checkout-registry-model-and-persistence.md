---
id: 1
group: "detection-registry"
dependencies: []
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Foundational data model + concurrency contract + host-persisted JSON store; every other component reads it. Concurrency risk floor applies."
skills:
  - go
  - concurrency
---
# Checkout registry: data model and host-persisted store

## Objective
Establish the single source of truth that maps a VM to its GitHub branches: a
per-VM checkout registry with a pointer-held, mutex-guarded in-memory store and a
host-persisted JSON file, keyed by profile connection + VM name. This is the spine
every other land component reads (badge, delete guard, Landing pane, CLI).

## Skills Required
- **go** — package/type design, `encoding/json`, atomic file rewrite.
- **concurrency** — the Bubble Tea by-value-model contract: pointer-held state,
  mutex-guarded writes, value-copy reads, verified under `-race`.

## Acceptance Criteria
- [ ] A new package (e.g. `internal/checkouts`) defines a `Checkout` row type
      carrying: `Path`, `Kind` (repo | worktree), `Parent` (parent repo path for
      worktrees), `Branch`, `Forge` (host, e.g. `github.com`), `OrgRepo`
      (`org/repo`), `PushState` (pushed | unpushed | never-pushed), `Ahead`,
      `Behind`, `Dirty` (int count), and `LastSeen` (time).
- [ ] A per-VM registry aggregates rows keyed by **profile connection + VM name**
      (mirroring `internal/secrets` / `internal/registry` connection-scoping so a
      same-named VM on two profiles never collides).
- [ ] The store is pointer-held and mutex-guarded: writes go through a method
      that takes the lock; reads return value copies (no shared slice/map aliasing).
- [ ] Host persistence writes a single JSON file under
      `${XDG_DATA_HOME:-$HOME/.local/share}/sandbar/` (sibling to `secrets.json`
      and `managed-vms.json`), mode `0600`, via **atomic rewrite** (temp file +
      rename), single writer.
- [ ] Load-on-start tolerates a missing/empty/corrupt file (returns an empty
      registry, does not panic).
- [ ] `go test ./internal/checkouts/... -race` passes with: a persistence
      round-trip test (write → reload → deep-equal), a concurrent read/write test
      that the race detector clears, and a connection-scoping collision test
      (same VM name, two connections, no overwrite).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Follow the existing host-state conventions in `internal/registry/registry.go`
  (`managed-vms.json`, atomic rewrite, XDG path helper) and
  `internal/secrets/secrets.go` (`secrets.json`, connection-scoped keys). Reuse
  the same path-resolution idiom rather than inventing a new one.
- The concurrency contract must mirror the heartbeat's sample-state pattern
  (see `internal/ui/heartbeat.go`): Bubble Tea passes the model by value, so
  shared mutable state is pointer-held and guarded by a mutex; the registry is
  updated from a single goroutine/message path and read by value elsewhere.
- Persisted schema must be forward-tolerant: unknown fields ignored on load, new
  optional fields default cleanly.

## Input Dependencies
None. This is the foundation task.

## Output Artifacts
- `internal/checkouts/` package with the `Checkout` row type, the per-VM
  registry type, its mutex-guarded accessors, and JSON load/save helpers.
- Exported constructors/accessors consumed by tasks 2, 3, 4, 5, 7, 9.

## Implementation Notes
<details>
<summary>Detailed implementation guidance</summary>

1. Create `internal/checkouts/registry.go`. Model the row:
   ```go
   type Kind string // "repo" | "worktree"
   type PushState string // "pushed" | "unpushed" | "never" 
   type Checkout struct {
       Path      string
       Kind      Kind
       Parent    string // parent repo path when Kind == worktree, else ""
       Branch    string
       Forge     string // e.g. "github.com", "gitlab.com"
       OrgRepo   string // "org/repo"
       PushState PushState
       Ahead     int
       Behind    int
       Dirty     int
       LastSeen  time.Time
   }
   ```
2. Per-VM value object: `type VMCheckouts struct { Checkouts []Checkout; Truncated bool; SweptAt time.Time }`.
3. The store keyed by connection+VM. Look at how `internal/secrets` and
   `internal/registry` build their key from the connection identity; reuse that
   exact key derivation. Something like `map[string]VMCheckouts` where the key is
   `connKey + "\x00" + vmName`, plus a `sync.Mutex`.
4. Persistence: copy the atomic-rewrite helper shape from
   `internal/registry/registry.go` (write temp in same dir, `chmod 0600`,
   `os.Rename`). Path: reuse the XDG resolver; filename `checkout-registry.json`.
5. Accessors:
   - `Set(conn, vm string, c VMCheckouts)` — locks, updates map, persists.
   - `Get(conn, vm string) (VMCheckouts, bool)` — locks, returns a **deep copy**
     (copy the slice) so callers can't mutate shared state.
   - `Load()` / `Save()` — JSON marshal/unmarshal with the tolerant behavior.
6. Tests (`registry_test.go`): round-trip, `-race` concurrent Set/Get in
   goroutines, and the two-connection same-name collision case. Use `t.TempDir()`
   and set `XDG_DATA_HOME` to it, mirroring existing tests in `internal/registry`.
7. Do NOT wire any TUI or sweep behavior here — this task is the data layer only.
   Keep the package free of Bubble Tea imports.

Follow RED→GREEN→REFACTOR for the persistence and concurrency logic (the
meaningful custom behavior); the plain struct fields need no test.
</details>
