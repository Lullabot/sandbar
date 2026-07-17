---
id: 3
group: "store-conversion"
dependencies: [1]
status: "completed"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 8
complexity_notes: "Concurrency + security-sensitive (real secret values, 0600/0700/fsync invariants must be preserved while wrapping the write path)."
skills:
  - go
  - concurrency
---
# Secrets store: locked reload-merge

## Objective

Convert `internal/secrets/secrets.go` mutations (`Set`, `SetAll`, `Remove`) into
lock-protected read-modify-writes against the current on-disk store, using the
same non-saving reload pattern established in Task 2, while preserving every
security-critical property of the secrets write path.

## Skills Required

- `go` — refactoring the store write path.
- `concurrency` — cross-process lock correctness, per-key merge semantics.

## Acceptance Criteria

- [ ] `Set` (~secrets.go:332), `SetAll` (~368), and `Remove` (~418) each:
      acquire the secrets lock (`<datadir>/secrets.json.lock`) via
      `internal/filelock`, reload the on-disk store via a **non-saving,
      side-effect-free parse** (shares `LoadFrom`'s v1/v2/v3 version detection /
      in-memory migration but does NOT `save()`, quarantine, or re-acquire the
      lock), apply only their per-`(connScope, vm)` delta to the reloaded tree,
      write via the existing atomic body, release, then refresh in memory.
- [ ] Security invariants are preserved unchanged (the lock+reload wrap around
      them, they are not rewritten): temp file created at 0600 before any secret
      bytes (~490), forced 0700 parent dir (~474/479), `tmp.Sync()` fsync before
      rename (~500→509), and key/scope validation before any mutation.
- [ ] Delta semantics: a concurrent `Set` on VM A and `Set`/`Remove` on VM B
      both survive; a write for one `(connScope, vm)` never discards another's.
- [ ] Best-effort posture: on lock-acquire failure the mutation still completes
      (unserialized) and emits a visible note through the store's existing
      warning channel.
- [ ] The `.corrupt` quarantine and version->3 refusal behavior remain on the
      `Load()` path.
- [ ] The store's doc comment is updated to state writes are lock-protected
      read-modify-writes.
- [ ] Tests (`go test ./internal/secrets/...`):
      - two `Store` instances on one temp file: interleaved `Set`/`Remove` on
        different VMs both survive;
      - security invariants under a locked write: temp file is 0600, parent dir
        forced 0700, fsync precedes rename (mirror the existing secrets tests);
      - re-entrancy: a mutation against a v1/v2 file that would migrate completes
        without deadlock and the reload performs no `save()`.
- [ ] `go build ./...`, `go vet ./...`, `go test ./...` pass.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Reuse Task 1's `internal/filelock.Acquire` and Task 2's non-saving-parse
  pattern (factor the analogous pure parse from `secrets.LoadFrom`, ~234-308).
- Acquire the lock exactly once at the mutation boundary; neither the reload nor
  the inner atomic-write body may re-acquire it.
- Do NOT change file modes, fsync, or validation ordering — only wrap them.

## Input Dependencies

- Task 1: `internal/filelock.Acquire`.
- Task 2: the non-saving-parse + locked-mutation pattern to mirror (soft — this
  task can proceed in parallel but should match Task 2's approach).

## Output Artifacts

- Converted `internal/secrets/secrets.go` + tests.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

Mirror Task 2 exactly, adapted to the secrets tree (nested
`map[connScope]map[vm]map[key]value` or equivalent):

```go
func (s *Store) Set(scope registry.Scope, vm, key, val string) error {
    if err := validate(...); err != nil { return err } // BEFORE lock/mutation
    release, err := filelock.Acquire(s.path + ".lock")
    if err != nil { s.warnf("proceeding without lock: %v", err) }
    defer release()
    cur, err := s.reloadUnlocked() // read bytes + non-saving parse
    if err != nil { return err }
    cur.set(scope, vm, key, val)   // the one delta
    if err := s.saveTree(cur); err != nil { return err } // existing 0600/0700/fsync body
    s.data = cur
    return nil
}
```

- Keep the 0600-before-write, 0700-dir-force, and fsync-before-rename bytes of
  the existing `save()` intact inside `saveTree`.
- `SetAll` applies its full replacement for its one `(connScope, vm)` subtree
  only, against the reloaded tree — not a whole-store overwrite.
- `Remove` deletes exactly its `(connScope, vm)` (or key) from the reloaded tree.

Test philosophy — write a few tests, mostly integration: cover the merge and the
security invariants under the new locked path; do not re-test the JSON library or
add trivial getters' tests.
</details>
