---
id: 6
group: "secrets"
dependencies: [1]
status: "completed"
created: 2026-07-12
model: "sonnet"
effort: "high"
complexity_score: 6
complexity_notes: "The code change is small; the risk floor is what sets the tier. This is credential-handling code, and the task's real deliverable is the subsystem's first end-to-end test — the one that reads a secret back from inside a live guest."
skills:
  - go
  - lima
---
# Make saving a secret actually change the VM (+ the first secrets e2e test)

## Objective

**`ctrl+s` never reaches the guest.** `updateSecrets` (`internal/ui/secrets.go`) parses the
buffer, calls `m.sec.SetAll(...)`, and returns a **`nil` command**. The only three callers of
`provision.ApplySecrets` are `startCmd`, `restartCmd`, and `applySecretsCmd` (dispatched only from
`provisionDoneMsg`, after a create or reset). So editing a secret on a **running** VM writes the
host JSON and stops there.

The consequence is worse than a stale display: rotating an expired `GH_TOKEN` leaves the **dead
token live** in the guest's `~/.config/sandbar/secrets.env` and in every shell already sourcing it,
while the TUI shows the new one and reports success. The status line — *"secrets saved for X — they
apply on next start"* — reads like a considered design note and is in fact a description of the bug.

Fix that, fix the CRLF corruption in the same parse path, and write the subsystem's **first
end-to-end test**.

## Skills Required

`go`, `lima` — the provision/secrets guest-apply path and a real-Lima e2e test.

## Acceptance Criteria

- [x] **(A) Save applies to a running guest.** `ctrl+s` in the secrets editor batches an apply
      (`provision.ApplySecrets`) when the VM's status is `Running`. A **stopped** VM has nothing to
      apply to — its value legitimately lands on next start, which is the behaviour the status line
      currently (falsely) claims for everyone. The status message must tell the truth in both cases.
- [x] **(H) CRLF no longer corrupts values.** `parseSecrets` splits on the **raw** line rather than
      the trimmed one, so keys are trimmed and values are not — a buffer pasted with CRLF line
      endings puts a trailing `\r` **inside every value**, and `Render` faithfully single-quotes it
      into the guest environment. Fix the trim; add a unit test that parses a CRLF buffer and asserts
      no value carries a trailing `\r`.
- [x] The apply is **not** fire-and-forget: its failure is surfaced to the user (the messages strip /
      status), not swallowed. A save that could not reach the guest must not report success.
- [x] **The first secrets e2e test** (`//go:build limae2e`, gated on `LIMA_E2E`, in the style of
      `internal/provision/lima_e2e_test.go` and `internal/lima/copy_e2e_test.go`): on a VM that is
      **RUNNING and stays running**, edit a secret, save, and then **read the value back from inside
      the guest** — via `limactl shell` — **without restarting the VM**. There are currently **zero**
      secrets e2e tests (`grep -i secret` over both existing `limae2e` files returns nothing).
- [x] That e2e test **fails against the pre-fix code** and passes after. Demonstrate this: run it on
      the unfixed path first and paste the failure. This is the whole point of the task — a save that
      only updates the host store passes every existing in-process test and fails here.
- [x] `go test ./...` and `go test -race ./...` pass.
- [x] **Out of scope, and must not be touched** (they are a separate follow-up plan): the shadowed
      git-credential helper (`roles/user/templates/gitconfig.j2`), the scoped `.env` write that
      clobbers the clone token (`internal/provision/secrets.go` `scopeEnvScript`), dropped scopes
      leaving plaintext tokens in the guest, and `sand create --clone-token` not seeding the store.
      They are real, two of them destroy or leak a credential, and folding them in would roughly
      double this plan and bury them.

## Technical Requirements

- `func ApplySecrets(ctx context.Context, cli *lima.Client, name, user string, scopes map[string]map[string]string, out io.Writer) error`
  — already exists in `internal/provision`. All three existing call sites pass `io.Discard` as `out`.
- `func (m model) secretsFor(name string) (user string, scopes map[string]map[string]string)` in
  `internal/ui/commands.go` already assembles the arguments — reuse it.
- `applySecretsCmd(cli, name, user, scopes)` already exists in `commands.go`. The fix is largely
  *returning it* from the save path instead of `nil`, plus the running-VM gate and honest status.
- `parseSecrets` (`internal/ui/secrets.go`): skips blanks and `#`, validates scope via
  `secrets.ValidScope` and key via `secrets.ValidKey`, splits on the **first** `=`, rejects duplicate
  keys within a scope, and aborts the whole parse on any bad line. Only the trim is wrong.
- The e2e test should follow the existing pattern exactly: a minimal overlay in `t.TempDir()`
  (`template:_images/debian-13`, 2 cpus, 2GiB, `vm.BaseDiskFloor`), pre-emptive `_ = cli.Delete(...)`,
  unconditional `t.Cleanup` delete, and it should skip the ansible/playbook mount so boot is fast.
  Run with `go test -tags limae2e -timeout 30m -run TestE2E ./internal/ui/` (or wherever it lands)
  with `LIMA_E2E=1`.

## Input Dependencies

Task 01 — Charm v2. Otherwise independent of the board; this can land any time after the migration.

## Output Artifacts

- A secrets editor whose save actually changes the VM.
- A CRLF-safe `parseSecrets`.
- `*_e2e_test.go` — the secrets subsystem's first real-Lima test, asserting at the boundary the user
  cares about.

## Implementation Notes

<details>
<summary>Guidance</summary>

**Read this before writing a test.** The secrets editor already has behavioural tests that pass —
`TestSecretsEditorIsFocusedOnOpen`, `TestSecretsEditorTypeInsertsAndSaves`,
`TestSecretsEditorSaveValidPersists`. They drive the real key path through `Update`, they assert on
real store state, and **the editor is broken anyway**. They assert that the *host store* persisted.
The user's complaint is that the *VM* did not change. Those are different claims, and every existing
test makes the first one.

So: an in-process behavioural test for the apply is **necessary but not sufficient**, and adding one
does not close this task. The e2e test that reads the value back from **inside the guest** is the
deliverable. If you find yourself satisfied by an assertion that `m.sec` contains the new value, or
that a `tea.Cmd` was returned, you have written the same test that already exists and already passes
while the feature is broken.

The controlling rule from the plan: **an assertion must reach the boundary the user cares about.** A
test that stops at the nearest in-process state proves the code did what the code does, not that the
feature works. This claim crosses into a guest — so test the far side.

Sequence the e2e first if you can (RED → GREEN, per `PRE_TASK_EXECUTION.md`): write it, watch it fail
against the current code for the *right reason* (the guest still has the old value), then fix the save
path and watch it pass. That failure, pasted into your completion report, is the evidence this task
existed for.

**Test philosophy**: write a few tests, mostly integration. Meaningful tests verify custom business
logic, critical paths, and edge cases specific to this application — test *your* code, not the
framework. Here: the guest-apply-on-save e2e (the critical path), the running/stopped gate, and the
CRLF parse. Not: a test per validation rule that already has one.
</details>
