---
id: 5
group: "remote-provider"
dependencies: [2, 3, 4]
status: "pending"
created: 2026-07-15
model: "opus"
effort: "xhigh"
skills:
  - go
  - ssh
complexity_score: 9
complexity_notes: "The core new capability: an SSH host-access implementation plus two remote-only hazards (nested-PTY interactive attach and two-stage host↔remote↔guest copy) whose failure modes are silent and destructive."
---
# Remote Lima over SSH provider

## Objective
Ship a fully working second backend that drives `limactl` on a remote host over
SSH and delivers the same create / list / start / stop / reset / copy / shell
experience as local Lima. Built as the Lima provider (tasks 2–3) configured with
a new **SSH host-access implementation** (task 1's seam), plus the two remote-only
concerns that need real design: the nested-PTY interactive attach and the
two-stage file copy.

## Skills Required
`go`, `ssh` (non-interactive remote exec, `-t` PTY allocation, nested-PTY behaviour, remote file staging).

## Acceptance Criteria
- [ ] An SSH implementation of the task-1 host-access seam runs `limactl` on the configured remote host (`ssh <host> limactl …`) with the same stdout/stderr separation and context-cancel/`WaitDelay` reaping semantics as the local impl, and reads/stats Lima instance files on the remote host over SSH.
- [ ] The interactive attach argv becomes `ssh -t <host> limactl shell …` wrapping the **unchanged** guest tmux expression; the grouped-session / `destroy-unattached`-on-grouped-only / exact-match `=main` semantics are preserved.
- [ ] File copy is resolved as a two-stage path (local ↔ remote host ↔ guest) so a host→guest transfer actually lands in the guest, preserving the `--backend=scp` placement contract at the guest end; the provisioner apt-cache seed/harvest, reset stage-out/in, and TUI transfer all route through the provider copy and work remotely.
- [ ] Selecting the remote provider via the task-4 config produces a working provider; the registry tags created VMs with the remote target and does not mix them with local instances.
- [ ] **Verification (real remote)**: against a real remote Lima host *or a loopback SSH target*, create a VM, open `sand shell`, confirm the guest `main` tmux session is created and **survives detach** (attach, detach, re-list → `main` still present), copy a known file into the guest and read it back via `limactl shell … cat` to prove placement, then stop/delete it — capturing terminal output of each step. Also show `go build ./... && go vet ./...` clean and `go test ./... -race` green.

## Technical Requirements
- The SSH impl must not duplicate the `limactl` argv-building logic — reuse the lima core from tasks 1–2 by swapping only the host-access implementation.
- Interactive attach must exec against the caller's real TTY (as `sand shell` does today), not through a captured pipe.
- No secrets on argv (preserve the stdin-vars discipline in `provision.runProvision`); over SSH, ensure the vars still arrive via stdin, not the command line or a process listing on the remote host.

## Input Dependencies
- Task 1: the host-access seam (SSH implements it).
- Task 2: the `Provider` interface and lima core the SSH impl backs.
- Task 3: centralised construction the remote provider slots into.
- Task 4: provider selection config and provider-tagged registry.

## Output Artifacts
- A working remote-Lima-over-SSH provider (validated end to end).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

**SSH exec.** The local impl runs `exec.CommandContext(ctx, "limactl", args...)`.
The remote impl runs `exec.CommandContext(ctx, "ssh", host, "limactl", args...)`
(with configured user/port/identity), keeping the same stdout/stderr split and
`cmd.WaitDelay`. The `limactl list` race sentinels (`ErrListRacedInstanceDir`)
still apply — the same limactl runs, just remotely — so keep the stderr-pattern
matching. Instance-file reads become `ssh <host> cat <path>` / `ssh <host> test`/
`stat` equivalents behind the task-1 file-access methods; the base version stamp
and base lock now live on the *remote* host under its `LIMA_HOME`.

**Attach (dangerous — read `internal/lima/attach.go` in full first).** Keep
`guestAttachExpr` byte-for-byte. The only change is the argv prefix: local is
`limactl shell --workdir H NAME bash -c <expr>`; remote is `ssh -t <host>
limactl shell --workdir H NAME bash -c <expr>`. This nests PTYs (`ssh -t` →
`limactl shell` → guest `bash` → `tmux`). The comment warns `destroy-unattached`
must be on the grouped session and NEVER on `main`, and `--workdir` must precede
NAME. Validate on a real remote that `main` survives detach — this is the single
most destructive failure mode.

**Copy (the other hazard).** `limactl copy` runs on the remote host, so its
`<vm>:/path` guest endpoint is fine but its *host* endpoint refers to the remote
host's filesystem, not the laptop. A host→guest upload therefore needs: stage
the local source to the remote host (scp), then `limactl copy` on the remote host
into the guest; a guest→host download reverses it. Preserve `--backend=scp` (the
placement contract in `client.go`'s `Copy` doc). Put this two-stage topology in
one place (the provider copy) so `aptcache.go`, `staging.go`, and `ui/transfer.go`
inherit it unchanged.

Test the SSH host-access impl with a fake so unit tests do not need a remote host
(assert it builds `ssh <host> limactl …` argv correctly), and gate the genuine
end-to-end behaviour behind the `limae2e`-family build tag (task 6). A loopback
SSH target (`ssh localhost` on this dev box, which has Lima+KVM) is an acceptable
"remote" for validation.
</details>
