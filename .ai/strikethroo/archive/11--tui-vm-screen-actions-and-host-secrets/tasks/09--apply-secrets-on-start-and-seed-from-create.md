---
id: 9
group: "tui-secrets"
dependencies: [8]
status: "completed"
created: 2026-07-09
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Closes the host→guest loop across three call sites (start, restart, and the post-create/reset start). Failure semantics are subtle: a secrets failure must warn without failing the start. Security-adjacent."
skills:
  - go
  - bubbletea
---
# Apply secrets on start/restart; seed the store from the create form

## Objective

Close the loop: an edit made while a VM is stopped must take effect the next time
it comes up. Wire `provision.ApplySecrets` into `startCmd` and `restartCmd`, and
carry a create-form GitHub token into the host store so it becomes the VM's
`GH_TOKEN` secret going forward.

A failure to apply secrets warns but does **not** fail the start: a VM that is up
without its secrets is strictly more useful than a VM reported as failed-to-start.

## Skills Required

- **go** — threading a store and a user through command constructors; error
  semantics that distinguish "warn" from "fail".
- **bubbletea** — `tea.Cmd` construction and `actionDoneMsg` extension.

## Acceptance Criteria

- [ ] `startCmd` applies the VM's secrets **after** a successful `cli.Start(name)`.
      If `Start` fails, `ApplySecrets` is not attempted.
- [ ] `restartCmd` applies secrets after its `Start` too. This is **not** redundant
      with `startCmd`: `restartCmd` calls `cli.Stop` then `cli.Start` directly
      through `lima.Client`, not by re-dispatching `startCmd`, so it would
      otherwise skip the step.
- [ ] The create and reset flows apply secrets after their final start. Both end
      with a `StartStreaming` inside `provision`; wire the apply so it runs once the
      provisioning command completes successfully (see Implementation Notes for the
      two viable seams — pick one and justify it in a comment).
- [ ] A create-form GitHub token (`cfg.CloneToken`, non-empty) is written into the
      host store as the VM's `GH_TOKEN` secret on a successful create. Existing
      secrets for that VM name are not clobbered wholesale — merge, with the
      create-form token winning for the `GH_TOKEN` key only.
- [ ] `registry.Add` still strips `CloneToken`; `managed-vms.json` still contains no
      token. The token reaches `secrets.json`, never the registry.
- [ ] An `ApplySecrets` error surfaces on `m.status` as a warning **appended to a
      successful start**, and `actionDoneMsg.err` stays nil. The VM is reported as
      started.
- [ ] Applying an empty secret set removes the guest file (task 2's `ApplySecrets`
      already does this) — so a VM with no secrets does not carry a stale
      `secrets.env` from a previous configuration.
- [ ] The store is pruned when a VM goes away: `m.sec.Remove(name)` runs alongside
      the existing `m.reg.Remove(name)` on delete, and alongside `manage.Reconcile`
      dropping a VM that vanished outside the TUI.
- [ ] The known limitation is recorded in a code comment: a VM started outside
      `sand` (a bare `limactl start`) does not get freshly-applied secrets; it
      sources whatever `secrets.env` was last written.
- [ ] Verification: `go test ./internal/ui/... ./internal/provision/... -v` passes,
      including:
      ```
      go test ./internal/ui/... -run 'StartAppliesSecrets|SecretsWarnNotFail|TokenSeedsStore|SecretsPruned' -v
      ```
      Expected `PASS`, with tests asserting: (a) a successful `Start` is followed by
      an `ApplySecrets` call carrying that VM's stored pairs; (b) a failing `Start`
      is **not** followed by one; (c) a failing `ApplySecrets` after a successful
      `Start` yields `actionDoneMsg{err: nil}` and a status containing both `ok` and
      a warning; (d) creating a VM with `CloneToken: "ghp_x"` leaves
      `sec.Get(name)["GH_TOKEN"] == "ghp_x"` and leaves the registry's stored config
      with an empty `CloneToken`; (e) deleting a VM calls `sec.Remove`.
- [ ] Verification: `go build ./... && go vet ./...` succeed, and
      ```
      XDG_DATA_HOME=$(mktemp -d) go test ./internal/... -count=1
      ```
      passes (no test writes to the developer's real store).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Files: `internal/ui/commands.go`, `internal/ui/model.go`,
  `internal/ui/detail.go` (if the status warning is rendered there), and tests.
- `ApplySecrets(ctx, cli, name, user, pairs, out)` comes from task 2. It needs the
  **guest user**, which is `cfg.User` — available from `m.reg.Config(name)`. For a
  VM with no recorded config, fall back to `vm.HostUser()`, matching how
  `openResetForm` seeds a missing config.
- `startCmd`/`restartCmd` currently take `(cli *lima.Client, name string)`. They
  will need the store and the user too. Prefer passing the resolved
  `pairs map[string]string` and `user string` in, rather than the `*secrets.Store`
  — it keeps the command a pure function of its inputs and makes the tests trivial.
- `actionDoneMsg` may need a `warn string` field to carry the non-fatal
  `ApplySecrets` error. Adding one is preferable to encoding a warning in `err`,
  which the `msg.err != nil` branch treats as failure.

## Input Dependencies

- **Task 8**: the `*secrets.Store` handle on the model (`m.sec`) and the editor
  that populates it.
- **Task 2** (transitively): `provision.ApplySecrets` and `secrets.Store`.

## Output Artifacts

- Secrets that actually reach the guest, on every `sand`-initiated start.
- A create-form token that persists as an editable secret.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Read first:** `internal/ui/commands.go` in full (94 lines), `internal/ui/model.go`
lines 187–248 (the `vmsLoadedMsg`, `actionDoneMsg`, and `provisionDoneMsg` cases),
`internal/manage/manage.go` (`RecordSuccess`, `Reconcile`), and task 2's
`ApplySecrets` signature.

**Step 1 — command signatures.** Make the commands pure:

```go
// startCmd boots a stopped VM and then writes its host-stored secrets into the
// guest. A secrets failure is reported as a warning, not a failure: a VM that is
// up without its secrets is more useful than one reported as failed-to-start.
//
// Note: a VM started outside sand (a bare `limactl start`) does not get freshly
// applied secrets — it sources whatever secrets.env was last written.
func startCmd(cli *lima.Client, name, user string, pairs map[string]string) tea.Cmd {
    return func() tea.Msg {
        if err := cli.Start(name); err != nil {
            return actionDoneMsg{action: "start", name: name, err: err}
        }
        warn := ""
        if err := provision.ApplySecrets(context.Background(), cli, name, user, pairs, io.Discard); err != nil {
            warn = "secrets not applied: " + err.Error()
        }
        return actionDoneMsg{action: "start", name: name, warn: warn}
    }
}
```

⚠️ `internal/ui` does not currently import `internal/provision` from
`commands.go` — check for an import cycle. `ui` already imports `provision` in
`model.go` and `form.go`, so there is none.

⚠️ `context.Background()` is right here: these are quick actions with no cancel
UI, unlike the provisioner which threads `m.cancel`. Do not thread a cancelable
context in without also wiring a cancel key, or you will create a context that is
never canceled and looks like a leak to a reviewer.

**Step 2 — resolving `user` and `pairs` at the call site.** In `updateDetail`:

```go
// secretsFor returns the guest user and stored secrets for a VM, defaulting the
// user to the host username when the VM has no recorded config (mirroring
// openResetForm's fallback).
func (m model) secretsFor(name string) (user string, pairs map[string]string) {
    user = vm.HostUser()
    if cfg, ok := m.reg.Config(name); ok && cfg.User != "" {
        user = cfg.User
    }
    return user, m.sec.Get(name)
}
```

**Step 3 — `actionDoneMsg.warn`.** In `model.go`'s handler, after the existing
success branches set `m.status = label + " ok"`:

```go
if msg.warn != "" {
    m.status += " (warning: " + msg.warn + ")"
}
```

Place this after the `switch`, so it augments whichever success branch ran, and
guard it so it does not append to a failure message (which already carries its own
error). Read the switch carefully — the `msg.err != nil` case returns early or
falls through depending on how task 5 left it.

**Step 4 — the create/reset seam.** Two options:

*Option A — inside the provisioner.* `createVM` and `Reset` both end with a
`StartStreaming`. Call `ApplySecrets` there. **Rejected:** `provision` would need
to import `secrets` and know about the host store, and the headless
`sand create` path would silently gain the behaviour without a way to populate the
store. Do not do this without a plan change.

*Option B — in the TUI, on `provisionDoneMsg`.* The model already handles
`provisionDoneMsg` and calls `manage.RecordSuccess(m.reg, m.provCfg)` on success.
Extend that branch: seed `GH_TOKEN` from `m.provCfg.CloneToken` (merging into any
existing pairs), persist, then dispatch a follow-up command that applies them.

**Take Option B.** It keeps the store a TUI concern, matches where
`RecordSuccess` already lives, and needs no new `provision` imports. Write the
justification as a comment.

```go
case provisionDoneMsg:
    ...
    if msg.err == nil && m.provCfg.Name != "" {
        if err := manage.RecordSuccess(m.reg, m.provCfg); err != nil { ... }
        // The create form's token becomes the VM's GH_TOKEN secret, so it can be
        // edited later without a rebuild. It never enters the managed registry,
        // which strips CloneToken by design.
        if m.provCfg.CloneToken != "" {
            pairs := m.sec.Get(m.provCfg.Name)
            if pairs == nil { pairs = map[string]string{} }
            pairs["GH_TOKEN"] = m.provCfg.CloneToken
            if err := m.sec.Set(m.provCfg.Name, pairs); err != nil { ... }
        }
    }
```

⚠️ `m.sec.Get` must return a **copy**, or this mutates the store's internal map
before `Set` validates. Check task 2's implementation; if it returns the live map,
either fix it there or clone here. State which you did.

Do the VM's secrets need applying after a create? The finalize playbook already
wrote the per-org `.env`, and the create ends with a start — but that start is
*inside* `createVM`, before `provisionDoneMsg` fires and before `GH_TOKEN` lands in
the store. So the guest has no `secrets.env` until the *next* start. Two choices:
dispatch an apply command from `provisionDoneMsg`, or accept the one-start delay.
**Dispatch the apply** — the acceptance criterion "creating a VM with a token
leaves it in the store" is about the store, but a user who creates a VM and
immediately shells in should find `GH_TOKEN` set. Return the apply as the `tea.Cmd`
alongside `listCmd`, batched.

**Step 5 — pruning.** In the `actionDoneMsg` delete branch, beside
`m.reg.Remove(msg.name)`, add `m.sec.Remove(msg.name)`. In the `vmsLoadedMsg`
branch, `manage.Reconcile(m.reg, msg.vms)` returns the names it dropped (check its
signature — it returns `(something, error)`); prune those from `m.sec` too. If
`Reconcile` does not return the dropped names, extend it — but keep it the single
shared place where the TUI and the headless path agree on reconciliation, per the
plan's Integration Strategy. Do not fork the logic into `ui`.

**Step 6 — test isolation.** The store writes under `$XDG_DATA_HOME`. Every test
touching it must `t.Setenv("XDG_DATA_HOME", t.TempDir())`. The final verification
command in the Acceptance Criteria exists to catch a test that forgot.

**Testing philosophy.** Write a few tests, mostly integration. Meaningful tests
verify custom business logic, critical paths, and edge cases specific to this
application — test *your* code, not the framework or library.

Write tests for: custom business logic and algorithms; critical user workflows
and data transformations; edge cases and error conditions for core functionality;
integration points between components; complex validation logic or calculations.

Do NOT write tests for: third-party library functionality; framework features;
simple CRUD operations without custom logic; trivial getters/setters or static
configuration; obvious functionality that would break immediately if incorrect.

Here that means: the start→apply ordering, the warn-not-fail semantics, and the
token-seeds-store-not-registry invariant are the integration points that earn
tests. Do not test `lima.Client.Start`.

</details>
