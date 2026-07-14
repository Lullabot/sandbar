---
id: 7
group: "tier-1-staleness"
dependencies: [4]
status: "pending"
created: 2026-07-13
model: "opus"
effort: "xhigh"
skills:
  - go
  - concurrency
complexity_score: 9
complexity_notes: "Concurrency-critical. Changes the base lifecycle under an exclusive flock shared by concurrent creates, AND fixes a real delete-while-cloning race. A mistake here corrupts other users' in-flight clones or wedges every create."
---
# In-place base re-apply, and move `--rebuild`'s destroy under the base lock

## Objective

Collapse the playbook-development inner loop from a from-scratch rebuild into an idempotent delta converge — and close the race where `--rebuild` deletes the base **outside** the base lock, which can destroy a base another create is mid-clone from.

## Skills Required

- **go** — `internal/provision/provision.go`, `internal/provision/baselock.go`, `cmd/sand/create.go`.
- **concurrency** — the exclusive flock protocol and double-checked locking discipline.

## Acceptance Criteria

- [ ] On staleness, the base is **re-applied in place**: start the existing base, re-run the base-phase playbook against it (same in-guest script, same stdin-fed vars, same rsync of the playbook fileset), re-stamp it, and stop it again. No Debian image re-download, no base deletion.
- [ ] The absent-base case is unchanged: it still builds from scratch.
- [ ] `sand create --rebuild` still performs a full destroy-and-rebuild — but the **destroy now happens inside `ensureBaseStopped`, under the base lock**, not in the CLI layer before the provisioner is called.
- [ ] `cmd/sand/create.go`'s `doHeadlessCreate` no longer calls `ld.Delete(cfg.BaseName, true)` before the provisioner. The rebuild **intent** is passed down instead.
- [ ] **No new locking is introduced.** The re-apply lives inside `ensureBaseStopped`, which its caller `prepareBaseAndClone` already runs while holding the exclusive base lock.
- [ ] **Staleness is read AFTER the lock is acquired**, never cached before it. A create that blocked for minutes behind another's rebuild must re-read the stamp and skip redundant work. (This is the double-checked-locking discipline `ensureBaseStopped` already follows — do not hoist the reads out of it.)
- [ ] The lock is released on context cancellation, so a cancelled build does not wedge every other create.
- [ ] **Stamp only on full success.** A failed or partial re-apply must leave the base unambiguously stale (old stamp retained or cleared) — never silently half-converged with a fresh stamp, which would poison every subsequent clone.
- [ ] Existing guarantees survive: extra-vars travel over **stdin, never argv**; the rsync filter stays in step with `playbook_embed.go`.
- [ ] A test proves two concurrent creates cannot have one delete the base while the other clones from it — **including when one passes `--rebuild`**.
- [ ] `go vet ./...` and `go test ./...` are green.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- `internal/provision/baselock.go`: exclusive `syscall.Flock(LOCK_EX|LOCK_NB)` on `<base-version-stamp>.lock` in a 250 ms poll loop, ctx-cancellable. **Failure to lock is non-fatal** — it logs a note and proceeds unserialized (~:66-89). Preserve that behavior.
- `internal/provision/provision.go` `prepareBaseAndClone` (~:243-259) takes the lock and holds it across **both** `ensureBaseStopped` **and** the clone — deliberately, because the stale-base path can otherwise delete a base another create is cloning from.
- `ensureBaseStopped` (~:264-294) today: `Status()` → if exists && `baseStale` → `Delete(force)` → `exists=false`; then `!exists` → `BuildBase`; else if `status != "Stopped"` → `StopStreaming`.
- **The bug**: `cmd/sand/create.go` `doHeadlessCreate` (~:177-183) calls `ld.Delete(cfg.BaseName, true)` **before** `prov.CreateVM` — i.e. before the flock is ever taken. `baselock.go`'s own doc comment (~:19-20) says the lock exists to stop exactly this. `--rebuild` is currently the hole in that guarantee.
- The in-guest script (`inGuestScript`, provision.go ~:53-65) writes vars to `/dev/shm/sand-vars.yml` via `install -m 600 /dev/null`, `trap … EXIT` removes it, and passes `--extra-vars @"$vars"` with the content fed over **stdin**. Reuse it verbatim for the re-apply.

## Input Dependencies

- Task 4: the content-hash stamp is what staleness is computed from.

## Output Artifacts

- In-place re-apply as the default staleness response.
- `--rebuild` destroy relocated under the base lock.
- The re-apply machinery task 9 reuses for the base self-refresh.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**1. Pass the rebuild intent down — do not delete up front.**

Remove the pre-provisioner delete from `doHeadlessCreate`. Thread a `rebuild bool` into the provisioner (e.g. onto `CreateConfig`, or as an option on `CreateVM`) so `ensureBaseStopped` can act on it **under the lock**.

```go
// cmd/sand/create.go — DELETE this block:
//   if rebuild {
//       if status, err := ld.Status(cfg.BaseName); err == nil && status != "" {
//           if err := ld.Delete(cfg.BaseName, true); err != nil { ... }
//       }
//   }
// The destroy now happens inside ensureBaseStopped, under the base lock.
// Deleting here races a concurrent create that is mid-clone from this base —
// the exact race baselock.go exists to close.
```

**2. Restructure `ensureBaseStopped`.** It already owns build/rebuild/stop, and it already runs under the lock. Give it three outcomes:

```go
func (p *Provisioner) ensureBaseStopped(ctx context.Context, cfg vm.CreateConfig, rebuild bool, out io.Writer) error {
    // Everything below is read AFTER the lock is held by prepareBaseAndClone.
    // A waiter that blocked behind someone else's rebuild MUST re-read the
    // stamp here, or it will redundantly redo work that just completed.
    status, err := p.Lima.Status(cfg.BaseName)
    exists := err == nil && status != ""

    switch {
    case exists && rebuild:
        step(out, "Rebuilding the base image %q from scratch…", cfg.BaseName)
        if err := p.Lima.Delete(cfg.BaseName, true); err != nil { return err }
        exists = false

    case exists && p.baseStale(cfg, out):
        // Ansible is idempotent: converge the delta instead of rebuilding.
        return p.reapplyBase(ctx, cfg, out)
    }

    if !exists {
        return p.BuildBase(ctx, cfg, out)
    }
    if status != "Stopped" {
        return p.Lima.StopStreaming(ctx, cfg.BaseName, out)
    }
    return nil
}
```

**3. `reapplyBase`** — start, converge, re-stamp, stop:

```go
func (p *Provisioner) reapplyBase(ctx context.Context, cfg vm.CreateConfig, out io.Writer) error {
    step(out, "Re-applying the playbook to the existing base image %q…", cfg.BaseName)
    if err := p.Lima.StartStreaming(ctx, cfg.BaseName, out); err != nil {
        return err
    }
    // Same in-guest script, same stdin-fed vars, same rsync filter as BuildBase.
    if err := p.runProvision(ctx, cfg.BaseName, cfg, "base", out); err != nil {
        // Leave the OLD stamp in place: the base is unambiguously stale, so the
        // next create retries. Never stamp a half-converged base — a fresh stamp
        // on a failed converge silently poisons every subsequent clone.
        return err
    }
    if err := writeBaseVersion(cfg.BaseName, version); err != nil {
        return err
    }
    return p.Lima.StopStreaming(ctx, cfg.BaseName, out)
}
```

**Order matters**: stamp only after `runProvision` returns nil, and stop after stamping.

**4. Progress bar.** The re-apply is a *third* Ansible run. `internal/ui/ansible.go` counts `TASK [` lines against a `SAND_ANSIBLE_TASK_TOTAL=` marker; without its own marker the bar would keep counting against the previous total. Ensure `runProvision` emits the marker for this run too (it should already, if you reuse it — verify).

**5. Tests.** These are the acceptance gate:
- Fake `limactl` Runner: stale base + no `--rebuild` → `Delete` is **never** called; the base-phase playbook runs; the stamp is rewritten.
- Stale base + failing playbook → stamp is **not** updated.
- `--rebuild` → `Delete` **is** called, and it is called *while the lock is held* (assert ordering against the lock acquisition, e.g. via a hook or a recorded call sequence).
- Concurrency: two goroutines through `prepareBaseAndClone`; assert no `Delete` interleaves with the other's `Clone`.
- Cancelled context mid-re-apply → lock released; a subsequent call proceeds rather than hanging.

</details>
