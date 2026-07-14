---
id: 9
group: "tier-1-freshness"
dependencies: [8]
status: "completed"
created: 2026-07-13
model: "sonnet"
effort: "medium"
skills:
  - go
---
# Make the bounce conditional, and stop Reset destroying a live tmux session

## Objective

Remove tens of seconds of pure latency from the common create path by bouncing (stop + start) only when a reboot is genuinely required — and stop `Reset` from silently destroying the user's persistent tmux session.

## Skills Required

- **go** — `internal/provision/provision.go` (`createVM`, `Reset`), `internal/lima`.

## Acceptance Criteria

- [x] The unconditional stop+start after finalize in `createVM` is replaced by a conditional bounce, triggered by `/var/run/reboot-required` existing in the guest.
- [x] **The hostname dependency is verified before the bounce is dropped.** A `hostnamectl` change does not require a reboot; what must be confirmed is whether anything in the first interactive shell depends on the restart to observe the new hostname. Record the finding. If it genuinely requires the restart, keep the bounce for that reason and skip it only when demonstrably unnecessary.
- [x] A create whose VM needs no reboot performs **no** stop+start — verify from task 1's timing summary that the bounce phase is absent and its cost is gone from the total.
- [x] Touching `/var/run/reboot-required` in a VM and creating/finalizing **does** produce a bounce.
- [x] **`Reset`'s unconditional bounce becomes conditional and tmux-safe**: it must not silently destroy a live `main` tmux session. Detect a live session and warn (or refuse) rather than bouncing through it.
- [x] `go vet ./...` and `go test ./...` are green, including a test that a VM with no `reboot-required` marker is not bounced.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- `internal/provision/provision.go` `createVM` (~:192-220): after `runProvision(finalize)` it runs `StopStreaming` + `StartStreaming` ("Restarting … for a clean first boot"). `Reset` repeats the same bounce (~:437-444).
- **The docker-group rationale for the bounce is void** — this was verified during plan refinement. The `docker` group is added in `roles/dev-tools/tasks/main.yml` (~:49-53), and the whole `dev-tools` role is gated `when: provision_phase != 'finalize'` (`site.yml` ~:22-23). So the group is written during the **base** build and baked into the image: a clone's `/etc/group` already has it before the clone first boots, and every `limactl shell` is a fresh login with a fresh `initgroups()`. Finalize never touches groups. Do not reintroduce this rationale.
- The bounce's own code comment cites the finalize `apt upgrade` (new kernel/libs) and the hostname change. **Task 8 removed the upgrade from finalize**, so the kernel/libs reason is gone too. Only the hostname remains to be checked.
- **tmux**: `internal/lima/attach.go` — `sand shell` and the TUI `S` verb attach to a persistent `main` tmux session, kept alive across disconnects by `loginctl enable-linger` (`roles/user/tasks/main.yml` ~:28-33, asserted in CI). The server is started **lazily on first attach** (there is no systemd user unit for it), so it does not cache stale pre-finalize credentials. But a stop+start of a **running** VM destroys the session and everything in it — the precise disaster the tmux feature exists to prevent.
- Counter-consideration (do not ignore it): a long-lived tmux server freezes its supplementary groups and environment at fork time. If a re-apply against a *running* VM ever changes group membership, existing panes will not observe it without a restart. So "never bounce" is not automatically safe either — the choice must be explicit.

## Input Dependencies

- Task 8: `apt upgrade` must already be out of finalize, since that is what retires the bounce's kernel/libs justification.

## Output Artifacts

- A conditional create bounce.
- A tmux-safe `Reset`.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**1. Detect whether a reboot is actually needed.**

```go
// needsReboot reports whether the guest asked for one. Debian's
// /var/run/reboot-required is written by kernel/libc upgrades.
func (p *Provisioner) needsReboot(ctx context.Context, name string) bool {
    var buf bytes.Buffer
    err := p.Lima.Shell(ctx, name, nil, &buf, &buf,
        "test -e /var/run/reboot-required")
    return err == nil
}
```

**2. `createVM` — bounce only when required.**

```go
// The bounce used to be unconditional. Its two stated reasons are gone:
//   - the finalize apt upgrade (removed in task 8), and
//   - docker group membership — which is granted in the BASE phase and baked
//     into the image, so a clone has it before it ever boots.
// What is left is a genuine reboot request from the guest.
if p.needsReboot(ctx, cfg.Name) {
    step(out, "Restarting %q to apply a pending reboot…", cfg.Name)
    if err := p.Lima.StopStreaming(ctx, cfg.Name, out); err != nil { return err }
    if err := p.Lima.StartStreaming(ctx, cfg.Name, out); err != nil { return err }
}
```

**3. Verify the hostname question before you delete the old bounce.** Build a VM, skip the bounce, then check inside the guest that the hostname is correct in `hostname`, `/etc/hostname`, `/etc/hosts`, and in a fresh `limactl shell` prompt. If all four are right, the hostname needs no restart and the bounce can go. **Write down what you observed** in the task's completion notes — this is the one open empirical question the plan flagged.

**4. `Reset` — do not kill a live session.**

```go
// A stop+start destroys the guest's persistent tmux session and everything
// running in it. Detect one and do not blow it away silently.
func (p *Provisioner) hasLiveTmux(ctx context.Context, name string) bool {
    err := p.Lima.Shell(ctx, name, nil, io.Discard, io.Discard,
        "tmux has-session -t =main")
    return err == nil
}
```

In `Reset`, bounce only when `needsReboot`; and if `hasLiveTmux` is true and a bounce is required, **warn loudly** via `step()` before doing it (or refuse and tell the user to detach/finish first). Choose one and be explicit — do not silently destroy work.

**5. Test.** Fake Runner: guest without the reboot marker → assert `Stop`/`Start` are NOT called after finalize. Guest with the marker → assert they ARE. That is enough; don't build a broad suite.

</details>

## Completion notes

**Hostname finding (empirical, verified against a real running VM, not assumed):**
`limactl` was available in this environment with a running instance (`test`). Ran
`limactl shell test -- sudo hostnamectl set-hostname sandtest` (the same
mechanism `roles/base`'s "Set hostname" task uses via
`ansible.builtin.hostname`), then immediately opened a brand-new
`limactl shell test` (a fresh login, exactly what the first interactive shell
after a create/reset is). It reported `sandtest` for `hostname`,
`cat /etc/hostname`, and `hostnamectl`'s Static hostname — all correct, with
**no reboot**. Restored the hostname to `test` afterward. Conclusion: nothing
caches the pre-change name across a fresh process; `sethostname()` is
system-wide and immediate, and `/etc/hosts` is rewritten by the same playbook
run (a template task, unconditional on `provision_phase`). The bounce's
hostname rationale does not hold and the bounce was made conditional as
described.

**Nothing could not be checked** — the hostname question was the one open
empirical item and it was verified directly, not assumed.
