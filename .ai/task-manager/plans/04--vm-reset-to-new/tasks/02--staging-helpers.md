---
id: 2
group: "reset-flow"
dependencies: []
status: "pending"
created: "2026-06-27"
skills:
  - go
complexity_score: 4
complexity_notes: "Self-contained helpers; the only subtle parts are URL→org-dir parsing (mirrors the Ansible project role) and building the tar-over-shell argv, both unit-testable with the existing fake runner."
---
# Staging helpers: tar-out / tar-in + guest path resolution

## Objective
Provide the building blocks the reset flow needs to carry data across a destroy/recreate: resolve the guest user's home and the per-org project directory, and stream selected paths out of a running VM to a private host directory and back in (preserving modes/ownership). These helpers are pure/IO-thin and independently testable; the orchestration that sequences them lives in task 3.

## Skills Required
- `go` (Lima `Shell` streaming, `os` temp dirs, string/path parsing, tests with the fake runner)

## Acceptance Criteria
- [ ] A function resolves the project **org directory** (relative to the guest home) from a `CloneURL`, matching the Ansible `project` role: `https://github.com/org/repo(.git)` → `github.com/org`.
- [ ] A function resolves the guest user's home directory (via `getent passwd <user>` over `limactl shell`).
- [ ] `StageOut` streams given guest paths into a host archive file using `tar` over `limactl shell` (as root), and `StageIn` extracts a host archive back into the guest and re-`chown`s to the user.
- [ ] A helper creates a private (`0700`) host staging directory and a cleanup helper removes it.
- [ ] Unit tests cover org-dir parsing (several URL shapes) and the `tar`/`getent` argv construction via the fake runner; `cd tui && go test ./...` passes.

## Technical Requirements
- The VM has no writable host mount; data crosses via `limactl shell` streams. `tar` preserves permissions/ownership inside the archive; restored files still need an explicit `chown` because extraction runs as root.
- `lima.Client.Shell(ctx, name, stdin, out, argv...)` already streams stdin→guest and guest output→`out`. Use `out`=host file for tar-out, `stdin`=host file for tar-in.

## Input Dependencies
None.

## Output Artifacts
- `provision` helpers: `cloneOrgRelDir(cloneURL) (string, bool)`, `guestHome(ctx, name, user) (string, error)`, `StageOut(...)`, `StageIn(...)`, stage-dir create/cleanup. Consumed by the reset orchestration (task 3).

## Implementation Notes
<details>
<summary>Detailed implementation guidance</summary>

Create `tui/internal/provision/staging.go` and `tui/internal/provision/staging_test.go`.

**1. Org-dir parsing (pure) — mirror `roles/project/tasks/main.yml`:**
```go
// cloneOrgRelDir turns a clone URL into the per-org directory relative to the
// guest home, mirroring the project role: host = first path segment after the
// scheme, relpath = the rest minus a trailing ".git", orgRel = host/dirname(relpath).
// Returns ("", false) if the URL has no org component.
func cloneOrgRelDir(cloneURL string) (string, bool)
```
- Strip leading `scheme://` (regex `^[a-zA-Z]+://`). First segment = host (e.g. `github.com`). Remainder = relpath; strip a trailing `.git`. `orgRel = host + "/" + path.Dir(relpath)`. If `path.Dir(relpath)` is `.` (no org), return `false`.
- Tests:
  - `https://github.com/deviantintegral/claude-code-ansible` → `github.com/deviantintegral`, true
  - `https://github.com/org/repo.git` → `github.com/org`, true
  - `https://gitlab.com/group/sub/repo` → `gitlab.com/group/sub`, true
  - `https://github.com/justrepo` → `"", false`
  - `""` → `"", false`

**2. Guest home:**
```go
func guestHome(ctx context.Context, cli *lima.Client, name, user string) (string, error)
```
- Run `cli.Shell(ctx, name, nil, &buf, "getent", "passwd", user)`; parse the line `user:x:uid:gid:gecos:/home/user:/bin/bash` — split on `:`, home is index 5. Trim newline. Error if fewer than 7 fields.
- Test: feed a canned `getent` line via the fake runner's `Stream`… note the fake runner in `provision_test.go` does not write to `out`. For this test, extend the fake (or add a small local fake) so `Stream` can write canned stdout. Keep it minimal: a field like `streamOut map[string][]byte` keyed by argv[0] (`"shell"` is ambiguous; key by a substring match on the joined argv, e.g. contains "getent"). Assert the parsed home and the argv `["shell", name, "getent", "passwd", user]`.

**3. Staging dir:**
```go
func newStageDir() (string, error)         // os.MkdirTemp("", "claude-vm-reset-*"), then Chmod 0o700
func removeStageDir(dir string)            // best-effort os.RemoveAll
```

**4. Tar out / in (over shell, as root):**
```go
// StageOut writes guestPaths (relative to home) into hostArchive via tar.
func StageOut(ctx context.Context, cli *lima.Client, name, home string, guestPaths []string, hostArchive string) error
// StageIn extracts hostArchive into home and chowns the restored top-level paths to user.
func StageIn(ctx context.Context, cli *lima.Client, name, home, user string, topPaths []string, hostArchive string) error
```
- StageOut argv: `["sudo", "tar", "-C", home, "-czf", "-", p1, p2, ...]`; open the host archive file for writing and pass it as `out`. Skip paths that don't exist in the guest? Prefer `tar --ignore-failed-read` so a missing optional path (e.g. `~/.claude.json`) doesn't abort: argv `["sudo","tar","-C",home,"--ignore-failed-read","-czf","-", paths...]`.
- StageIn argv: `["sudo","tar","-C",home,"-xzf","-"]` with `stdin`=opened host archive file. After extract, chown: `["sudo","chown","-R",user+":"+user, topPath1, topPath2, ...]` (each relative path resolved to absolute under home, or run with `-C home`? chown needs paths — pass absolute `home+"/"+p`).
- Tests: assert the argv built for StageOut and StageIn (and chown) via the fake runner. Use temp files for the archive args so the file-open succeeds; you can write empty files. Verify ordering: tar extract then chown in StageIn.

**Paths used by the caller (task 3), documented here for clarity:**
- Claude archive top-level paths (relative to home): `.claude`, `.claude.json`.
- Project archive top-level path (relative to home): the `cloneOrgRelDir` result (e.g. `github.com/deviantintegral`) — this directory contains `.env` and the checkout.

Keep these helpers free of orchestration logic (no delete/clone/finalize here).
</details>
