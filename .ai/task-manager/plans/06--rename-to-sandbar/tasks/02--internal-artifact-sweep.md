---
id: 2
group: "internal-sweep"
dependencies: [1]
status: "pending"
created: "2026-07-03"
skills:
  - go
  - bash
---
# Internal-artifact sweep (`claude-vm-*` → `sand-*`), coupled pairs on both sides

## Objective
Honor the full-sweep scope so no internal `claude-vm-*` artifact identifier survives — **without breaking the coupled host↔guest↔CI references**. Each coupled pair (the `/dev/shm` vars file and the `/var/log` logs) is renamed on **both** endpoints in this single task so producer and consumer keep agreeing.

## Skills Required
- `go` — rename artifact path/prefix strings in provision/staging/baseversion code + a test glob.
- `bash` — rename temp-dir prefix, `/dev/shm` vars path, and `/var/log` log paths in `scripts/new-vm.sh`, plus the CI workflow.

## Acceptance Criteria
- [ ] **Coupled pair 1 (/dev/shm vars):** `scripts/new-vm.sh` and `tui/internal/provision/provision.go` both use `/dev/shm/sand-vars.yml`.
- [ ] **Coupled pair 2 (/var/log logs):** `scripts/new-vm.sh` and `.github/workflows/test.yml` both use `/var/log/sand-provision.log` and `/var/log/sand-finalize.log`.
- [ ] Temp prefixes renamed: `claude-vm.XXXXXX`→`sand.XXXXXX` (new-vm.sh), `claude-vm-base-*`→`sand-base-*` (provision.go), `claude-vm-reset-*`→`sand-reset-*` (staging.go **and** the matching glob in provision_test.go).
- [ ] Persistent host dir renamed: `~/.lima/_claude-vm/` → `~/.lima/_sand/` in `tui/internal/provision/baseversion.go`.
- [ ] `grep -rn 'claude-vm' scripts/new-vm.sh .github/workflows/test.yml tui/internal/provision` returns **0**.
- [ ] `cd tui && go build ./... && go test ./...` passes.

## Technical Requirements
The two coupled pairs are runtime paths shared across files: `/dev/shm/...-vars.yml` (written by `new-vm.sh`, read by `provision.go`) and `/var/log/...-{provision,finalize}.log` (written by `new-vm.sh`, tailed by CI). A one-sided rename silently breaks provisioning or log capture, so both endpoints move in this task.

## Input Dependencies
- Task 1 (module path + `cmd/sand` + app-name strings). Runs after so both tasks never edit the same file concurrently.

## Output Artifacts
- All internal artifacts renamed to `sand-*`; coupled endpoints consistent for the external sweep and verification tasks to build on.

## Implementation Notes

<details>
<summary>Step-by-step</summary>

1. **Coupled pair 1 — `/dev/shm` vars file** (rename on BOTH sides):
   - `scripts/new-vm.sh` (~line 419): `vars=/dev/shm/claude-vm-vars.yml` → `vars=/dev/shm/sand-vars.yml`.
   - `tui/internal/provision/provision.go` (~line 27, inside the provisioning script string): `vars=/dev/shm/claude-vm-vars.yml` → `vars=/dev/shm/sand-vars.yml`.

2. **Coupled pair 2 — `/var/log` logs** (rename on BOTH sides):
   - `scripts/new-vm.sh` (~lines 447, 462): `/var/log/claude-vm-provision.log` → `/var/log/sand-provision.log`; `/var/log/claude-vm-finalize.log` → `/var/log/sand-finalize.log`.
   - `.github/workflows/test.yml` (~lines 118, 119): same two paths in the "Dump provisioning logs on failure" step.

3. **Temp-dir prefixes** (uncoupled, rename in place):
   - `scripts/new-vm.sh` (~line 337): `mktemp -d "${TMPDIR:-/tmp}/claude-vm.XXXXXX"` → `sand.XXXXXX`.
   - `tui/internal/provision/provision.go` (~line 74): `os.CreateTemp("", "claude-vm-base-*.yaml")` → `"sand-base-*.yaml"`.
   - `tui/internal/provision/staging.go` (~line 84): `os.MkdirTemp("", "claude-vm-reset-*")` → `"sand-reset-*"`.
   - `tui/internal/provision/provision_test.go` (~lines 527, 532): the comment and `filepath.Glob(... "claude-vm-reset-*")` → `"sand-reset-*"` (must match staging.go's new prefix, or the leaked-dir test breaks).

4. **Persistent host dir** — `tui/internal/provision/baseversion.go` (~line 68): `filepath.Join(home, "_claude-vm", ...)` → `"_sand"`.
   - **Migration note (not a bug):** existing base-image stamps live under `~/.lima/_claude-vm/`. After the rename the TUI finds no stamp under `_sand`, treats each pre-existing base image as stale, and **rebuilds it once**. This self-heals with no data loss; no migration code is required. Call it out so the one-time rebuild isn't mistaken for a regression.

5. **Catch stragglers** — after the edits:
   ```bash
   cd /home/debian/claude-code-ansible
   grep -rn 'claude-vm' scripts/new-vm.sh .github/workflows/test.yml tui/internal/provision
   ```
   Fix any remaining `claude-vm` comment/string in those paths (all should become `sand`/`sand-*`). Then:
   ```bash
   cd tui && go build ./... && go test ./...
   ```

6. **Do NOT** touch repo URLs (`deviantintegral`/`claude-code-ansible`) in `new-vm.sh` or the data-dir `CACHE_DIR` — those are tasks 3 and 4.
</details>
