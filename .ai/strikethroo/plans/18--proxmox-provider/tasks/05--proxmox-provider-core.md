---
id: 5
group: "provider"
dependencies: [1, 2, 3]
status: "pending"
created: 2026-07-20
model: "opus"
effort: "xhigh"
complexity_score: 9
complexity_notes: "The architecture-defining task: it decides how a non-Lima backend satisfies a Lima-shaped interface, including the two known-awkward seams (HostFiles, per-VM disk). Every later provider task builds on the shape chosen here."
skills:
  - go
  - provider-architecture
---
# provider: proxmoxProvider core — discovery, power, guest transport, HostFiles

## Objective

Implement the `provider.Provider` interface for Proxmox: discovery, power
control, guest-IP resolution via the guest agent, the SSH-based guest transport,
attach argv and guest paths, host identity, preflight, and the `HostFiles`
handle. Provisioning (`Create`/`Recreate`/`Reset`) is deliberately left to
task 6; this task may stub those three methods with a clear
"not yet implemented" error so the type compiles against the interface.

## Skills Required

`go` and `provider-architecture` (satisfying an existing interface without
changing it; deciding how Lima-shaped concepts map onto a different backend).

## Acceptance Criteria

- [ ] `var _ provider.Provider = (*proxmoxProvider)(nil)` compiles — verified by
      `go build ./internal/provider/`.
- [ ] `go test ./internal/provider/ -race` passes with new tests against an
      `httptest` mock PVE server asserting:
      - `List` returns only `qemu` guests belonging to the configured pool.
      - `Get` issues a **single-VM** request and does **not** call
        `/cluster/resources` (assert the mock never sees that path).
      - Guest IP resolution picks the interface whose `hardware-address` matches
        `net0`'s MAC, skips `lo`, skips `fe80::/10` link-local, and tolerates an
        interface with **no `ip-addresses` key at all**.
      - A 403 from the agent endpoint aborts the readiness wait immediately
        rather than retrying until timeout.
- [ ] `HostFiles()` returns a non-nil implementation; `DiskAllocBytes` returns
      `-1` and `LimaHome()` returns the per-endpoint state directory.

## Technical Requirements

- New file `internal/provider/proxmox.go`, plus `proxmoxfiles.go`.
- `ProxmoxProviderID = "proxmox"` constant; extend `TargetConfig` with the
  Proxmox fields and extend `TargetConfig.Scope()` so a Proxmox target yields
  `registry.Scope{Provider: ProxmoxProviderID, RemoteTarget: "<host>:<node>/<pool>"}`
  — matching `profiles.proxmoxTarget()` exactly. Both existing formats carry
  "keep these in agreement" comments; add the same note.
- `NewProxmox(cfg TargetConfig) (Provider, error)` must **not** perform any
  network round trip (`BuildFleet` relies on cheap construction); all reachability
  checks belong in `Preflight`.
- Guest transport reuses SSH. Resolve the VM's IP, then run SSH with the
  configured identity. Reuse the existing `internal/lima` SSH command-building
  helpers where they are already exported; if they are not, extract the minimum
  rather than duplicating quoting logic.
- `Preflight` must verify: the API is reachable, the token authenticates, the
  configured pool exists, the configured storage exists and supports `images`
  content, and the PVE version is ≥ 8.1. Each failure must name the specific
  cause.

## Input Dependencies

Tasks 1 and 3 (`internal/pve` client and VM endpoints); task 2 (profile fields
and `LoadToken`).

## Output Artifacts

`internal/provider/proxmox.go`, `internal/provider/proxmoxfiles.go`, tests —
consumed by tasks 6, 7, 8, 9, 10, 11, 12.

## Implementation Notes

<details>

**Struct.** Hold the `*pve.Client`, the resolved pool/node/storage/bridge, the
SSH identity path, the state dir, and a mutex-guarded `map[string]string` IP
cache.

**`vm.VM.Dir`.** The interface treats this as a **provider-opaque instance
handle**. Use the per-VM state directory under this provider's state root and
keep the VMID in a separate field of your internal record. Never let a consumer
build a path from it.

**Mapping `List`.** `pve.ListVMs(ctx, pool)` → `[]vm.VM`. Map PVE `status`
(`running`/`stopped`) onto the strings the UI already expects — check what
`internal/lima` returns (`"Running"`, `"Stopped"`) and match it exactly, or the
status bands will not colour correctly.

**`Get` must not scan `List`.** The interface documents this explicitly. Use
`GET .../status/current` for the single VM and return `lima.ErrNoSuchInstance`
when PVE reports 404 — consumers already branch on that sentinel, so returning a
different error silently breaks them. Reusing the `lima.` sentinel from a
non-Lima provider is intentional: it is the interface's shared vocabulary, not a
Lima implementation detail.

**Guest IP discovery** — the single most failure-prone piece:

```go
// guestIP resolves v's reachable address from the guest agent. It matches the
// interface by its MAC against net0 rather than by name: interface naming is
// not stable across images, `lo` is ALWAYS present, IPv6 link-local fe80::/10
// is always present, and an interface that is up but unaddressed omits the
// ip-addresses key ENTIRELY rather than returning an empty array.
```

Read `net0` from `GetConfig` to extract the MAC (format
`virtio=AA:BB:CC:DD:EE:FF,bridge=vmbr0`). Compare case-insensitively. Filter
candidate addresses with `netip.Addr.IsGlobalUnicast()` and reject
`IsLinkLocalUnicast()`. Cache the result; invalidate on any power transition.

**Agent readiness wait.** Poll `AgentPing` with a timeout. Use
`pve.AgentUnavailableReason` from task 3:
- `"not-configured"` → **permanent**, fail immediately with a clear message.
- `pve.IsPermission(err)` → **permanent**, fail immediately. This is the
  canonical failure mode in comparable tools: the readiness predicate swallowed a
  403 as "not ready yet" and the operation hung forever instead of reporting a
  permission problem. Do not repeat it.
- `"vm-stopped"`, `"agent-down"`, `"timeout"` → transient, keep polling.

**`HostFiles()` — the awkward seam, resolved.** Proxmox has no "host where
limactl runs". Implement `proxmoxFiles` as a thin local-filesystem type rooted at
a per-endpoint state directory:

```go
// proxmoxFiles satisfies lima.HostFiles for a backend that has no Lima host.
// Sandbar's own state ABOUT an instance (version stamps, locks, provenance
// fallbacks) is kept locally, per endpoint, because there is no shared
// filesystem to put it on; Proxmox itself owns the VM state and is reached
// through the API instead.
type proxmoxFiles struct{ root string }
```

- `LimaHome()` → the state root, e.g.
  `${XDG_STATE_HOME:-~/.local/state}/sandbar/proxmox/<host>-<node>-<pool>/`.
  Sanitize the components into a filesystem-safe directory name.
- `ReadFile`/`Stat`/`WriteFile`/`MkdirAll`/`RemoveAll`/`OpenLock` → delegate to
  the existing local implementation (`lima.LocalFiles()`) if it can be rooted, or
  wrap `os.*` directly. Missing files must satisfy `errors.Is(err, fs.ErrNotExist)`.
- `StagePlaybook(ctx, localDir)` → return `localDir` **unchanged**. The playbook
  reaches the guest over SSH during provisioning, not via a bind mount, so there
  is nothing to stage.
- `DiskAllocBytes(path)` → return `-1`. This is already the interface's
  documented "cannot be measured" answer, and it is honest here: there is no
  local qcow2 to stat. Task 7 supplies real per-VM disk figures instead.
- `ReadInstanceMarkers` → return an empty map; task 8 implements provenance
  natively via PVE tags, so the marker path is never the source of truth.

**`HostUser()`** returns the guest login user the playbook provisions — the
cloud-init `ciuser`, which the provider configures. Return it from config rather
than the local machine's user; the interface's doc is explicit that returning the
laptop's user leaves the guest login account unprovisioned.

**`AttachArgv(v)`** returns an `ssh -t` wrapper around the same tmux guest
expression the remote provider builds — check `internal/lima/sshhost.go`'s
`AttachArgv` and mirror its shape so tmux behaviour is identical.

**`GuestPath(name, path)`** — for an SSH transport this is `<user>@<ip>:<path>`.

Stub `Create`, `Recreate`, and `Reset` with
`errors.New("proxmox: provisioning not yet implemented")` so the interface is
satisfied; task 6 replaces them.

</details>
