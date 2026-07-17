---
id: 5
group: "validation"
dependencies: [2, 3, 4]
status: "completed"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 6
complexity_notes: "Verification/quality gate for a concurrency-sensitive change; risk floor applies."
skills:
  - go
  - testing
---
# End-to-end validation, call-site audit, and docs

## Objective

Prove the concurrency fix end-to-end (the headline two-process race), audit for
any remaining code path that writes a store snapshot captured before a
long-running operation, and land the documentation updates the plan requires.

## Skills Required

- `go` — reading call sites, writing an integration test.
- `testing` — end-to-end / cross-process style test harness.

## Acceptance Criteria

- [ ] **Headline race test (Self Validation step 1).** An integration test (Go
      test harness driving the registry + secrets code paths, or two real `sand`
      processes against a scratch `XDG_DATA_HOME`/`XDG_CONFIG_HOME`) reproduces:
      process P1 holds the registry in memory; process P2 records a new VM;
      P1 then runs its reconcile-save. Assert the P2 VM is still present and
      still tagged **managed** in `managed-vms.json`, and that its host secrets
      were NOT pruned from `secrets.json`. This test must FAIL against the
      pre-change behavior and PASS after Tasks 2–4.
- [ ] **Call-site audit (Component 5b).** Verify no remaining code path writes a
      registry/secrets/profiles snapshot captured before a long-running
      operation. Confirm `cmd/sand/create.go` (`RecordSuccess`→`AddScoped` at
      ~281, and the pre-act `manage.Reconcile` at ~218) now goes through the
      locked reload-merge with the corrected pruning basis. Record the audit
      result (files/lines checked) in the PR/commit description.
- [ ] **Docs.** Add a note to the relevant developer/AI-facing docs (e.g.
      `AGENTS.md` if it covers the state stores) that cross-version concurrency
      during an upgrade (an OLD pre-lock binary beside a NEW one) is a known,
      accepted limitation. Confirm the three stores' doc comments and the
      `internal/filelock` package doc (from Tasks 1–4) are present and accurate.
- [ ] **Lock-failure degradation (Self Validation step 3).** A test points a
      store at a path where the lock file cannot be created (or injects an
      Acquire failure via the test seam) and confirms the mutation still persists
      and a note is emitted.
- [ ] **Full suite green:** `go build ./...`, `go vet ./...`, and
      `go test ./...` all pass; capture the output.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Prefer a Go-level integration test over spawning real VMs (a full Lima VM is
  not required — drive the registry/secrets/manage code paths directly to
  simulate the two processes, as the plan's Self Validation allows).
- If `AGENTS.md` does not currently cover the state stores, add the note in the
  most appropriate existing doc rather than inventing a new file.

## Input Dependencies

- Tasks 2, 3, 4: all three converted stores and the corrected
  `ReconcileScoped`/`manage.Reconcile`.
- Task 1: `internal/filelock` (for the lock-failure degradation test seam).

## Output Artifacts

- An integration test covering the headline race + lock-failure degradation, the
  documentation updates, and a recorded call-site audit.

### Call-site audit (Component 5b) — result

Every `.Load(`/`.LoadFrom(` of the three stores was traced forward to every
subsequent mutation reachable from it. Conclusion: **every mutation now goes
through the store's own `mutateLocked` (lock + non-saving reload + delta-merge
+ atomic write); no remaining code path writes a pre-operation snapshot.**

Process-start loads (each a one-time, long-lived in-memory handle — never
reloaded on a timer):
- `cmd/sand/create.go:201` — `registry.Load()` (headless create's `reg`).
- `cmd/sand/create.go:` (profiles) — `resolve.go:17` — `profiles.Load()`.
- `internal/ui/model.go:339,347,361` — `registry.Load()` (`m.reg`),
  `secrets.Load()` (`m.sec`), `profiles.Load()` (`m.profileStore`), all in
  `model.New()`, all held for the life of the TUI process.
- `cmd/sand/shell.go:104` / `cmd/sand/main.go:68` — same pattern for their own
  narrow needs (unaffected by this plan; no mutation follows).

Mutations traced from each held handle:
- `cmd/sand/create.go:218` — `manage.Reconcile(reg, live, scope)` →
  `reg.ReconcileScoped(scope, present, known)`, where `known` is
  `reg.NamesInScope(scope)` captured from `reg`'s in-memory view **before**
  `ReconcileScoped` reloads under the lock (`internal/manage/manage.go:35-36`)
  — the corrected `known ∩ absent` pruning basis (Task 2), not "everything in
  my map not in `live`".
- `cmd/sand/create.go:281` (`doHeadlessCreate` → `manage.RecordSuccess`) →
  `reg.AddScoped(cfg, scope)` → `registry.mutateLocked`: takes the lock,
  re-reads the CURRENT on-disk index, merges in exactly this one
  `(scope, name)` entry, writes. This is the widest window in the codebase
  (a multi-minute provision runs between the `Load()` at :201 and this call at
  :281) and it is now provably not a blind overwrite of the :201 snapshot —
  confirmed both by reading `registry.AddScoped`/`mutateLocked` and by the
  headline integration test below, which reproduces exactly this shape of
  race and passes.
- `cmd/sand/create.go:235` — `store.SetLastUsed(profile.ID)` →
  `profiles.mutateLocked`.
- `internal/ui/model.go:959` — `manage.Reconcile(m.reg, live, sc)` → same
  corrected pruning basis as above, on the TUI's 5s refresh tick.
- `internal/ui/model.go:970-972` — `m.sec.Remove(name, sc)` for each dropped
  name (the secrets cascade the Work Order calls out) →
  `secrets.mutate` → `secrets.mutateLocked`: reloads the on-disk tree fresh
  and deletes only that one `(connScope, vm)` entry — a concurrently-added
  same-scope secret for an unrelated VM is untouched, and (per the headline
  test) a VM added by another process after `m.reg`'s snapshot is never in
  `dropped` in the first place, so its secret is never even considered for
  removal.
- `internal/ui/model.go:1030` — `m.reg.RemoveScoped(sc, msg.name)` →
  `registry.mutateLocked`.
- `internal/ui/model.go:1033` — `m.sec.Remove(msg.name, sc)` →
  `secrets.mutateLocked`.
- `internal/ui/model.go:1101` — `manage.RecordSuccess(m.reg, cfg, msg.job.scope)`
  → `reg.AddScoped` → `registry.mutateLocked` (same multi-minute-window
  closure as the headless path, for the TUI's own build path).
- `internal/ui/model.go:1111` — `m.profileStore.SetLastUsed(...)` →
  `profiles.mutateLocked`.
- `internal/ui/model.go:1129` — `m.sec.Set(cfg.Name, msg.job.scope, pairs)` →
  `secrets.mutateLocked`.
- `internal/ui/secrets.go:214` — `m.sec.SetAll(...)` → `secrets.mutateLocked`.
- `internal/ui/profilesview.go:240,272,316,377,510` —
  `m.profileStore.Enable/Disable/Remove/Update/Add(...)` → each →
  `profiles.mutateLocked`.

The only writes that are NOT lock-protected are the two intentionally-unlocked
process-start side effects the plan calls out by design: `registry.LoadFrom`'s
migration persist (`registry.go:251`, `_ = r.save()`) and `profiles.LoadFrom`'s
seed-on-missing/corrupt persist (`store.go`'s `seedLocal()` calls). Both run
before any concurrent mutation from that process and are idempotent, matching
the plan's Clarifications table entry on this exact question.

**Conclusion: no remaining code path writes a registry/secrets/profiles
snapshot captured before a long-running operation.** Verified by static trace
(above) and empirically by `TestHeadlineTwoProcessRace_ConcurrentCreateSurvivesStaleReconcile`
(`internal/manage/e2e_concurrency_test.go`), which reproduces the exact
`create.go:201→281` window and passes.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

- Headline test: construct two `*registry.Registry` (and matching
  `*secrets.Store`) values pointing at the same scratch files. Instance P1 loads
  and holds. P2 does `AddScoped(newVM)` + records secrets. Then invoke P1's
  reconcile with a `present`/`known` pair that reflects P1's *old* live list
  (which predates newVM). Assert newVM survives in the on-disk file and is
  managed, and that `secrets.json` still has newVM's entries. This directly
  exercises the `known ∩ absent` pruning basis from Task 2 and the secrets
  cascade at `model.go:970-972`.

- Audit: grep for `.Load(` of the three stores and trace each to its subsequent
  mutation; confirm every mutation now reloads under the lock (Tasks 2–4).
  Specifically confirm `cmd/sand/create.go` and `internal/ui/model.go` no longer
  write a pre-operation snapshot.

- Test philosophy — write a few tests, mostly integration: this task is the
  integration/critical-path coverage. Do not duplicate the per-store unit tests
  already added in Tasks 2–4.

- This task is a verification/quality gate: do not mark it complete unless the
  headline test genuinely fails on the old behavior and passes on the new, and
  the full `go test ./...` suite is green.
</details>
