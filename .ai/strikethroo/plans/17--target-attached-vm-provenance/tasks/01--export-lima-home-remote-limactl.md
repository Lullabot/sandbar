---
id: 1
group: "transport-fixes"
dependencies: []
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "medium"
skills:
  - go
  - ssh
---
# Export LIMA_HOME to the remote limactl invocation

## Objective
Fix the latent bug where the profile's `RemoteLimaHome` is honored for host
file reads but never reaches the remote `limactl` command. Make the remote
`limactl` run with `LIMA_HOME` set to the resolved remote Lima home, so
discovery (`limactl list`) and sand's own file reads resolve the same instance
directory on hosts with a non-default Lima home. This is self-contained and
lands independently of the provenance work.

## Skills Required
- `go` — modify the SSH command construction in `internal/lima/sshhost.go`.
- `ssh` — understand remote-process env assignment vs. local ssh client env.

## Acceptance Criteria
- [ ] The remote `limactl` invocation built by `sshCommand` includes a
  `LIMA_HOME=<resolved remote home>` assignment applied to the **remote**
  process (inside the quoted remote command), not the local ssh client env.
- [ ] Only that single env var is asserted across the hop — no other local env
  (`XDG_*`, local `LIMA_HOME`) leaks to the remote (the non-leak property is
  preserved).
- [ ] A unit test asserts the constructed remote argv/command string for a
  `limactl` call carries `LIMA_HOME=<value>` when `RemoteLimaHome` is set, and
  does not when it is the remote default (or asserts the chosen behavior
  explicitly).
- [ ] `go build ./...` and `go test ./internal/lima/...` pass.
- [ ] Verification command: `go test ./internal/lima/... -run LimaHome -v` exits
  0 and shows the new test passing.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- File: `internal/lima/sshhost.go` — `sshBase`, `sshCommand`, `Output`,
  `runRemote`/`Stream` argv construction.
- `SSHConfig.RemoteLimaHome` (default `".lima"`) and `LimaHome()` accessor.
- The remote command is a shell-quoted argv appended to the ssh base; a remote
  env assignment is expressed as `LIMA_HOME=<v> limactl …` in the remote command
  string (properly quoted), NOT as an ssh `-o SetEnv` unless the code already
  uses that mechanism.

## Input Dependencies
None. This is a leaf task.

## Output Artifacts
- Updated `sshCommand`/`Output` that prefixes `LIMA_HOME` on the remote
  `limactl` command.
- A unit test locking the behavior.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

Today (verified) `sshBase` emits only ssh flags (`-t`, `-p`, `-i`, mux `-o`
flags) + target; `sshCommand` appends the shell-quoted remote argv; `Output`
prepends `"limactl"`. There is no `LIMA_HOME=` anywhere and no `cmd.Env`
threading, so the remote `limactl` uses whatever `LIMA_HOME` the remote login
shell has, while sand reads files from `h.cfg.RemoteLimaHome` — these diverge on
a non-default home.

Fix approach:
1. In the code path that builds the remote `limactl` command (where `Output`
   prepends `"limactl"`), prepend an env assignment so the effective remote
   command is `LIMA_HOME=<remoteHome> limactl <args…>`. Use the SAME resolved
   value `LimaHome()` returns for reads, so reads and discovery agree.
2. Quote it safely with the existing shell-quoting helper used for remote argv.
   If `RemoteLimaHome` is relative (e.g. `.lima`), that is fine — it resolves
   against the remote `$HOME` at command time, consistent with how sand's file
   reads treat it.
3. Do NOT add ssh `SetEnv`/`SendEnv` or export any local env; assign only this
   one var on the remote side to preserve the non-leak property (the laptop's
   local `LIMA_HOME`/`XDG_*` must still never cross the hop).
4. Decide and document one behavior for the default case: simplest correct
   choice is to ALWAYS set `LIMA_HOME=<LimaHome()>` on the remote `limactl`
   (even when it equals the remote default), since it is always what sand
   intends. The test should assert whichever behavior you implement.

Test: construct an `SSHHost` with a known `RemoteLimaHome`, call the (possibly
unexported — use an internal test in package `lima`) command-building function
for a `list` invocation, and assert the resulting command string contains
`LIMA_HOME=<value>` positioned before `limactl`. Name it so
`go test ./internal/lima/... -run LimaHome` selects it. Verify against the
existing loopback/second-user e2e expectations — do not break how those tests
construct remote commands.

Keep the change minimal and localized; this task must not touch provenance code.
</details>
