---
id: 4
group: "data-dir"
dependencies: [3]
status: "completed"
created: "2026-07-03"
skills:
  - go
  - bash
---
# Data dir rename + migrate-then-cleanup of the managed-VM index

## Objective
Adopt `~/.local/share/sandbar` without losing the managed-VM index, and remove the old `claude-code-ansible` location only once migration is proven. Point `registry.defaultPath` and the shell `CACHE_DIR` at `sandbar`; the TUI migrates the index (copy → verify → remove old, `rmdir`-if-empty); the shell scripts delete the old dir **only when it no longer holds a `managed-vms.json`**, so an un-migrated index can never be destroyed regardless of run order.

## Skills Required
- `go` — change `defaultPath`, add the copy→verify→remove migration, and a test.
- `bash` — change `CACHE_DIR` and add a race-safe cleanup guard in `install.sh` / `new-vm.sh`.

## Acceptance Criteria
- [ ] `tui/internal/registry/registry.go`: `defaultPath` builds `.../sandbar/managed-vms.json`; the data-dir comment (~line 47) says `sandbar`.
- [ ] TUI migration: if `$base/sandbar/managed-vms.json` is absent **and** `$base/claude-code-ansible/managed-vms.json` exists, copy it forward, read it back to verify, then remove the old index file and `rmdir` the old dir if empty. Copy-before-remove; a crash mid-migration cannot lose the index.
- [ ] `tui/internal/registry/registry_test.go`: a test seeds a legacy `claude-code-ansible/managed-vms.json`, loads via the default path, and asserts the new file exists + parses and the old index file was removed.
- [ ] `install.sh` and `scripts/new-vm.sh`: `CACHE_DIR` uses `sandbar`; each removes the old `claude-code-ansible` dir **only if it contains no `managed-vms.json`**.
- [ ] `grep -rn 'claude-code-ansible' install.sh scripts/new-vm.sh tui/internal/registry` returns **0**.
- [ ] `cd tui && go test ./...` passes (incl. the new migration test); `shellcheck install.sh scripts/new-vm.sh` passes.

## Technical Requirements
The old dir is **both** the `managed-vms.json` index home and the playbook git-clone cache. Migration is split by owner: the TUI owns the index (sole writer of the migration), the shell scripts own the clone cache and only clean up when no index remains. This makes cleanup race-safe whichever runs first. (Plan 07 deletes these scripts, so the shell-side cleanup is interim.)

## Input Dependencies
- Task 3 (external sweep) — sequenced so `install.sh`/`new-vm.sh` are edited by one task at a time. After this task the repo-wide identity grep should reach zero.

## Output Artifacts
- `~/.local/share/sandbar` data dir with automatic, loss-free index migration; the completeness gate (task 5) can then assert zero old-identity references.

## Implementation Notes

<details>
<summary>Step-by-step</summary>

1. **`registry.go` — `defaultPath`** (~lines 46–57): change the returned path from `filepath.Join(base, "claude-code-ansible", "managed-vms.json")` to `"sandbar"`, and update the doc comment (~47) to reference `sandbar`.

2. **Add migration** in `registry.go`. Introduce a helper, e.g.:
   ```go
   // migrateLegacyIndex copies a pre-rename managed index from the old
   // claude-code-ansible data dir into the new sandbar dir exactly once,
   // copy-before-remove so a crash cannot lose it.
   func migrateLegacyIndex(newPath string) {
       if _, err := os.Stat(newPath); err == nil {
           return // new index already present; nothing to do
       }
       base := filepath.Dir(filepath.Dir(newPath)) // .../.local/share
       oldPath := filepath.Join(base, "claude-code-ansible", "managed-vms.json")
       data, err := os.ReadFile(oldPath)
       if err != nil {
           return // no legacy index
       }
       if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
           return
       }
       if err := os.WriteFile(newPath, data, 0o600); err != nil {
           return
       }
       // verify the new file reads back before removing the old one
       if back, err := os.ReadFile(newPath); err != nil || len(back) != len(data) {
           return
       }
       _ = os.Remove(oldPath)
       _ = os.Remove(filepath.Join(base, "claude-code-ansible")) // rmdir if empty
   }
   ```
   Call `migrateLegacyIndex(p)` from the default-path load entry point (e.g. in `Load()` before `LoadFrom(defaultPath())`, or at the top of `LoadFrom` when `p == defaultPath()`), so it runs before the first read. Match the existing file style; reuse the file's existing imports (`os`, `filepath`).

3. **`registry_test.go`** — add a focused migration test using `t.TempDir()` and `XDG_DATA_HOME` (or a test seam if one exists): create `<tmp>/claude-code-ansible/managed-vms.json` with one managed entry, run the load path, then assert (a) `<tmp>/sandbar/managed-vms.json` exists and parses to the same entry, and (b) `<tmp>/claude-code-ansible/managed-vms.json` no longer exists. Follow the patterns already in this test file.

4. **`install.sh`** — `CACHE_DIR` (~line 16) → `"${XDG_DATA_HOME:-$HOME/.local/share}/sandbar"`. Add a race-safe cleanup near where `CACHE_DIR` is set:
   ```bash
   OLD_CACHE_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/claude-code-ansible"
   if [ -d "$OLD_CACHE_DIR" ] && [ ! -f "$OLD_CACHE_DIR/managed-vms.json" ]; then
     rm -rf "$OLD_CACHE_DIR"
   fi
   ```

5. **`scripts/new-vm.sh`** — `CACHE_DIR` (~line 24) → `sandbar`; add the same `OLD_CACHE_DIR` guard/cleanup block.

6. **Verify**:
   ```bash
   cd /home/debian/claude-code-ansible
   grep -rn 'claude-code-ansible' install.sh scripts/new-vm.sh tui/internal/registry
   cd tui && go test ./...
   cd /home/debian/claude-code-ansible && shellcheck install.sh scripts/new-vm.sh
   ```
</details>
