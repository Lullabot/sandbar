---
id: 3
group: "provenance-seam"
dependencies: [2]
status: "completed"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Trust-boundary I/O across local + SSH transports, a new batched-read seam method, and relative-LimaHome path handling."
skills:
  - go
  - ssh
---
# Implement Lima provenance marker I/O (write, read, batched read, unmark) and expose it

## Objective
Implement the `Provenancer` interface for Lima as a marker file at
`<LimaHome>/<name>/sandbar.json`, read/written through the `HostFiles` seam so it
works identically for local (`localFiles`) and remote (`SSHHost`) hosts. Add the
one-round-trip batched read (a new `HostFiles` capability), and expose the
implementation through the provider so `local.go` uses it directly and
`remote.go` inherits it via embedding.

## Skills Required
- `go` — implement across `internal/lima` and `internal/provider`.
- `ssh` — write the compound `sh -c` batched read over the multiplexed
  connection.

## Acceptance Criteria
- [ ] Marker write/read/unmark implemented against `hf.LimaHome()` for both
  local and SSH hosts; path is `filepath.Join(hf.LimaHome(), name, "sandbar.json")`
  and MUST NOT assume `LimaHome()` is absolute (remote may return `.lima`).
- [ ] A single batched read returns `map[string]Provenance` in ONE host
  round-trip: `SSHHost` uses one compound `sh -c 'for d in <LimaHome>/*/sandbar.json; …'`
  (mirroring the existing `HostResources` pattern); `localFiles` does an
  equivalent single directory pass.
- [ ] Missing or unparseable markers are treated as "unmanaged" and never abort
  the batched read or a listing.
- [ ] `limaProvider` satisfies `Provenancer`; `remoteLimaProvider` inherits it
  through embedding (no override needed, confirmed by a compile-time
  `var _ provider.Provenancer = ...` assertion).
- [ ] Unit tests (using the local `HostFiles` / a fake host) cover: write→read
  round-trip, unmark, batched read of multiple markers, and a malformed marker
  being skipped. Verification command:
  `go test ./internal/lima/... ./internal/provider/... -run Provenance -v` exits 0.
- [ ] `go build ./...` passes.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Files: `internal/lima/hostfiles.go` (add batch method to the `HostFiles`
  interface + both impls), `internal/lima/sshhost.go` (SSH batched read),
  `internal/lima/client.go` or a new `internal/lima/provenance.go` (marker
  encode/decode + path), `internal/provider/local.go`/`remote.go` (expose).
- Reuse `WriteFile(path, data, dirPerm, filePerm)` and `ReadFile`/`Stat`
  (already present on both `localFiles` and `SSHHost`).
- Reuse the `runRemote`/`Output` compound-command precedent from `HostResources`.

## Input Dependencies
- Task 2: the `Provenance` struct + `Provenancer` interface + `ErrUnsupported`.

## Output Artifacts
- A working Lima `Provenancer` (local + remote) and a batched `HostFiles` read,
  consumed by tasks 4, 5, 6, 7.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

**Path.** `markerPath(hf, name) = filepath.Join(hf.LimaHome(), name, "sandbar.json")`.
`LimaHome()` returns `$LIMA_HOME`/`~/.lima`/`""` locally and
`cfg.RemoteLimaHome` (default `.lima`, possibly relative) remotely. Relative is
OK: `cat`/`stat`/`mkdir` run in a login shell resolve it against `$HOME`. Do not
`filepath.Abs` the remote path.

**Write (MarkManaged).** JSON-encode `Provenance`, `WriteFile(markerPath, data,
0o700, 0o600)` (match the restrictive perms the existing stamp writes use). The
instance dir already exists at create time (this is called from task 4's create
path, after clone/boot).

**Single read (ProvenanceOf).** `ReadFile(markerPath)`; map `fs.ErrNotExist` →
`(Provenance{}, false, nil)`; a JSON parse error → treat as unmanaged
(`false, nil`) rather than surfacing, so one bad marker never hides a VM's peers.

**Batched read (Provenance).** Add to the `HostFiles` interface a method like
`ReadInstanceMarkers(ctx, limaHome, filename string) (map[string][]byte, error)`
returning instance-name → raw bytes for every `<limaHome>/<name>/<filename>`
present. Implementations:
- `localFiles`: `os.ReadDir(limaHome)`, for each entry that is a dir, try
  `os.ReadFile(filepath.Join(limaHome, entry, filename))`, skip not-exist.
- `SSHHost`: one `runRemote` of a `sh -c` script that iterates
  `"$LIMA_HOME"/*/sandbar.json` (LIMA_HOME passed in, or the literal home) and
  emits parseable framed output — e.g. for each file print a header line
  `==> <name>` then the file bytes, or better, emit NUL/length-prefixed records
  to survive arbitrary JSON. A robust, simple encoding: for each marker run
  `printf '%s\t' "$name"; wc -c` then the bytes — or simplest and safe: use
  `tar cf - -C "$LIMA_HOME" <glob>` and untar in Go. Pick one and parse
  controller-side into `map[name][]byte`. Then the Lima `Provenance()` decodes
  each value, skipping malformed ones.
  Keep it to ONE round trip; reuse the mux (ControlMaster) already configured.
- Choose the LimaHome value consistent with task 1 so the batched read and
  `limactl list` see the same directory.

**Decode.** A helper `decodeProvenance([]byte) (Provenance, bool)` used by both
single and batched reads; `false` on any error.

**Expose.** Implement the four methods on the type that backs `limaProvider`
(likely on `*lima.Client` or a wrapper the provider holds), then ensure
`limaProvider` forwards them. `remoteLimaProvider` embeds `*limaProvider`, so it
inherits them unchanged — add `var _ provider.Provenancer = (*limaProvider)(nil)`
(and for the remote type) to lock this at compile time.

**Tests.** Use `localFiles` against a temp dir (create `<tmp>/<name>/sandbar.json`)
for the local path, and a fake/stub `HostFiles` for batched decoding including a
deliberately malformed marker that must be skipped. Name tests so
`-run Provenance` selects them.

Do NOT wire consumers (board/manage/cli) here — that is tasks 4 and 5.
</details>
