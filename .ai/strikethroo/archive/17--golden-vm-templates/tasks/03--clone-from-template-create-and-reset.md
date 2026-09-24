---
id: 3
group: "provisioning"
dependencies: [1, 2]
status: "completed"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Branches the core create/reset path to clone from a template and skip all base-build/staleness machinery; correctness of the create flow is load-bearing."
skills:
  - go
---
# Clone-from-Template Create and Reset

## Objective
Make "create from a template" a variant of the existing create flow: when a template source is supplied, skip the base-ensure/staleness/convergence stage entirely and clone from the template instance, then run the normal per-VM sizing, start, and finalize phases. Ensure that resetting a template-sourced VM re-clones from the template (not `sandbar-base`), and that a missing template produces a clear error on both create and reset.

## Skills Required
- **go**: modifying an existing control-flow path with minimal disruption; error handling.

## Acceptance Criteria
- [ ] `provision.CreateOptions` gains a `TemplateSource string` field (the template's Lima instance name). When set, `createVM` skips `ensureBaseStopped`/base build/`reapplyBase` and clones from `TemplateSource`; when empty, behavior is byte-for-byte unchanged.
- [ ] The finalize phase (hostname, git identity, optional repo clone) still runs on a template-sourced create; per-VM disk sizing still grows from the template's size (never shrinks).
- [ ] `Reset` of a VM whose stored `CreateConfig`/provenance indicates a template source re-clones from the template instance and skips base machinery; a normal VM's reset is unchanged.
- [ ] Creating or resetting from a template instance that does not exist on the host fails fast with an error naming the missing template (no silent fallback to base).
- [ ] A unit test with a fake `lima.Runner` proves a template-sourced create records a `clone <templateInstance> <name>` and records **no** base create/provision calls; and that reset of a template-sourced config clones from the template instance.
- [ ] `go build ./... && go vet ./... && go test ./internal/provision/... -race` passes; `gofmt -l` empty for touched files.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- The create flow today is `CreateVMWithOptions → createVM → prepareBaseAndClone (lockBase → migrateLegacyBase → ensureBaseStopped → CloneStreaming) → Lima.Configure → StartStreaming → runProvision(finalize)`. Introduce the template branch at the `prepareBaseAndClone` level: when `opts.TemplateSource != ""`, still take the lock (on the template instance) and `CloneStreaming(ctx, opts.TemplateSource, cfg.Name, out)`, but do not build/convergence-check a base.
- Detect a template-sourced reset from the stored config: task 1 sets a template-sourced VM's `CreateConfig.BaseName` to the template instance name and records `TemplateSource` provenance. Reset should pass that instance as the clone source and set `TemplateSource` in the options it builds internally so the same skip-base branch is taken.
- Guard existence with `p.Lima.Status(templateInstance)` (or `Get`) before cloning; on `ErrNoSuchInstance` return a wrapped, user-facing "template \"X\" not found" error.
- Do not alter the non-template code path's function order or side effects.

## Input Dependencies
- Task 1: registry provenance + instance-name helper.
- Task 2: shares `internal/provision` (must land first to avoid file conflicts) and the template instance conventions.

## Output Artifacts
- `CreateOptions.TemplateSource` and the skip-base clone branch in `createVM`/`prepareBaseAndClone`, plus template-aware `Reset`, consumed by the CLI (task 4) and TUI (task 5).

## Implementation Notes
Test the branch that skips base building and the reset-from-template path — these are the custom, critical-path behaviors. Do not re-test the unchanged base path beyond one regression assertion.

<details>
<summary>Detailed implementation guidance</summary>

1. **`internal/provision/provision.go`:**
   - Add `TemplateSource string` to `type CreateOptions struct`.
   - In `prepareBaseAndClone` (or the smallest enclosing function), branch at the top:
     ```go
     if opts.TemplateSource != "" {
         if _, err := p.Lima.Status(opts.TemplateSource); err != nil {
             if errors.Is(err, lima.ErrNoSuchInstance) {
                 return fmt.Errorf("template %q not found", opts.TemplateSource)
             }
             return err
         }
         release, lerr := lockBase(ctx, p.hostFiles(), opts.TemplateSource, out)
         if lerr == nil { defer release() }
         return p.Lima.CloneStreaming(ctx, opts.TemplateSource, cfg.Name, out)
     }
     // ... existing base-ensure + clone path unchanged ...
     ```
   - Ensure the caller of `prepareBaseAndClone` (in `createVM`) continues to `Configure`, `StartStreaming`, and `runProvision(finalize)` after it returns — those are outside the branch and already run for both paths.

2. **Reset (`Reset`/`RecreateWithOptions`):** where reset determines the clone source from `cfg.BaseName`, detect the template case. Simplest: if the stored config's `TemplateSource` (provenance) is non-empty, build the internal `CreateOptions{TemplateSource: cfg.BaseName}` (recall task 1 stores the template instance name in `BaseName`) so the same skip-base branch runs. Confirm staging.go behavior (Claude/project carry-over) is unaffected — it operates on guest data, orthogonal to clone source.

3. **Missing-template error:** verify `lima.ErrNoSuchInstance` is what `Status`/`Get` return for an absent instance (see `internal/lima/client.go`); wrap it with the user-facing name.

4. **Test (`internal/provision/template_create_test.go`):** using the recording fake `lima.Runner`:
   - `CreateVMWithOptions(ctx, cfg, CreateOptions{TemplateSource: "sandbar-tmpl-golden"}, out)` with the runner reporting the template instance exists and the new VM absent → assert recorded argv includes `clone sandbar-tmpl-golden <name>` and includes finalize provisioning, and does **not** include a base `create`/base-provision.
   - Reset of a config with template provenance → assert clone source is the template instance.
   - Create with `TemplateSource` pointing at a non-existent instance → assert the returned error contains the template name.

5. Run `go build ./...`, `go vet ./...`, `go test ./internal/provision/... -race`, `gofmt -l`. Paste passing output.
</details>
