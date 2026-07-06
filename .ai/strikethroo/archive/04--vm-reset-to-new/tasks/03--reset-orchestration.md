---
id: 3
group: "reset-flow"
dependencies: [1, 2]
status: "completed"
created: "2026-06-27"
skills:
  - go
complexity_score: 5
complexity_notes: "Ordering-sensitive orchestration (stage→delete→size→restore-before-finalize→finalize-skip-clone→restore-after-finalize→bounce→cleanup) reusing primitives from tasks 1 and 2; risk is in the restore ordering and the finalize clone-skip, both pinned in notes."
---
# Reset orchestration (stage out → recreate sized → restore → finalize → restore → bounce → clean)

## Objective
Add a `Reset` path to the `provision` package that recreates a managed VM from a (possibly edited) config and, when requested, preserves the Claude login and/or the per-org `.env` + checkout across the destroy/recreate by staging them on the host and restoring them in the correct order relative to the finalize playbook.

## Skills Required
- `go` (provisioner orchestration, reuse of lima primitives, integration tests with the fake runner)

## Acceptance Criteria
- [ ] A `ResetOptions{ PreserveClaude, PreserveProject bool }` type exists in the `provision` package.
- [ ] `Reset(ctx, cfg, opts, out)` runs this exact sequence: (if any preserve) ensure VM running → resolve home → stage out selected archives; then delete → ensure base stopped → clone → configure (size) → start → (if PreserveClaude) restore Claude **before** finalize → finalize (skipping the project git-clone when PreserveProject) → (if PreserveProject) restore project dir + chown + `direnv allow` → stop → start.
- [ ] On success the host staging dir is removed; on failure **after** stage-out it is kept and its path is included in the returned error.
- [ ] The finalize phase omits `project_clone_url` when `PreserveProject` is true (so the role does not re-clone over the restored tree).
- [ ] An integration test with the fake runner asserts the ordered limactl calls for (a) reset with both preserves on and (b) reset with no preserve; `cd tui && go test ./...` passes.

## Technical Requirements
- Builds on task 1 (`Configure`) and task 2 (staging helpers + path resolution).
- The Claude restore must precede finalize so the finalize playbook re-applies `~/.claude/settings.json` from its template on top of the restored credentials/history. The project restore must follow finalize so the (skipped) clone step doesn't clobber it.

## Input Dependencies
- Task 1: `lima.Client.Configure`, `vm.BaseDiskFloor`.
- Task 2: `cloneOrgRelDir`, `guestHome`, `StageOut`, `StageIn`, stage-dir helpers.

## Output Artifacts
- `provision.ResetOptions` and `provision.Reset` — dispatched by the TUI (task 4).

## Implementation Notes
<details>
<summary>Detailed implementation guidance</summary>

Work in `tui/internal/provision/provision.go` (and `provision_test.go`).

**1. Extract a shared base-ensure helper (small refactor):**
- Pull the "ensure the base exists and is stopped" block out of `createVM` into:
  ```go
  func (p *Provisioner) ensureBaseStopped(ctx context.Context, cfg vm.CreateConfig, out io.Writer) error
  ```
  Have `createVM` call it. This keeps `Reset` from duplicating the base-build/stop logic. Do not change `createVM`'s observable call order.

**2. Add the options type and Reset:**
```go
type ResetOptions struct {
    PreserveClaude  bool
    PreserveProject bool
}

func (p *Provisioner) Reset(ctx context.Context, cfg vm.CreateConfig, opts ResetOptions, out io.Writer) error
```

Sequence inside `Reset`:
1. **Stage out (only if `opts.PreserveClaude || opts.PreserveProject`):**
   - Ensure the source VM is running: `status,_ := p.Lima.Status(cfg.Name)`; if `status != "Running"` call `p.Lima.Start(cfg.Name)`.
   - `home, err := guestHome(ctx, p.Lima, cfg.Name, cfg.User)`.
   - `stageDir, err := newStageDir()`.
   - If `PreserveClaude`: `StageOut(ctx, p.Lima, cfg.Name, home, []string{".claude", ".claude.json"}, filepath.Join(stageDir, "claude.tgz"))`.
   - If `PreserveProject`: resolve `orgRel, ok := cloneOrgRelDir(cfg.CloneURL)`; only stage if `ok`. `StageOut(..., []string{orgRel}, filepath.Join(stageDir,"project.tgz"))`. Remember `orgRel` + `ok` for restore.
   - From here on, any error path must NOT delete `stageDir`; wrap returned errors with the staging path, e.g. `fmt.Errorf("reset failed after staging; your data is preserved at %s: %w", stageDir, err)`.
2. **Delete:** `p.Lima.Delete(cfg.Name, true)`.
3. **Recreate sized:** `ensureBaseStopped` → `p.Lima.Clone(cfg.BaseName, cfg.Name)` → `p.Lima.Configure(cfg.Name, cfg.CPUs, cfg.Memory, cfg.Disk)` → `p.Lima.Start(cfg.Name)`.
4. **Restore Claude (before finalize):** if `PreserveClaude`, `StageIn(ctx, p.Lima, cfg.Name, home, cfg.User, []string{".claude", ".claude.json"}, filepath.Join(stageDir,"claude.tgz"))`. (Re-resolve `home` for the new VM if you prefer; it is the same user so the path matches — resolving once before delete is fine since the username is unchanged.)
5. **Finalize:** build a finalize config:
   ```go
   finCfg := cfg
   if opts.PreserveProject { finCfg.CloneURL = "" } // skip the role's git clone over the restored tree
   p.runProvision(ctx, cfg.Name, "finalize", cfg.EffectiveHostname(), finCfg, out)
   ```
   Clearing `CloneURL` makes `BuildExtraVars` omit `project_clone_url`, so the `project` role's block (gated on `project_clone_url | length > 0`) is skipped.
6. **Restore project (after finalize):** if `PreserveProject && ok`:
   - `StageIn(..., []string{orgRel}, project.tgz)`.
   - `direnv allow` for the org dir, run as the user: `p.Lima.Shell(ctx, cfg.Name, nil, out, "sudo", "-iu", cfg.User, "direnv", "allow", home+"/"+orgRel)`. (StageIn already chowns; this re-approves the restored `.env`.)
7. **Bounce:** `p.Lima.Stop(cfg.Name)` → `p.Lima.Start(cfg.Name)` (mirror `createVM`'s tail).
8. **Cleanup:** on full success, `removeStageDir(stageDir)`.

**3. Tests — `provision_test.go` (use the existing fakeRunner, extended in task 2 to emit canned stream output for `getent`):**
- **Reset, no preserve:** assert argv order: `delete claude -f`, `list claude-base --format {{.Status}}`, `clone claude-base claude`, `edit --set ... claude`, `start claude`, `shell claude sudo bash -c <inGuestScript>` (finalize), `stop claude`, `start claude`. Also assert the finalize stdin (captured in `f.streams`) **contains** `project_clone_url` only if `cfg.CloneURL` was set (no-preserve keeps the clone).
- **Reset, both preserves on (cfg.CloneURL set):** assert the leading stage-out calls (`getent`, two `tar -czf -` shells), then delete/clone/configure/start, then the Claude `tar -xzf -` + chown **before** the finalize shell, then the finalize shell whose captured stdin does **NOT** contain `project_clone_url`, then the project `tar -xzf -` + chown + `direnv allow`, then stop/start. Assert the stage dir is removed at the end (e.g. expose stageDir via a test seam or check no error). Keep assertions focused on ordering and the clone-skip, not on exhaustive argv equality if it becomes brittle.

**Do not** wire any UI here (task 4) and do not change `Recreate`'s existing behavior except that it benefits from `Configure` via task 1 (leave `Recreate` as-is or have it delegate to `Reset` with zero options only if trivial — otherwise leave untouched to limit blast radius).
</details>
