---
id: 4
group: "pve-api-client"
dependencies: [1]
status: "completed"
created: 2026-07-20
model: "sonnet"
effort: "medium"
skills:
  - go
  - proxmox-api
---
# internal/pve: node and storage statistics endpoints

## Objective

Add typed methods for reading host-wide capacity from the Proxmox API — node CPU,
memory, and the free/total space of the storage backing sandbar's VMs — with
PVE's unit conventions and its known lying fields handled correctly.

## Skills Required

`go` (JSON decoding, pointer fields for absent values) and `proxmox-api` (node
status field semantics and units).

## Acceptance Criteria

- [ ] `go test ./internal/pve/ -race` passes with tests asserting:
      - `NodeStatus` decodes `cpuinfo.cpus`, `memory.total`, `memory.used`, and
        `memory.available` from a recorded fixture.
      - Memory headroom is computed as `memory.available`, **not**
        `total - used`, verified by a fixture where those differ.
      - `loadavg` decodes from an **array of strings** (not numbers) without error.
      - A node status response missing `disk`/`maxdisk` decodes without error
        (fields are pointers or zero-valued, never a decode failure).
      - `StorageStatus` decodes `total`, `used`, `avail`, `used_fraction`, and
        that a storage with `active: 0` omitting size fields decodes cleanly.
- [ ] `go vet ./internal/pve/` is clean.

## Technical Requirements

- `NodeStatus(ctx)` via `GET /nodes/{node}/status`.
- `StorageStatus(ctx, storage)` via `GET /nodes/{node}/storage/{storage}/status`. (Corrected during execution: the bare `/storage/{storage}` path returns storage *config*; the usage figures live under `/status`.)
- All byte sizes in these responses are **bytes** (no KiB anywhere).
- All `cpu` fields are a **fraction 0..1**, not a percentage.
- `uptime` is integer seconds.
- Every optional numeric must tolerate absence — decode into pointers or use
  `omitempty`-tolerant zero values, never fail the whole decode.

## Input Dependencies

Task 1's `Client` and `do` helper.

## Output Artifacts

`internal/pve/node.go` and its tests, plus JSON fixtures under
`internal/pve/testdata/` — consumed by task 7.

## Implementation Notes

<details>

```go
type NodeStatus struct {
    CPU     float64 `json:"cpu"`     // fraction 0..1, NOT percent
    Wait    float64 `json:"wait"`
    Uptime  int64   `json:"uptime"`  // seconds
    LoadAvg []string `json:"loadavg"` // ARRAY OF STRINGS, not numbers
    Memory  struct {
        Total     int64 `json:"total"`
        Used      int64 `json:"used"`
        Free      int64 `json:"free"`
        Available int64 `json:"available"`
    } `json:"memory"`
    RootFS struct {
        Total int64 `json:"total"`
        Used  int64 `json:"used"`
        Avail int64 `json:"avail"`
        Free  int64 `json:"free"`
    } `json:"rootfs"`
    CPUInfo struct {
        CPUs    int    `json:"cpus"`
        Sockets int    `json:"sockets"`
        Cores   int    `json:"cores"`
        Model   string `json:"model"`
    } `json:"cpuinfo"`
    PVEVersion string `json:"pveversion"`
}
```

Add these comments verbatim in spirit — they encode findings that are invisible
from the field names:

```go
// MemAvailable is the number to show as headroom. Proxmox computes
// memused = memtotal - memavailable, so Used + Free does NOT equal Total and
// Total - Used is not free memory. Available is the honest figure.

// Do NOT read an `idle` field from this endpoint: it is initialized to 0 and
// never overwritten upstream, so it is always 0 regardless of actual load.
```

```go
type StorageStatus struct {
    Total        int64   `json:"total"`
    Used         int64   `json:"used"`
    Avail        int64   `json:"avail"`
    UsedFraction float64 `json:"used_fraction"` // 0..1
    Active       int     `json:"active"`  // reachable right now
    Enabled      int     `json:"enabled"` // enabled in config
    Content      string  `json:"content"`
}
```

An enabled-but-unreachable storage (e.g. an NFS mount that is down) returns
`enabled: 1, active: 0` and **omits every size field**. Callers must treat that
as "unknown", not as zero free space — a false "0 bytes free" would trip the
low-disk warning. Provide `func (s StorageStatus) HasSizeReading() bool`
returning `s.Active == 1 && s.Total > 0`.

Also add a `Content` helper so task 6 can verify the configured storage actually
supports `images` before attempting to create a cloud-init drive, and produce a
clear error instead of a cryptic PVE failure.

Save two fixtures under `internal/pve/testdata/`: `node-status.json` and
`storage-status.json`, plus `storage-inactive.json` for the omitted-fields case.
Capture realistic shapes (including `loadavg` as strings and `mhz` as a string).

Deliberately **out of scope**: `GET /nodes/{node}/rrddata`. It is only needed for
historical graphs, its row format is sparse and ragged (undefined values are
omitted from rows entirely rather than emitted as null), and its schema was
renamed in PVE 9. The board header needs only current values.

</details>
