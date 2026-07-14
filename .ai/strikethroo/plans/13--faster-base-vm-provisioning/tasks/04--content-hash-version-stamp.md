---
id: 4
group: "tier-1-staleness"
dependencies: [1]
status: "completed"
created: 2026-07-13
model: "sonnet"
effort: "high"
skills:
  - go
complexity_score: 7
complexity_notes: "Correctness-critical: every downstream staleness, tool-set, and refresh mechanism keys off this stamp. A wrong hash either never invalidates (serving a stale base forever) or always invalidates (rebuilding on every create)."
---
# Re-base the version stamp on playbook content instead of git HEAD

## Objective

Replace the git-HEAD-plus-`-dirty` base version stamp with a content hash of the playbook fileset plus the tool-set selection. The current stamp is **inert outside a git checkout** — for a released/Homebrew binary the base is never rebuilt and no stamp is ever written, which would make the tool-set and the entire staleness redesign dead code for those users.

## Skills Required

- **go** — `internal/provision/baseversion.go`, `internal/provision/provision.go`, and the embedded playbook FS.

## Acceptance Criteria

- [x] The stamped value is a content hash (e.g. SHA-256) over the playbook fileset, combined with a tool-set selection string.
- [x] The stamp is computed and written **outside a git checkout** (embedded FS / released binary). Verify with a test that builds the version from a non-git directory and asserts a non-empty stamp.
- [x] The hash covers **exactly** the fileset that reaches the guest — the same set the `go:embed` directives in `playbook_embed.go` and the rsync filter in `provision.go` agree on (already pinned by `TestGuestSyncCopiesOnlyThePlaybook`).
- [x] Changing a playbook file changes the stamp. Verify with a test.
- [x] A commit that touches **no** playbook file does **not** change the stamp. Verify with a test (this is a property the old git-HEAD scheme got wrong).
- [x] Changing the tool-set selection string changes the stamp. Verify with a test.
- [x] The `-dirty` suffix mechanism is removed — it is unnecessary once content is hashed.
- [x] An **old-format (git-hash) stamp is treated as stale**, so upgrading users converge once rather than silently serving a base built by the old scheme. Verify with a test.
- [x] An unreadable/absent stamp still counts as stale (preserve today's behavior).
- [x] `go vet ./...` and `go test ./...` are green.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- `internal/provision/baseversion.go`: today `gitPlaybookVersion` runs `git rev-parse HEAD` on the playbook dir and appends `-dirty` when `git status --porcelain` is non-empty. The stamp lives at `$LIMA_HOME/_sand/<baseName>.playbook-version` (fallback `~/.lima/_sand/…`), dir `0755`, file `0644`, content `version + "\n"`.
- `internal/provision/provision.go` `baseStale` (~:302-318): a `playbookVersionFn` **error** is treated as **NOT stale** (deliberate: "better to reuse the base than to rebuild it on every create"). That is precisely the branch that makes a non-git install never rebuild. After this task, the content hash cannot fail for that reason, so the error branch should become genuinely exceptional.
- `BuildBase` cannot stamp when the version lookup fails — so today a non-git install never writes a stamp at all.
- The playbook resolution order (working tree when present, embedded FS otherwise) is unchanged and must be respected: hash whatever source will actually be sent to the guest, or the stamp describes a tree the base was not built from.

## Input Dependencies

- Task 1 only for sequencing (no code dependency).

## Output Artifacts

- A content-hash version function in `baseversion.go` that accepts a tool-set selection string.
- The stamp format that tasks 5, 7, 8 and 9 all key off.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Hash the fileset deterministically.** Order matters — walk paths in sorted order and hash both the path and the content, so a rename is detected:

```go
// playbookContentHash hashes exactly the fileset that reaches the guest:
// the same set playbook_embed.go embeds and the rsync filter copies. The
// drift test (TestGuestSyncCopiesOnlyThePlaybook) pins those two together,
// so it now guards this hash too.
func playbookContentHash(fsys fs.FS) (string, error) {
    var paths []string
    err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
        if err != nil {
            return err
        }
        if !d.IsDir() {
            paths = append(paths, p)
        }
        return nil
    })
    if err != nil {
        return "", err
    }
    sort.Strings(paths)

    h := sha256.New()
    for _, p := range paths {
        b, err := fs.ReadFile(fsys, p)
        if err != nil {
            return "", err
        }
        fmt.Fprintf(h, "%s\n%d\n", p, len(b))  // path + length frame the content
        h.Write(b)
    }
    return hex.EncodeToString(h.Sum(nil)), nil
}
```

When the playbook is a working-tree directory rather than the embedded FS, use `os.DirFS(playbookDir)` — but **filter it to the same fileset** the rsync filter uses (`site.yml`, `ansible.cfg`, `inventory`, `roles/**`, `group_vars/**`), or stray files in the checkout will perturb the hash.

**Combine with the tool-set.** The final stamped version is the content hash plus a canonical, stable rendering of the selection:

```go
// version = "v2:" + sha256(playbook fileset) + ":" + toolset
// The "v2:" prefix is what lets baseStale recognise (and invalidate) an
// old-format git-hash stamp.
func PlaybookVersion(fsys fs.FS, toolset string) (string, error) {
    h, err := playbookContentHash(fsys)
    if err != nil {
        return "", err
    }
    return "v2:" + h + ":" + toolset, nil
}
```

Task 5 supplies the real `toolset` string. For **this** task, thread the parameter through and pass a fixed placeholder (e.g. the default all-three selection) so the signature is ready; task 5 wires the actual value.

**Old-format stamps are stale.** In `baseStale`, a stamp that does not begin with the `v2:` prefix must return `true` (stale) — an upgrading user converges once. The existing empty-stamp path already returns `true`, so this is a natural extension:

```go
if have == "" || !strings.HasPrefix(have, "v2:") {
    step(out, "Base image %q was built by an older version scheme; converging it…", baseName)
    return true
}
```

**Do not** add a timestamp here — task 9 adds the build timestamp for the 30-day refresh. Keep this task to the content hash.

**Tests.** Table-driven, using `fstest.MapFS`:
- Same files → same hash. Changed file content → different hash. Renamed file → different hash. Extra unrelated file outside the fileset → same hash.
- Different toolset string → different version.
- `baseStale` with a `v1`-looking (40-hex) stamp → true.
- Building a version from a non-git `fstest.MapFS` → succeeds (this is the whole point of the task).

</details>
