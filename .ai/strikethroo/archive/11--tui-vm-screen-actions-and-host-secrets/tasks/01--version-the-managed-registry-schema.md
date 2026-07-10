---
id: 1
group: "host-state"
dependencies: []
status: "completed"
created: 2026-07-09
model: "sonnet"
effort: "high"
complexity_score: 6
complexity_notes: "Data migration of a user's on-disk index. Must not lose entries from the existing unversioned format, and must refuse a future format rather than misparse it."
skills:
  - go
  - data-migration
---
# Version the managed-registry schema

## Objective

Give `managed-vms.json` an explicit `version` field so future schema changes have
a migration hinge. An existing unversioned file must load with zero data loss and
be rewritten with the version on the next write. A file whose version is newer
than this binary understands must be refused with a clear error, not misparsed.

## Skills Required

- **go** — JSON marshalling, `encoding/json`, atomic file writes.
- **data-migration** — forward/backward compatibility of an on-disk format.

## Acceptance Criteria

- [ ] `internal/registry/registry.go`'s `fileSchema` carries a `Version int` field
      serialized as `"version"`, and a package constant (e.g. `currentVersion = 1`)
      names the version this binary writes.
- [ ] Loading a file with **no** `version` key succeeds, preserves every VM entry
      and its `config`, and is treated as version 1.
- [ ] Loading a file whose `version` is greater than `currentVersion` returns a
      non-nil error whose message tells the user to upgrade `sand`, and does
      **not** return a partially-populated registry.
- [ ] Any save writes `"version": 1` into the file.
- [ ] The existing invariant is preserved: `Add` still clears `CloneToken` before
      persisting, and no token ever appears in `managed-vms.json`.
- [ ] Verification: `go test ./internal/registry/... -v` passes, including new
      tests named for each of the three load cases above. Then run:
      ```
      export XDG_DATA_HOME=$(mktemp -d)
      mkdir -p "$XDG_DATA_HOME/sandbar"
      printf '{"vms":{"old-vm":{"base":"claude-base","config":{"Name":"old-vm","BaseName":"claude-base","CPUs":4}}}}' \
        > "$XDG_DATA_HOME/sandbar/managed-vms.json"
      go test ./internal/registry/... -run Migrat -v
      ```
      Expected: `PASS`, and a test asserting that after a load-then-save round trip
      the file contains both `"version": 1` and the `old-vm` entry with `CPUs: 4`
      intact.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Package: `internal/registry`.
- `fileSchema` today is `{ VMs map[string]entry `json:"vms"` }`. Add
  `Version int `json:"version"``.
- `Load`/`LoadFrom` currently tolerate a missing file (empty registry) and report
  a corrupt file as an error while still returning a usable non-nil registry —
  `internal/ui/model.go` relies on that (`reg, loadErr := registry.Load()`; a nil
  reg falls back to `NewEmpty()`). **Preserve that posture** for corrupt files.
- The version-too-new case is different: it is not corruption, it is a
  deliberate refusal. Still return a usable non-nil empty registry alongside the
  error so the TUI does not crash, but do not populate it from the file.
- Saves must remain atomic if they already are; if they are not, do not change
  that in this task.

## Input Dependencies

None. This task is self-contained in `internal/registry`.

## Output Artifacts

- A versioned `managed-vms.json` schema.
- `currentVersion` constant, available for `internal/secrets` (task 2) to mirror.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

Read `internal/registry/registry.go` in full first. Note the header comment
explaining *why* the index exists (recreate would replace ANY instance it is
pointed at) and the `Add` method's `cfg.CloneToken = ""` line with the comment
"secrets never touch the on-disk index." Neither may be weakened.

**Step 1 — schema.**

```go
// currentVersion is the schema version this binary writes. A file with no
// version predates versioning and is read as version 1.
const currentVersion = 1

type fileSchema struct {
    Version int              `json:"version"`
    VMs     map[string]entry `json:"vms"`
}
```

**Step 2 — load.** After unmarshalling, before populating:

```go
if fs.Version == 0 {
    fs.Version = 1 // unversioned file predates the version field
}
if fs.Version > currentVersion {
    return NewEmpty(), fmt.Errorf(
        "managed index %s has schema version %d, but this sand only understands %d; upgrade sand",
        path, fs.Version, currentVersion)
}
```

Return an **empty** registry in the too-new branch — populating it from a schema
we do not understand is exactly the misparse we are guarding against.

**Step 3 — save.** Wherever `fileSchema` is constructed for writing, set
`Version: currentVersion`.

**Step 4 — tests.** Add to `internal/registry/registry_test.go`:

1. `TestLoad_UnversionedFileMigrates` — write the legacy JSON (no `version` key)
   to a temp `XDG_DATA_HOME`, load, assert `IsManaged("old-vm")` is true and
   `Config("old-vm").CPUs == 4`. Then trigger a save (e.g. `Add` another VM) and
   re-read the raw file, asserting it now contains `"version":1` **and** still
   contains `old-vm` with `"CPUs":4`.
2. `TestLoad_FutureVersionRefused` — write `{"version":99,"vms":{"x":{...}}}`,
   load, assert the error is non-nil, its message contains `upgrade sand`, the
   returned registry is non-nil, and `IsManaged("x")` is **false** (nothing was
   parsed out of the future file).
3. `TestSave_WritesVersion` — fresh registry, `Add` a VM, read the raw file,
   assert `"version":1` is present and no `CloneToken` value appears anywhere in
   the bytes.

For the token assertion, `Add` a `vm.CreateConfig` whose `CloneToken` is a
distinctive sentinel like `"SENTINEL_TOKEN_DO_NOT_PERSIST"`, then
`bytes.Contains(raw, []byte("SENTINEL_TOKEN_DO_NOT_PERSIST"))` must be false.

**Testing philosophy.** Write a few tests, mostly integration. Meaningful tests
verify custom business logic, critical paths, and edge cases specific to this
application — test *your* code, not the framework or library.

Write tests for: custom business logic and algorithms; critical user workflows
and data transformations; edge cases and error conditions for core functionality;
integration points between components; complex validation logic or calculations.

Do NOT write tests for: third-party library functionality; framework features;
simple CRUD operations without custom logic; trivial getters/setters or static
configuration; obvious functionality that would break immediately if incorrect.

Here that means: test the three load cases and the token-stripping invariant. Do
not test `encoding/json` itself.

</details>
