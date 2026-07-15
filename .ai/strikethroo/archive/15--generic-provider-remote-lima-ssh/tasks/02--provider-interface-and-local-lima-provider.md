---
id: 2
group: "foundation"
dependencies: [1]
status: "completed"
created: 2026-07-15
model: "opus"
effort: "high"
skills:
  - go
  - interface-design
complexity_score: 8
complexity_notes: "Architecture-defining: the interface every consumer will depend on and the boundary that lets future non-Lima backends plug in. Must faithfully envelope today's usage without leaking Lima specifics."
---
# Define the `provider.Provider` interface and the local Lima provider

## Objective
Create `internal/provider` with a `Provider` interface that owns the full VM
lifecycle plus the guest transport and interactive attach, and implement a
**local Lima provider** that satisfies it by composing the seam-backed lima core
(task 1) and the existing `provision.Provisioner`. This task adds the interface
and its first implementation **alongside** the current code; consumer migration
is task 3. Behaviour must be identical to today when the local provider is used.

## Skills Required
`go`, `interface-design` (designing a minimal, faithful interface envelope over existing usage).

## Acceptance Criteria
- [ ] `internal/provider` defines a `Provider` interface grouping: discovery (list / get / status), power (start / stop / delete + streaming variants), provisioning lifecycle (create / reset / recreate), guest transport (exec-merged / exec-stdout-only / exec-with-captured-stderr / copy), and interactive attach (produce the argv to exec), plus preflight and a guest-path helper. Every method corresponds to a current `*lima.Client` method, free `provision` function, or `lima` helper — no speculative methods.
- [ ] A local Lima provider implements `Provider`, delegating discovery/power/transport to the seam-backed lima core and create/reset/recreate to `provision.Provisioner`. `AttachArgv`/`GuestHome`/`GuestUser`/`GuestPath` are produced *by the provider* so callers never hand-build a Lima-shaped path or command.
- [ ] A compile-time assertion (`var _ provider.Provider = ...`) proves the local provider satisfies the interface.
- [ ] **Verification**: `go build ./... && go vet ./...` clean; a new unit test constructs the local provider over a fake host-access seam and asserts List / Status / Start / Exec produce the expected `limactl` argv (reusing the existing fake-runner style), passing under `go test ./internal/provider/ -race`.

## Technical Requirements
- Method signatures should carry `context.Context` and `io.Writer` where the current methods do, so streaming and cancellation semantics survive.
- The interface must not expose Lima-only types; use `vm.VM`, strings, and `io` types. `vm.VM.Dir` may remain (documented as a provider-opaque instance dir).
- Do not modify consumers in this task.

## Input Dependencies
- Task 1: the host-access seam and its local implementation.

## Output Artifacts
- `provider.Provider` interface (consumed by tasks 3, 4, 5, 6).
- Local Lima provider implementation (the default backend).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

Derive the interface from the concrete surface the map already enumerated:
`List/Get/Status/Start/Stop/Delete/Clone/Configure/Create/CreateStreaming/
CloneStreaming/StartStreaming/StopStreaming/Shell/ShellStreamOut/ShellOut/Copy/
Preflight` on `lima.Client`, plus the free `provision` functions
(`ApplySecrets`, `StageOut`, `StageIn`, `applyGitCredEntries`, `guestHome`) and
the `Provisioner` lifecycle (`CreateVMWithOptions`, `RecreateWithOptions`,
`Reset`). Group them; do not expose internal-only helpers like `Clone`/
`Configure` on the top interface unless a consumer needs them (the provisioner
uses them internally via the lima core, not via `Provider`).

Structure the local provider as: `type limaProvider struct { core *lima.Client; prov *provision.Provisioner }` (names illustrative). `core` is built over the
task-1 seam. `Provider.Create` → `prov.CreateVMWithOptions`; `Provider.Reset` →
`prov.Reset`; `Provider.List` → `core.List`; `Provider.Exec` → `core.Shell`;
etc. The provisioner keeps depending on the lima core (not on `Provider`), so
there is no import cycle: consumers → `Provider` → { lima core, provisioner };
provisioner → lima core.

`AttachArgv` currently lives in `internal/lima/attach.go` and returns a literal
`limactl shell …` argv; expose it through the provider (the remote provider will
return an `ssh -t …` form in task 5). Keep the guest tmux expression
(`guestAttachExpr`) byte-for-byte identical and its tests intact.

Preserve every invariant-documenting comment as code moves. Do NOT migrate
consumers here — `cmd/sand`, `internal/ui`, `internal/browse` still hold
`*lima.Client` after this task; task 3 flips them. Keeping this task additive is
what keeps the suite green and the coverage floor intact between tasks.
</details>
