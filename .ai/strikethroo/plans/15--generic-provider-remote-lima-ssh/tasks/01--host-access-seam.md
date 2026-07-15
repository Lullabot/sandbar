---
id: 1
group: "foundation"
dependencies: []
status: "completed"
created: 2026-07-15
model: "opus"
effort: "high"
skills:
  - go
  - refactoring
complexity_score: 8
complexity_notes: "Concurrency-sensitive relocation of all Lima host-state access behind one seam; carries the WaitDelay orphan-reaping and limactl-list-race invariants that fail silently if disturbed."
---
# Host-access seam: generalize the Runner and relocate every ~/.lima filesystem touch behind it

## Objective
Introduce a single **host-access seam** that abstracts the two things that will
differ between local and remote Lima — (a) *running a `limactl` invocation* and
(b) *reading/stat-ing a Lima instance file* — while keeping local behaviour
byte-for-byte identical. Today `lima.Runner` already abstracts subprocess
execution but hardcodes the local `limactl` binary, and a dozen call sites read
`~/.lima/...` off the local filesystem directly. This task moves all of those
reads onto the seam and ships only the local implementation. No remote code and
no `Provider` interface yet — this is the layer beneath both.

## Skills Required
`go` (interfaces, `os/exec`, context cancellation, `io`), `refactoring` (behaviour-preserving relocation across packages).

## Acceptance Criteria
- [ ] A new host-access abstraction exists (extend/rename `lima.Runner`) with a **local** implementation that runs `limactl` via `os/exec` exactly as `execRunner` does today, including `WaitDelay` reaping of the orphaned ssh grandchild on context cancel.
- [ ] The abstraction also exposes instance-file access (read a named file under an instance dir; stat a path), with a local implementation reading the real filesystem.
- [ ] Every current direct `~/.lima` / instance-dir filesystem touch is routed through the seam: guest identity reads (`internal/lima/guest.go` `GuestHome`/`GuestUser`), base-overlay parse (`internal/provision/baseoverlay.go`), version stamp (`internal/provision/baseversion.go`), base lock (`internal/provision/baselock.go`), partial-instance cleanup (`internal/provision/cleanup.go`), and the TUI disk-usage / up-since / last-used sampling (`internal/ui/diskusage_*.go` and the list-enrichment reads).
- [ ] The `limactl list` clone/delete race handling (`ErrListRacedInstanceDir`, `listRacedInstanceDir`) and `ErrNoSuchInstance` are preserved unchanged.
- [ ] **Verification**: `go build ./... && go vet ./...` is clean; `go test ./... -race` passes with no changes required to existing assertions beyond mechanical seam substitution; and `grep -rn 'os.ReadFile\|os.Open\|os.Stat\|filepath.Join(limaHome' internal/provision internal/lima internal/ui --include='*.go' | grep -v '_test.go'` shows every remaining hit is *inside* the local host-access implementation, not scattered across callers.

## Technical Requirements
- Language: Go, module `github.com/lullabot/sandbar`.
- Preserve the existing `Runner` method contracts (`Output`, `Stream`, `StreamOut`) and their stdout/stderr separation semantics; add file-access methods rather than breaking them.
- Do not change `vm.VM` fields in this task.
- Keep all existing exported error sentinels.

## Input Dependencies
None — this is the base layer.

## Output Artifacts
- A host-access seam interface + local implementation consumed by tasks 2 and 5.
- All Lima host-state reads funnelled through it.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

Read `AGENTS.md` for the testing conventions (no test may require a real
`limactl`; no test may write host state; `isolateHostState`/`LIMA_HOME`).

The seam has two halves. Half one is today's `Runner` (in
`internal/lima/runner.go`): keep `Output`/`Stream`/`StreamOut` and the
`execRunner` implementation, including `const waitDelay = 2 * time.Second` and
`cmd.WaitDelay = waitDelay` — the doc comment there explains it reaps the
orphaned ssh grandchild on cancel; do not drop it. The only Lima-specific token
is `bin: "limactl"` in `NewExecRunner`; leave the binary name where a future
remote impl can vary it, but ship only the local exec form here.

Half two is new: instance-file access. Today these reads are scattered:
- `internal/lima/guest.go`: `GuestHome` reads `<instanceDir>/cloud-config.yaml`,
  `GuestUser` reads `<instanceDir>/ssh.config`.
- `internal/provision/baseoverlay.go`: reads `<limaHome>/<base>/lima.yaml`.
- `internal/provision/baseversion.go`: reads/writes the version stamp under
  `limaHome()/_sand`.
- `internal/provision/baselock.go`: the file lock under `limaHome()/_sand`.
- `internal/provision/cleanup.go`: `os.RemoveAll(limaHome()/<name>)`.
- `internal/ui/diskusage_unix.go` + the list enrichment (`vm.VM.DiskUsed`,
  `UpSince`, `LastUsed`) that `os.Stat`/read files in the instance dir.

Define file-access methods on the seam (e.g. read a file at an instance-relative
path; stat a path; remove an instance dir) and give each caller the seam instead
of `os`/`filepath` directly. The local implementation is a thin wrapper over the
current code, so behaviour is unchanged. The point is that a later SSH
implementation can satisfy the same methods by reading over ssh.

Preserve the seams already used as test hooks (`playbookVersionFn`,
`writeBaseVersionFn`, `readBaseVersionFn`, `readBaseBuiltAtFn`, `baseOverlayFn`,
`aptCacheHostDirFn`, and the ui `hostMemBytesFn`/`hostDiskFreeFn`) — this task
should compose with them, not remove them.

Do NOT introduce the `Provider` interface here (task 2) and do NOT add any ssh
code (task 5). Keep the diff to "same behaviour, one seam". This keeps the
coverage floor defensible because every existing test still exercises the same
code path through the local implementation.
</details>
