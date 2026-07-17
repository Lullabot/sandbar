---
id: 4
group: "store-conversion"
dependencies: [1]
status: "completed"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 8
complexity_notes: "Concurrency + the trickiest merge: insertion-order slice and lastUsed scalar do not map-union; seedLocal writes during LoadFrom must be kept off the locked reload path."
skills:
  - go
  - concurrency
---
# Profiles store: locked reload-merge

## Objective

Convert `internal/profiles/store.go` mutations (`Add`, `Update`, `Remove`,
`Enable`, `Disable`, `SetLastUsed`) into lock-protected read-modify-writes
against the current on-disk profiles, correctly merging the insertion-order
slice and the `lastUsed` scalar (which do not map-union) and keeping the
seed-on-empty behavior off the locked reload path.

## Skills Required

- `go` — refactoring the store write path.
- `concurrency` — cross-process lock correctness, order/scalar merge semantics.

## Acceptance Criteria

- [ ] `Add` (~181), `Update` (~215), `Remove` (~242), `Enable` (~260),
      `Disable` (~266), `SetLastUsed` (~295) each: acquire the profiles lock
      (`<configdir>/profiles.yaml.lock`) via `internal/filelock`, reload via a
      **non-saving, seed-free, side-effect-free parse**, apply only their delta,
      re-run `validate()` (~324) against the merged set, write via the existing
      atomic body, release, then refresh in memory.
- [ ] The locked reload does NOT trigger `seedLocal()` (today `profiles.LoadFrom`
      writes a seeded file on empty/missing input, ~74/80/131-134). Seeding stays
      on the process-start `Load()` path only.
- [ ] **Insertion order** merges correctly: `Add` appends its one new profile to
      the order as found in the *reloaded* set (which may already include a
      profile another process added); it does not rewrite `order` from this
      process's stale slice. `Remove` deletes exactly its one key from the
      reloaded order.
- [ ] **`lastUsed` scalar** is last-writer-wins: `SetLastUsed` updates only the
      scalar on the reloaded set; a concurrent profile edit updates only its
      profile entry. Both survive because each applies its own narrow delta.
- [ ] Preserved: 0644 file mode (~388), insertion-order stability, seed-local-on-
      empty (on `Load()`), and `.corrupt` quarantine (on `Load()`).
- [ ] Best-effort posture: on lock-acquire failure the mutation still completes
      (unserialized) and emits a visible note.
- [ ] The store's doc comment is updated to state writes are lock-protected
      read-modify-writes.
- [ ] Tests (`go test ./internal/profiles/...`):
      - two `Store` instances on one temp file: concurrent `Add(A)` and `Add(B)`
        yield a file containing both, with a coherent insertion order;
      - `SetLastUsed` on one instance and `Update`/`Add` on the other both
        survive (scalar not clobbered, edit not lost);
      - re-entrancy / no-seed-on-reload: a mutation against an empty/missing file
        completes without deadlock and the reload does not itself seed+save.
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` pass.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Reuse Task 1's `internal/filelock.Acquire` and Task 2's non-saving-parse
  pattern (factor the analogous pure, seed-free parse from `profiles.LoadFrom`).
- Uses `gopkg.in/yaml.v3` (not JSON). `LoadFrom` deliberately does NOT call
  `validate()` — keep that, but DO re-run `validate()` on the merged set inside
  each mutation as today.
- Acquire the lock exactly once at the mutation boundary; the reload and inner
  write must not re-acquire.

## Input Dependencies

- Task 1: `internal/filelock.Acquire`.
- Task 2: the non-saving-parse + locked-mutation pattern to mirror (soft).

## Output Artifacts

- Converted `internal/profiles/store.go` + tests.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

The profiles store holds `profiles map[string]Profile`, an `order []string`, and
a `lastUsed string` (store.go ~:30-33). A naive map-merge loses order and the
scalar, so apply narrow deltas to the reloaded value:

```go
func (s *Store) Add(p Profile) error {
    release, err := filelock.Acquire(s.path + ".lock")
    if err != nil { s.warnf("proceeding without lock: %v", err) }
    defer release()
    cur, err := s.reloadUnlocked()   // read bytes + non-saving, seed-free parse
    if err != nil { return err }
    if _, exists := cur.profiles[p.Name]; !exists {
        cur.order = append(cur.order, p.Name) // append to RELOADED order
    }
    cur.profiles[p.Name] = p
    if err := cur.validate(); err != nil { return err }
    if err := s.saveSet(cur); err != nil { return err } // existing 0644 atomic body
    *s = adopt(cur) // refresh in-memory profiles/order/lastUsed
    return nil
}

func (s *Store) SetLastUsed(name string) error {
    // ... lock + reload ...
    cur.lastUsed = name   // only the scalar
    // validate + save + refresh
}
```

- `reloadUnlocked` on an empty/missing file returns an empty set (NOT a seeded
  one) — seeding only happens via `Load()` at process start. If the file is
  missing at mutation time, treat it as an empty set and let the mutation create
  it via the normal atomic write.
- `Remove`/`Enable`/`Disable`/`Update` each mutate exactly their one profile in
  the reloaded set (and, for `Remove`, drop it from `order`).

Test philosophy — write a few tests, mostly integration: cover the order and
lastUsed merge (the real custom logic here) and the no-seed-on-reload path. Do
not re-test yaml.v3 or add trivial per-field tests.
</details>
