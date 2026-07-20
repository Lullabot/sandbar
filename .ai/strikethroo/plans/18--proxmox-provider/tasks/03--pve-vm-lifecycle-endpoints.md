---
id: 3
group: "pve-api-client"
dependencies: [1]
status: "pending"
created: 2026-07-20
model: "sonnet"
effort: "high"
complexity_score: 8
complexity_notes: "Wide endpoint surface with several counter-intuitive semantics (async POST vs sync PUT config, conditional clone params, three disk unit conventions, guest-agent method/shape asymmetry) that must each be encoded correctly."
skills:
  - go
  - proxmox-api
---
# internal/pve: typed VM lifecycle and guest-agent endpoints

## Objective

Add typed methods to `internal/pve` covering the full VM CRUD lifecycle —
listing, create, clone, template conversion, config read/write, disk resize,
power control, delete, snapshots — plus the guest-agent calls needed for IP
discovery and readiness checks.

## Skills Required

`go` (typed API surface, option structs) and `proxmox-api` (endpoint semantics,
parameter formats, unit conventions).

## Acceptance Criteria

- [ ] `go test ./internal/pve/ -race` passes with new `httptest`-based tests
      asserting each of:
      - `CloneVM` with `Full: true` sends `storage`; with `Full: false` it sends
        **neither** `storage` nor `format` (a linked clone rejects both).
      - `ResizeDisk` sends an absolute size with an explicit unit suffix (never a
        bare number, which PVE reads as **bytes**).
      - `GetConfig` sends `current=1`.
      - `SetConfigSync` uses **PUT**; `SetConfigAsync` uses **POST** and returns a
        UPID.
      - `AgentPing` and `AgentFsfreezeStatus` use **POST**; `AgentNetworkGetInterfaces`
        and `AgentGetFsinfo` use **GET**.
      - `CreateVM` percent-encodes `sshkeys` with `%20` for spaces and `%0A` for
        newlines, and **never** `+`.
      - `NextID` collision handling: a create that fails with an "already exists"
        500 causes a fresh `NextID` call, not a local increment.
- [ ] `go vet ./internal/pve/` is clean and `go.mod` is still unchanged.

## Technical Requirements

- `ListVMs(ctx, pool string)` via `GET /cluster/resources?type=vm` filtered to
  the pool. **The `type` enum is `vm|storage|node|sdn`; `type=qemu` is invalid
  and returns 400.** Each item's own `type` field then reads `qemu` or `lxc` —
  keep only `qemu`.
- `NextID(ctx)` via `GET /cluster/nextid` (returns a bare integer). Never use
  `nextid?vmid=N` as an existence check — it 400s when taken.
- `CreateVM(ctx, opts)` via `POST /nodes/{node}/qemu` → UPID.
- `CloneVM(ctx, vmid, opts)` via `POST .../clone` → UPID.
- `ConvertToTemplate(ctx, vmid)` via `POST .../template` → UPID.
- `GetConfig(ctx, vmid)` via `GET .../config?current=1`.
- `SetConfigSync` (PUT, returns nothing) and `SetConfigAsync` (POST, returns a
  UPID) via `.../config`.
- `ResizeDisk(ctx, vmid, disk, sizeBytes)` via `PUT .../resize` → UPID.
- `RegenerateCloudInit(ctx, vmid)` via `PUT .../cloudinit`.
- Power: `StartVM`, `StopVM`, `ShutdownVM`, `RebootVM` via `POST .../status/*`
  → UPID. `GetStatus(ctx, vmid)` via `GET .../status/current`.
- `DeleteVM(ctx, vmid, purge bool)` via `DELETE .../{vmid}` → UPID.
- Snapshots: `ListSnapshots`, `CreateSnapshot`, `DeleteSnapshot`.
- Agent: `AgentPing`, `AgentNetworkGetInterfaces`, `AgentGetFsinfo`,
  `AgentExec`, `AgentExecStatus`.
- `DownloadURL(ctx, storage, opts)` via
  `POST /nodes/{node}/storage/{storage}/download-url` → UPID.
- `StorageContent(ctx, storage)` via `GET .../storage/{storage}/content`.

## Input Dependencies

Task 1's `Client`, `do` helper, `APIError`, and `WaitTask`.

## Output Artifacts

`internal/pve/vm.go`, `internal/pve/agent.go`, `internal/pve/storage.go` and
tests — consumed by tasks 5, 6, 8, and 11.

## Implementation Notes

<details>

**Create options.** Model as a struct with a `formValues()` method:

```go
type CreateVMOptions struct {
    VMID    int
    Name    string
    Cores   int
    Memory  int    // MiB
    DiskGB  int
    Storage string
    Bridge  string
    Pool    string
    SSHKeys []string
    CIUser  string
    ImportFrom string // optional storage volid, e.g. "local:import/debian-13.qcow2"
}
```

Emit these form values — the non-obvious defaults are called out because PVE's
own defaults are wrong for cloud images:

- `scsihw=virtio-scsi-pci` — **PVE defaults to `lsi`**, which cloud images do not
  drive.
- `scsi0` = `"<storage>:<DiskGB>"` (a bare number here means **GiB**), or
  `"<storage>:0,import-from=<volid>"` when importing (the `:0` is enforced).
- `ide2=<storage>:cloudinit` — requires a storage with `images` content. **The
  built-in `local` storage has no `images` content by default**; surface a clear
  error if the configured storage lacks it.
- `net0=virtio,bridge=<bridge>` — **omitting `bridge` silently gives QEMU
  user-mode NAT, not "no network"**, which would make the VM unreachable over
  SSH in a way that looks like a boot failure.
- `boot=order=scsi0` (the bare/`legacy=` form is deprecated).
- `agent=1`, `ostype=l26`, `serial0=socket`, `vga=serial0`.
- `ipconfig0=ip=dhcp`. Note `ip=auto` is **rejected** (it is IPv6 SLAAC only) and
  `gw` may not be combined with `ip=dhcp`.
- `pool=<pool>` — **this is what makes the new VM a pool member**, and therefore
  what makes every later permission check succeed under a pool-scoped token.
  It must never be omitted.

**`sshkeys` encoding.** The server does `URI::Escape::uri_unescape` on this
value. Encode as:

```go
func encodeSSHKeys(keys []string) string {
    joined := strings.Join(keys, "\n")
    return strings.ReplaceAll(url.QueryEscape(joined), "+", "%20")
}
```

Because the body is form-urlencoded, `url.Values.Encode()` will escape this a
second time and the server's transport decodes once — two encodes, one decode.
**Do not try to compensate for that**; the above is what both mature Go clients
converged on. Getting it backwards puts a literal `ssh-rsa%20AAAA...` into the
guest's `authorized_keys`.

**Clone.** Build params conditionally:

```go
form := url.Values{"newid": {...}, "name": {...}, "pool": {...}}
if opts.Full {
    form.Set("full", "1")
    if opts.Storage != "" { form.Set("storage", opts.Storage) }
} // linked clone: storage and format are HARD ERRORS, so send neither
```

Note PVE's own default for `full` is `!is_template(source)` — do not rely on it;
always be explicit.

**Config.** `GET .../config` returns *pending* values by default; always send
`current=1` to read real state. `POST` is async (UPID) and `PUT` is sync — this
is backwards from intuition and is deliberate upstream ("almost any
configuration change can involve hot-plug actions"). Prefer `PUT` for simple
metadata writes. Carry the `digest` field back on mutating calls for optimistic
concurrency where convenient.

**Cloud-init regeneration.** A config write alone does **not** regenerate the
cloud-init image. Only VM start, `PUT .../cloudinit`, or hotplug do — and
cloudinit is not in the default hotplug set. Provide `RegenerateCloudInit` and
document that callers must invoke it (or stop/start) after any cloud-init write.

**Resize.** Normalize to bytes internally but emit an explicit unit:

```go
// PVE reads a BARE number as BYTES. "20G" is absolute; "+10G" is relative.
// Shrinking is a hard error; an equal size is a silent successful no-op, which
// makes this safely idempotent.
form.Set("size", fmt.Sprintf("%dG", sizeBytes/(1<<30)))
```

**Agent.** Methods differ per command and are not guessable: `ping` and
`fsfreeze-status` are **POST**; `network-get-interfaces`, `get-osinfo`,
`get-fsinfo` are **GET**. Responses nest twice: `{"data":{"result": ...}}`.

All four agent failure conditions return **HTTP 500** and are distinguishable
only by message text. Provide a classifier:

```go
// AgentUnavailableReason distinguishes a permanent misconfiguration from a
// transient not-yet-booted state. Note PVE checks `!defined($conf->{agent})`
// first, so a VM configured with `agent: 0` reports "not running" rather than
// "not configured" — read the config to tell those apart.
func AgentUnavailableReason(err error) string // "not-configured" | "vm-stopped" | "agent-down" | "timeout" | ""
```

`network-get-interfaces` returns hyphenated keys (`ip-address`,
`ip-address-type`, `hardware-address`). Define the structs accordingly.

**VMID collision.** `/cluster/nextid` takes no lock and reserves nothing; the
create call is the atomic operation. On a create that fails with a 500 matching
`already exists|config file already exists`, **re-ask `NextID` fresh and retry**
(bounded, ~5 attempts). Never increment locally — linear probing stalls for
seconds across occupied ID ranges.

**Clone serialization.** Parallel clones from one template serialize on the
template's server-side flock (10s timeout, and there is no unlock endpoint
reachable from a token identity). Expose the client method plainly; task 6 owns
the client-side serialization.

Tests use `httptest` and assert on the received method, path, and parsed form
values. Focus on the custom encoding/conditional logic above — do not test
`net/http` or `url.Values` themselves.

</details>
