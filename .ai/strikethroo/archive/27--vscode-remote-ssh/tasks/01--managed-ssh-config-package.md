---
id: 1
group: "ssh-config"
dependencies: []
status: "completed"
created: 2026-11-13
skills:
  - go
  - openssh-config
complexity_score: 5
---
# Managed ssh config package (`internal/sshconfig`)

## Objective
Create a dependency-free package that renders per-VM ssh Host entries, upserts/removes them in sandbar's managed ssh config file, and detects/adds the `Include` line in `~/.ssh/config`.

## Skills Required
Go (file I/O, `internal/statelock`), OpenSSH client config semantics (`Include`, `Match all`, `ssh -G`).

## Acceptance Criteria
- [x] `Entry` renders deterministically; every block starts with `# sandbar begin <alias>` then `Match all`, and ends with `# sandbar end <alias>`.
- [x] `Upsert` replaces an existing block for the alias or appends; `Remove` deletes it; both use `statelock.Acquire` + re-read-then-write and write 0600 via temp file + rename.
- [x] `IncludeStatus` / `AddInclude` behave as specified (prepend, create `~/.ssh` 0700 and config 0600, refuse symlink/unwritable with an error carrying the exact line).
- [x] Values with newlines/control characters are rejected; paths with spaces are quoted.
- [x] `go test ./internal/sshconfig` passes, including a test (skipped when `ssh` is absent) that runs `ssh -F <tmp config> -G <alias>` for a local-Include entry and a Host-options entry from ONE managed file plus a pre-existing `Host` block in the user config, and asserts the resolved `hostname`/`port`/`user`/`identityfile`/`proxycommand`.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Managed file path: `${XDG_CONFIG_HOME:-~/.config}/sandbar/ssh_config` (absolute in the `Include`).
- User config path: `$HOME/.ssh/config`.
- No new module dependencies.

## Input Dependencies
None.

## Output Artifacts
`internal/sshconfig` with `Entry`, `Upsert`, `Remove`, `IncludeStatus`, `AddInclude`, `ManagedPath`, `UserConfigPath`, `IncludeLine`, plus alias helpers `ProfileAlias(profileName, vmName string) string` (`sandbar-<slug>-<name>`) and `LimaAlias(name) string` (`lima-<name>`).

## Implementation Notes
<details>

- Suggested `Entry`:
  ```
  type Entry struct {
      Alias string
      Include string        // local Lima: <instance dir>/ssh.config; when set, only Include is rendered
      HostName, User, IdentityFile, ProxyCommand string
      Port int              // 0 / 22 omitted
      IdentitiesOnly bool
      EphemeralHostKeys bool // StrictHostKeyChecking no, UserKnownHostsFile /dev/null, LogLevel ERROR
      AuthorizedKey string  // informational only (remote Lima: which .pub was authorized); never rendered
  }
  ```
  Include entries render as:
  ```
  # sandbar begin lima-foo
  Match all
  Include "/home/u/.lima/foo/ssh.config"
  # sandbar end lima-foo
  ```
  Host entries: `Match all`, then `Host <alias>` and indented options. `Match all` resets scope so an `Include` in a later block is not conditional on the previous `Host`.
- Validation: alias must match `^[A-Za-z0-9._-]+$`; other string fields must not contain `\n`, `\r` or other control chars; quote IdentityFile/Include paths with double quotes when they contain spaces (reject a value containing `"`). ProxyCommand is written verbatim after validation (provider code builds it with sshx.Quote).
- Slug: lowercase, `[a-z0-9]` kept, everything else → `-`, collapse/trim hyphens; empty → `profile`.
- Lock: `release := statelock.Acquire(path); defer release()`, read the file (missing = empty), splice, write a temp file in the same dir, chmod 0600, rename. Create the parent dir 0700.
- `IncludeStatus` returns a struct {Present, Exists, Symlink, Writable bool}. Present when any non-comment line, trimmed, case-insensitively starts with `include` and one of its args (quotes stripped) equals the absolute managed path or its `~/`-relative form. Use `os.Lstat` for symlinks. Writable: open with O_WRONLY (no truncate) and close; for a missing file, writable if `~/.ssh` exists-writable or can be created.
- `AddInclude` prepends:
  ```
  # Added by sandbar: VM host entries for VS Code Remote-SSH (sandbar code).
  Include "/abs/path/ssh_config"

  ```
  preserving the original mode (default 0600), atomically. Return `*ManualIncludeError{Line, Reason string}` for symlink/unwritable.
- Tests: `t.Setenv("HOME", t.TempDir())`, same for `XDG_CONFIG_HOME`; no `t.Parallel()`. Cover: upsert/replace/remove round trip, AddInclude onto an existing config that has `Host` blocks then `ssh -G` resolves both aliases, symlink refusal, injection rejection. `exec.LookPath("ssh")` → `t.Skip` if absent. For the Include entry create a fake `<dir>/ssh.config` with `Host lima-foo\n  Hostname 127.0.0.1\n  Port 60022\n  User u\n`.
- Test philosophy: write a few tests, mostly integration; test custom logic, critical workflows and edge cases; do not test third-party behaviour or trivial getters.
- AGENTS.md comment style: explain why; `gofmt`.

</details>
