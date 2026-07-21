---
id: 5
group: "delete-guard"
dependencies: [1]
status: "completed"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 6
complexity_notes: "Security-invariant task: the delete confirmation must read only cached host data and issue ZERO guest contact. Risk floor applies; a test must prove no guest command runs on delete."
skills:
  - go
  - bubbletea
---
# Delete guard (zero guest contact)

## Objective
Extend the `d` (delete) confirmation so that, when the target VM's **cached**
registry shows work that exists only inside the VM, the dialog names it —
distinguishing "lost on delete" (unpushed/uncommitted) from "safe on GitHub"
(pushed, no PR) — **without ever executing inside the guest**. Deleting a VM you
believe is compromised must stay a pure, guest-untouched `limactl delete`.

## Skills Required
- **go** — reading cached registry state; the delete/confirm control flow.
- **bubbletea** — the `m.confirm` confirmation dialog rendering.

## Acceptance Criteria
- [ ] The `d` confirmation dialog is extended: when the cached registry shows
      VM-only work, the copy names it, e.g. "3 unpushed commits + uncommitted
      changes (only in this VM — lost on delete); 1 branch pushed without a PR
      (safe on GitHub)."
- [ ] For a **stopped** VM the data is labeled as of its `LastSeen`/last-seen time.
- [ ] **Hard boundary:** the guard reads **only** the host-persisted registry
      (task 1) and issues **no** `limactl shell`, no guest exec, nothing touching
      the instance. `Delete` semantics (removes disk + host-stored secrets,
      irreversible, `force` skips prompts) are otherwise unchanged.
- [ ] The guard is **informational only**: it never blocks the delete and never
      auto-lands/auto-pushes.
- [ ] A test asserts that exercising the delete-confirmation path performs **no
      guest command** (e.g. via a fake guest-shell/exec that fails the test if
      invoked, or by asserting the delete path never calls the shell resolver).
- [ ] `go test ./internal/ui/... -race` passes, including confirmation-copy tests
      for: unpushed-only, dirty-only, both, pushed-no-PR ("safe"), nothing (copy
      unchanged from today), and the stopped-VM last-seen label.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Extend only the **confirmation copy** around the existing delete path
  (`m.confirm` in `internal/ui/messages.go`/`jobs.go`; provider
  `Delete(name, force)` in `internal/provider/provider.go`). Do not modify the
  delete call itself or add any pre-delete guest refresh.
- Category wording must be honest and match the plan: "lost on delete" vs "safe
  on GitHub"; `force` still skips prompts entirely.
- This is a **risk-floor** task (the zero-guest-contact invariant is a security
  property) — treat the "no guest contact on delete" test as the primary
  acceptance signal, not an afterthought.

## Input Dependencies
- Task 1: cached registry accessors (`Get`, `LastSeen`, push/dirty fields).

## Output Artifacts
- Extended delete-confirmation rendering + tests, including the no-guest-contact
  assertion.

## Implementation Notes
<details>
<summary>Detailed implementation guidance</summary>

1. Find the delete confirmation construction (search `m.confirm` and the delete
   verb in `commandreg.go`/`jobs.go`/`messages.go`). Where the confirm prompt
   string is built for a delete, look up the VM's cached `VMCheckouts` from the
   registry (task 1) and compose a summary line:
   - Count unpushed commits (`Ahead` over `unpushed`/`never` rows) and whether
     any `Dirty > 0` → "N unpushed commits + uncommitted changes (only in this
     VM — lost on delete)".
   - Count `pushed`-with-no-PR rows → "M branch(es) pushed without a PR (safe on
     GitHub)".
   - If nothing VM-only, leave today's copy unchanged.
2. Stopped VM: if the registry entry's `SweptAt`/`LastSeen` indicates the VM
   isn't currently swept, append an "(as of <last-seen>)" label.
3. **Never** call the guest. The whole point: read the registry only. Add a test
   that injects a guest-shell resolver which `t.Fatal`s if called, then drives the
   delete-confirmation code path and asserts it was never invoked.
4. Keep `force` behavior identical (skips prompts → skips the guard copy too).
5. RED→GREEN→REFACTOR on the copy-composition function (pure: `VMCheckouts` →
   confirmation string) — that is the meaningful custom logic worth testing hard.
</details>
