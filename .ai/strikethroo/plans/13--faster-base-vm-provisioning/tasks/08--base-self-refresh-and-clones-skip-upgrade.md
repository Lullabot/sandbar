---
id: 8
group: "tier-1-freshness"
dependencies: [7, 3]
status: "pending"
created: 2026-07-13
model: "sonnet"
effort: "high"
skills:
  - go
  - ansible
complexity_score: 7
complexity_notes: "Mutates the base under the shared lock; a freshness read hoisted out of the lock makes every queued create redundantly re-upgrade."
---
# Base self-refresh at 30 days; clones stop re-paying `apt upgrade`

## Objective

Stop paying the same `apt upgrade dist` on every single clone. Move freshness from the clone to the base: when the base exceeds 30 days old, upgrade it **once, in place, under the lock**, and let every clone from that base skip the upgrade entirely.

## Skills Required

- **go** — the base build timestamp, the age check inside `ensureBaseStopped`, reusing task 7's re-apply machinery.
- **ansible** — removing `apt upgrade dist` from the finalize phase.

## Acceptance Criteria

- [ ] The base carries a **build timestamp** (it has none today — the stamp records only a version). Store it alongside the content hash from task 4.
- [ ] When the base is older than **30 days** (configurable), it is started, upgraded, re-stamped (fresh timestamp), and stopped — reusing task 7's `reapplyBase` machinery.
- [ ] The refresh runs **inside the base lock** (i.e. inside `ensureBaseStopped`). **No new locking.**
- [ ] The refresh **blocks and announces**: the create that trips it says so via `step()` (this IS a phase banner — it is user-facing news, unlike timing lines). Concurrent creates queue behind it, which is exactly what they already do behind a rebuild.
- [ ] **The age check is re-read AFTER the lock is acquired.** A create that waited behind someone else's refresh must observe the fresh timestamp and skip its own — otherwise every queued create redundantly re-upgrades. Verify with a test.
- [ ] `apt upgrade dist` is **removed from the finalize phase** — clones run no upgrade at all.
- [ ] A clone taken from a fresh (<30-day-old) base runs no `apt upgrade`/`dist-upgrade` task. Verify from the finalize task list.
- [ ] Artificially ageing the base stamp beyond the threshold and creating again runs the upgrade once on the **base** (not on the clone), and refreshes the timestamp.
- [ ] `go vet ./...`, `go test ./...`, and `ansible-playbook --syntax-check site.yml` are green.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- `roles/base/tasks/main.yml` ~:18-26: `Upgrade all apt packages` with `upgrade: dist`, gated to the **finalize** phase. This is the task to remove from finalize and relocate to the base refresh.
- `internal/provision/baseversion.go`: the stamp file today holds only `version + "\n"`. It needs a timestamp too — extend the format (e.g. two lines, or a small JSON object). Whatever the format, an **unparseable stamp must count as stale** (preserve today's behavior and task 4's `v2:` prefix check).
- `ensureBaseStopped` runs under the exclusive base lock held by `prepareBaseAndClone`. Both the staleness check (task 7) and this age check must be read there, after acquisition.
- `baselock.go` already tells a waiter that it is waiting, so the queueing experience is not new.

## Input Dependencies

- Task 7: `reapplyBase` (start → converge → stamp → stop) is the machinery the refresh reuses.
- Task 3: the consolidated apt structure, so the upgrade's removal from finalize is a clean edit.

## Output Artifacts

- A base build timestamp in the stamp.
- The 30-day in-lock, announced self-refresh.
- A finalize phase with no `apt upgrade`.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**1. Extend the stamp with a timestamp.** Keep it simple and forward-compatible:

```go
// Stamp file format (v2):
//   line 1: version   ("v2:<sha256>:<toolset>")
//   line 2: built-at  (RFC3339)
// An unparseable stamp counts as STALE — same as an empty one. Never guess.
type baseStamp struct {
    Version string
    BuiltAt time.Time
}
```

Write it on every successful build AND every successful re-apply/refresh.

**2. The age check, inside `ensureBaseStopped`, after the lock.**

```go
const baseMaxAge = 30 * 24 * time.Hour

// Read AFTER the lock is held. A create that blocked for minutes behind
// another's refresh MUST observe the fresh timestamp here and skip its own —
// otherwise every queued create redundantly re-upgrades the same base.
stamp := readBaseStamp(cfg.BaseName)
if exists && !stale && time.Since(stamp.BuiltAt) > baseMaxAge {
    step(out, "Base image %q is older than %d days; refreshing it once now "+
        "(other creates will queue behind this)…", cfg.BaseName, 30)
    return p.refreshBase(ctx, cfg, out)   // reuses reapplyBase's start/converge/stamp/stop
}
```

`refreshBase` can simply be `reapplyBase` with an extra var (e.g. `base_apt_upgrade: true`) that turns the upgrade task on for that run.

**3. Ansible.** Move the upgrade out of finalize. In `roles/base/tasks/main.yml`, change the gate from "finalize only" to "only when explicitly asked during a base run":

```yaml
- name: Upgrade all apt packages
  ansible.builtin.apt:
    upgrade: dist
    update_cache: true
  when: base_apt_upgrade | default(false) | bool
```

And emit `base_apt_upgrade: true` from `BuildExtraVars` **only** for the refresh run. Clones (finalize) never set it, so they never upgrade.

Note this reintroduces an `update_cache` — but only on the refresh path, not on the cold base build, so task 3's "one apt pass in the base build" criterion still holds.

**4. Tests.**
- Fresh base (BuiltAt = now) → no refresh, no upgrade var emitted.
- Aged base (BuiltAt = 31 days ago) → refresh runs, upgrade var emitted, timestamp rewritten.
- **The important one**: simulate a waiter — first goroutine refreshes and rewrites the timestamp; second goroutine, on acquiring the lock, re-reads and does **not** refresh. Assert `refreshBase` is called exactly once.
- Unparseable stamp → treated as stale.

</details>
