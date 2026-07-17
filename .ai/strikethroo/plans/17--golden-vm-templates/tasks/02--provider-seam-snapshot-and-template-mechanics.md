---
id: 2
group: "provisioning"
dependencies: [1]
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 8
complexity_notes: "Power-state-preserving stop/clone/restore with failure cleanup under a host lock (concurrency + destructive); ripples through the Provider interface, both impls, and the fake."
skills:
  - go
  - concurrency
---
# Provider Seam: Snapshot and Template Mechanics

## Objective
Implement the host-side mechanics for creating and deleting a template, and expose them through the `Provider` interface so they run correctly on both local and remote-Lima scopes. Snapshotting must be power-state preserving (a running source ends running; an already-stopped source stays stopped) and must never leave a formerly-running source stopped or a half-built template behind.

## Skills Required
- **go**: interface extension across multiple implementations, error wrapping, `defer`-based cleanup.
- **concurrency**: correct use of the existing host-side base/template lock so parallel snapshots/creates on one host cannot interleave.

## Acceptance Criteria
- [ ] `provision.Provisioner` gains `SnapshotTemplate(ctx, sourceVMName, templateInstanceName string, out io.Writer) (SnapshotResult, error)` that: records the source power state, stops the source only if running, clones it into the template instance under the base/template lock, restores the source to its recorded power state (always, including on the error path), stamps the template instance's playbook version, and cleans up the partial template instance on clone failure.
- [ ] `SnapshotResult` carries the captured `PlaybookVersion` and toolset so the caller can build the registry record.
- [ ] `provision.Provisioner` gains `DeleteTemplate(ctx, templateInstanceName string, out io.Writer) error` (locked `limactl delete --force`) and a `TemplateDiskBytes(templateInstanceName string) int64` helper (via `HostFiles.DiskAllocBytes` on the instance's `disk` file).
- [ ] The `Provider` interface (`internal/provider/provider.go`) gains `SnapshotTemplate`, `DeleteTemplate`, and `TemplateDiskBytes`; `local.go` and `remote.go` delegate to their `Provisioner`; `providerfake.Provider` gains matching `...Func` fields with safe zero-value defaults.
- [ ] A unit test using a fake `lima.Runner` proves the power-state matrix: running source → stop, clone, start (source running at end); stopped source → no stop, clone, no start (source stopped at end); and clone-failure → source restored to prior state AND template instance cleanup (`delete`) invoked, with no stamp written.
- [ ] `go build ./... && go vet ./... && go test ./internal/provision/... ./internal/provider/... -race` passes; `gofmt -l` is empty for touched files.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Reuse the existing lock discipline: `lockBase(ctx, hf lima.HostFiles, baseName string, out)` in `internal/provision/baselock.go` returns a `release func()`; call it with the template instance name so template mutation serializes against base/template operations on the same host.
- Reuse `p.Lima.Status(name)` to read power state, `p.Lima.StopStreaming`/`StartStreaming` (or `Stop`/`Start`) for transitions, `p.Lima.CloneStreaming(ctx, source, templateInstance, out)` for the copy, `p.Lima.Delete(name, true)` for cleanup, and the `baseversion.go` stamp writer (`writeBaseVersion(hf, templateInstance, version, builtAt)`) — the template instance is stamped exactly like the base is.
- Do all host file access through `p.HostFiles`/the Lima client so remote-over-SSH works unchanged; never touch the laptop filesystem directly.
- The `Provider` interface is implemented by `limaProvider` (`local.go`), `remoteLimaProvider` (`remote.go`), and faked by `providerfake.Provider` — all four must be updated so the package compiles (`var _ Provider = ...` assertions already exist).

## Input Dependencies
- Task 1: `vm.TemplateInstanceName` and the registry template model (the caller wiring in later tasks depends on it; this task consumes the instance-name helper).

## Output Artifacts
- `Provisioner.SnapshotTemplate` / `DeleteTemplate` / `TemplateDiskBytes` and the `SnapshotResult` type.
- Extended `Provider` interface + local/remote implementations + `providerfake` fields consumed by tasks 3–6.

## Implementation Notes
The test philosophy applies: the power-state matrix and failure-cleanup are exactly the custom, critical-path logic that warrants tests. Do not add tests for the trivial delegations in `local.go`/`remote.go`.

<details>
<summary>Detailed implementation guidance</summary>

1. **`internal/provision/template.go` (new):**
   ```go
   type SnapshotResult struct { PlaybookVersion string; Toolset map[string]bool }

   func (p *Provisioner) SnapshotTemplate(ctx context.Context, source, templateInstance string, out io.Writer) (SnapshotResult, error) {
       hf := p.hostFiles()
       release, err := lockBase(ctx, hf, templateInstance, out)
       if err == nil { defer release() }
       // record prior state
       prior, _ := p.Lima.Status(source) // "Running" etc.
       wasRunning := prior == "Running"
       if wasRunning {
           if err := p.Lima.StopStreaming(ctx, source, out); err != nil { return SnapshotResult{}, fmt.Errorf(...) }
       }
       // ensure source is restored no matter what
       defer func() {
           if wasRunning { _ = p.Lima.StartStreaming(ctx, source, out) }
       }()
       if err := p.Lima.CloneStreaming(ctx, source, templateInstance, out); err != nil {
           _ = p.Lima.Delete(templateInstance, true) // cleanup partial
           return SnapshotResult{}, fmt.Errorf("clone template: %w", err)
       }
       // stamp version/toolset from the source's base stamp or current playbook
       ver := readBaseVersion(hf, source) // fall back to PlaybookVersion(...) if empty
       toolset, _ := BaseToolset(hf, source)
       if err := writeBaseVersion(hf, templateInstance, ver, timeNow()); err != nil { /* wrap */ }
       return SnapshotResult{PlaybookVersion: ver, Toolset: toolset}, nil
   }
   ```
   - Use the same time source the codebase uses for stamping (see `baseversion.go` `writeBaseVersion` callers — it takes a `builtAt time.Time`; pass the source's built-at if available else the current time via the existing seam).
   - If `lockBase` fails it is non-fatal in the base path today — mirror that (log via `out`, proceed) rather than aborting.

2. **`DeleteTemplate` and `TemplateDiskBytes`:** in the same file.
   ```go
   func (p *Provisioner) DeleteTemplate(ctx context.Context, templateInstance string, out io.Writer) error {
       release, err := lockBase(ctx, p.hostFiles(), templateInstance, out); if err == nil { defer release() }
       return p.Lima.Delete(templateInstance, true)
   }
   func (p *Provisioner) TemplateDiskBytes(templateInstance string) int64 {
       hf := p.hostFiles()
       dir := filepath.Join(hf.LimaHome(), templateInstance)
       return hf.DiskAllocBytes(filepath.Join(dir, "disk"))
   }
   ```
   Mirror `internal/ui/diskusage.go` for the `disk` join.

3. **Provider interface (`internal/provider/provider.go`):** add the three methods. In `local.go` (`limaProvider`) and `remote.go` (`remoteLimaProvider`, which embeds the same core) delegate to `p.prov.SnapshotTemplate(...)` etc. Both share one `*provision.Provisioner`, so the remote impl needs no extra work beyond the delegation compiling.

4. **`internal/providerfake/fake.go`:** add `SnapshotTemplateFunc`, `DeleteTemplateFunc`, `TemplateDiskBytesFunc` fields and methods that call them if non-nil, else return zero values (`SnapshotResult{}, nil` / `nil` / `0`). Keep the `var _ provider.Provider` assertion.

5. **Test (`internal/provision/template_test.go`):** build a `lima.Client` over a recording fake `lima.Runner` (see how `internal/ui` and existing provision tests fake the runner — record the argv of each `limactl` call). Drive `SnapshotTemplate` three times:
   - Runner reports source `Running`: assert the recorded calls contain `stop`, then `clone`, then `start`.
   - Runner reports source `Stopped`: assert no `stop`/`start`, but `clone` present.
   - Runner errors on `clone`: assert `delete <templateInstance>` recorded (cleanup), `start` recorded iff source was running, and no version stamp write.
   If stamping goes through the stubbable `writeBaseVersionFn` var, override it in the test to capture writes.

6. Run `go build ./...`, `go vet ./...`, `go test ./internal/provision/... ./internal/provider/... -race`, `gofmt -l`. Paste passing output.
</details>
