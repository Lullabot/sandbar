---
id: 3
group: "sand-paste-image"
dependencies: [1]
status: "completed"
created: 2026-07-17
model: "sonnet"
effort: "medium"
skills:
  - go
complexity_score: 6
complexity_notes: "Reuses existing lima Shell plumbing but must get binary-over-stdin, absolute guest-home resolution, dir creation, and the remote hop right; both entrypoints depend on it."
---
# Guest Delivery + Paste Orchestration Core

## Objective
Provide the single reusable operation both entrypoints call: read the clipboard
image (task 1) and write it into the target guest's single-slot file
`<guest-home>/.sand/clip/latest.png` in one round trip, working for local and
remote-host VMs. Returns a typed result (staged / no-image / not-running) so
callers render a message without re-deriving it.

## Skills Required
- `go` — a small orchestration function over `internal/clipboard` and the
  existing `internal/lima` guest-exec plumbing.

## Acceptance Criteria
- [ ] A function (e.g. `PasteImage(ctx, prov, scope, vmName) (Result, error)` in a
      new small package or `internal/lima`) that: calls `clipboard.ReadImagePNG`;
      on `ErrNoImage` returns a distinct "no image" result WITHOUT touching the
      guest; otherwise writes the bytes to the guest single-slot path.
- [ ] The guest write is ONE round trip that creates the directory and file:
      `Client.Shell(ctx, name, <bytes>, out, "sh", "-c", "mkdir -p \"$1\" && cat > \"$2\"", "sand", dir, file)`.
- [ ] The destination uses the ABSOLUTE guest home from `lima.GuestHome(instanceDir)`
      (or the provider's equivalent) — never a literal `~`.
- [ ] Directory created `0700`, file `0600` (extend the shell script with `chmod`
      or `install`, mirroring `sshhost.go`'s `WriteFile`).
- [ ] Image bytes travel over **stdin**, never argv.
- [ ] `go test ./...` passes, including a unit test that records the guest argv
      and asserts: no image → zero guest calls; image present → exactly one guest
      write with the absolute path and stdin carrying the bytes (use the existing
      lima test seam that records argv without a real limactl).
- [ ] `go build ./...` succeeds.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Use `internal/lima` `Client.Shell(ctx, name, stdin, out, argv...)` (client.go)
  — it pipes stdin to the guest and works over the remote-host hop via `SSHHost`.
- Resolve the guest home the same way `internal/ui.guestHome` / `AttachArgv` do
  (`lima.GuestHome(instanceDir)`), reading Lima's cloud-config; do not
  reconstruct `/home/<user>.guest` by hand.
- If the provider interface does not already expose a guest-stdin-write or the
  instance dir needed for `GuestHome`, add a minimal method mirroring how `Copy`
  is exposed — do not invent a new transport.
- Follow `AGENTS.md`: no test may require a real `limactl`; use the argv-recording
  seam.

## Input Dependencies
- Task 1: `internal/clipboard.ReadImagePNG` / `ErrNoImage`.

## Output Artifacts
- The `PasteImage` orchestration entrypoint consumed by task 4 (CLI) and task 5
  (TUI). Defines the single-slot guest path contract (shared with task 2's shim).

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

- Model the guest write on `internal/lima/sshhost.go`'s `WriteFile`: one `sh -c`
  running `mkdir -p -- "$1"; chmod 700 "$1"; cat > "$2"; chmod 600 "$2"` with
  positional args `sand <dir> <file>` so paths never need shell-quoting, and the
  image piped in on stdin (`bytes.NewReader(png)`).
- `dir = guestHome + "/.sand/clip"`, `file = dir + "/latest.png"`.
- Result type: an enum/struct distinguishing `Staged`, `NoImage`, `NotRunning`
  (and carrying the vm name) so callers print e.g.
  `staged image on <vm> — press S then Ctrl-V` or `no image on clipboard`.
- Running-state check: reuse whatever `sand shell` uses to confirm `Running`
  before dispatch (`cmd/sand/shell.go` / provider Status), OR let the caller do
  the running guard and keep this function focused on read+write. Prefer the
  caller-guards approach to keep this core single-purpose; if so, drop the
  `NotRunning` result and document that the caller ensures Running.
- Keep clipboard reading host-local: `ReadImagePNG` runs on the machine sand runs
  on; only the returned bytes cross to the guest.
- Unit test via the lima argv-recording seam (see `sshhost_test.go` /
  `client` tests): inject a fake clipboard (`clipboard.run` or a small interface)
  returning `ErrNoImage` → assert no guest argv recorded; returning bytes →
  assert one `sh -c mkdir…cat` argv with the absolute file path and stdin bytes.
</details>
