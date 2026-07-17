---
id: 6
group: "testing"
dependencies: [1, 2, 3, 4]
status: "completed"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 6
complexity_notes: "Real-VM e2e over Lima (and remote SSH); correctness of a multi-step round-trip is the whole point, so it carries the verification-gate risk floor."
skills:
  - go
  - integration-testing
---
# E2E: Snapshot → Clone-from → Delete Round-Trip

## Objective
Prove the whole feature end-to-end against real Lima instances (local, and the remote-over-SSH profile) with a `//go:build limae2e` test: snapshot a VM into a template, create a new VM from that template, verify the seeded guest state carried over with a fresh per-VM identity, then delete the template and confirm cleanup — with all template disk data living only on the owning host.

## Skills Required
- **go**: build-tagged tests, driving the CLI/provider, guest-shell assertions.
- **integration-testing**: real-VM lifecycle, deterministic setup/teardown, remote parity.

## Acceptance Criteria
- [ ] A new `//go:build limae2e` test exercises: create source VM → write a marker (a file and a tiny SQLite/`sqlite3` DB or equivalent) in the guest → `snapshot` into a template → assert the source VM is back to its prior power state and a stopped template instance exists → create a second VM from the template → assert the marker is present in the new guest and hostname/git identity are the new VM's → `delete` the template → assert the template instance and disk are gone.
- [ ] The remote path is covered by extending `internal/provider/remote_e2e_test.go` (or a sibling `//go:build limae2e` file) so the round-trip runs against the remote profile, asserting the template disk existed only on the remote host.
- [ ] The test is skipped cleanly (not failed) when its prerequisites (limactl / remote target) are unavailable, consistent with the existing e2e harness.
- [ ] `go test -tags limae2e ./internal/provision/... ./internal/provider/... -run <NewTest>` passes locally where Lima is available (paste the run, or the documented skip if the runner lacks Lima). The non-e2e suite `go test ./... -race` remains green.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Follow the existing e2e conventions: first line `//go:build limae2e`; reuse the shared harness in `internal/provision/main_test.go` and the remote helpers in `remote_e2e_test.go` (`TestE2ERemoteLima`, `sshTarget(cfg)`).
- Drive the feature through the same seams the CLI uses (provider methods + registry) or invoke the built `sand` binary; assert guest state via `Provider.ShellOut`/`Shell` (or `p.Lima.Shell`).
- Keep the scenario to a single round-trip per scope (creates are expensive) as the plan's Remote-Parity risk mitigation requires; rely on tasks 1–3 unit tests for breadth.
- Ensure robust teardown (delete both VMs and the template) even on failure, so a partial run does not strand instances.

## Input Dependencies
- Tasks 1–3 (model + mechanics + create-from) and Task 4 (the CLI surface the e2e can drive headlessly).

## Output Artifacts
- `//go:build limae2e` round-trip test(s) covering local and remote scopes.

## Implementation Notes
This is a verification task — apply the evidence gate to your own run. Do not claim it passes without the command output (or an explicit, justified skip). Keep it to the critical round-trip; no exhaustive matrix.

<details>
<summary>Detailed implementation guidance</summary>

1. **Local test (`internal/provision/template_e2e_test.go`, `//go:build limae2e`):**
   - Reuse the harness to create a source VM (small sizing to keep it fast).
   - Seed a marker: `p.Lima.Shell(name, "echo golden > ~/marker.txt && sqlite3 ~/seed.db 'create table t(x); insert into t values(1);'")` (or skip sqlite if not present in the base and just use a file + a directory).
   - `SnapshotTemplate(ctx, name, vm.TemplateInstanceName("golden-e2e"), out)`; assert `p.Lima.Status(name)` returns the prior state (running), and `Status(templateInstance)` is `Stopped`.
   - Create VM2 with `CreateOptions{TemplateSource: templateInstance}` (or `sand create --template golden-e2e`); assert `cat ~/marker.txt` == `golden` in VM2 and the hostname != source hostname.
   - `DeleteTemplate(ctx, templateInstance, out)`; assert `Status(templateInstance)` → `ErrNoSuchInstance` and the instance dir is gone.
   - `t.Cleanup` deletes VM1, VM2, and the template unconditionally.

2. **Remote test:** extend `remote_e2e_test.go` — obtain the remote `TargetConfig`/provider via the existing helper, run the same round-trip through the remote provider, and assert (via the remote host's `HostFiles`/`limactl list`) that the template instance exists on the remote and never locally.

3. **Skip semantics:** mirror how existing e2e tests detect a missing limactl/remote target and call `t.Skip(...)` rather than failing.

4. Run `go test -tags limae2e ./internal/provision/... -run TemplateRoundTrip -v` (and the remote one where the target is configured). Also run `go test ./... -race` (no tag) to confirm nothing regressed. Paste both outputs; if Lima is unavailable on this machine, paste the skip lines and say so explicitly.
</details>
