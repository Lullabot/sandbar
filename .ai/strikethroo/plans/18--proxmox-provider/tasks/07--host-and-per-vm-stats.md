---
id: 7
group: "stats"
dependencies: [4, 5]
status: "pending"
created: 2026-07-20
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Must distinguish 'no reading' from 'zero' throughout, or the UI's low-capacity warnings fire falsely; also has to work around PVE hardcoding QEMU disk usage to 0."
skills:
  - go
  - tui-integration
---
# stats: HostResources from the Proxmox API and honest per-VM disk usage

## Objective

Populate `provider.HostResources` for the Proxmox provider from the node and
storage endpoints, and supply per-VM disk usage — working around the fact that
PVE reports QEMU disk usage as a hardcoded `0` and that the UI's normal per-VM
sampling path has no Proxmox analogue.

## Skills Required

`go` and `tui-integration` (how `internal/ui` consumes `HostResources`,
`vm.VM.DiskUsed`, and the "0 means unknown" convention).

## Acceptance Criteria

- [ ] `go test ./internal/provider/ -race` passes with tests asserting:
      - `HostResources()` returns CPU count from `cpuinfo.cpus`, `MemBytes` from
        `memory.total`, and disk free/total from the **configured VM storage**
        (not the node's rootfs).
      - When the configured storage is inactive (size fields omitted),
        `DiskFreeBytes` and `DiskTotalBytes` are **0** ("unknown"), not a
        computed zero.
      - A node the token cannot `Sys.Audit` yields zeroed stats rather than an
        error that breaks the whole refresh.
- [ ] `go test ./internal/ui/ -race` still passes — confirm the header renders
      without the capacity clause when the values are 0, and that no low-disk
      warning fires on a zero reading.
- [ ] `vm.VM.DiskUsed` is populated for Proxmox VMs; verify with a mock-server
      test that a listed VM carries a non-zero `DiskUsed` when storage content
      reports a volume size.

## Technical Requirements

- Implement `HostResources()` on `proxmoxProvider` using task 4's `NodeStatus`
  and `StorageStatus`.
- **Any value the API does not supply must be left `0`.** The header already
  treats 0 as "unknown" and drops the clause, and the low-capacity warning
  refuses to compute a percentage from it — a fabricated zero would trigger a
  false "less than 5% disk free" warning.
- Disk figures come from the storage backing the VMs, which is the meaningful
  denominator for "how much room is left for sandboxes", **not**
  `rootfs` (the node's own root filesystem).
- Populate `vm.VM.DiskUsed` inside `List`/`Get` from storage content volume
  sizes.
- Verify that the UI's `HostFiles`-based per-VM sampling degrades gracefully when
  `DiskAllocBytes` returns `-1` (task 5) — it must show "no reading", never 0.
  If it currently does not, fix the UI to distinguish the two.

## Input Dependencies

Task 4 (`NodeStatus`, `StorageStatus`), task 5 (`proxmoxProvider`, `List`).

## Output Artifacts

`HostResources` implementation and per-VM disk population in
`internal/provider/proxmox.go`; any needed `internal/ui` guard — consumed by
task 12's stats verification.

## Implementation Notes

<details>

```go
func (p *proxmoxProvider) HostResources() provider.HostResources {
    // Every field is left 0 ("unknown") when the API does not supply it. The
    // header drops the clause and the low-capacity warning refuses to compute a
    // percentage — both of which are correct. Substituting a real-looking zero
    // would instead read as "0 bytes free" and fire a false warning.
    var hr provider.HostResources
    ns, err := p.client.NodeStatus(ctx)
    if err == nil {
        hr.CPUs = ns.CPUInfo.CPUs
        hr.MemBytes = ns.Memory.Total
    }
    ss, err := p.client.StorageStatus(ctx, p.storage)
    if err == nil && ss.HasSizeReading() {
        hr.DiskFreeBytes = ss.Avail
        hr.DiskTotalBytes = ss.Total
    }
    return hr
}
```

Note the interface's signature has no `error` return, so a failed sample must
degrade to zeros rather than propagate — that is the existing contract, not a
shortcut. Do not log on every call (this runs on the TUI refresh timer); log at
most once per transition into failure if the codebase has a pattern for that,
otherwise stay silent.

**Do not** use the node's `mem`/`maxmem` from `/nodes`, and do not compute
headroom as `total - used`: PVE defines `memused = memtotal - memavailable`, so
`used + free != total`. If you later want a "memory available" figure, it is
`ns.Memory.Available`.

**Per-VM disk.** `GET .../status/current` for a QEMU guest hardcodes
`disk` to `0` — upstream literally writes `$d->{disk} = 0; # no info available`
— and `maxdisk` is the **boot disk size only**, not the sum of disks and not
actual thin allocation. So:

```go
// PVE reports QEMU disk usage as a hardcoded 0, so DiskUsed comes from the
// storage's own content listing (the volume's allocated size) rather than from
// the VM status endpoint. Reading status/current's `disk` would silently report
// every VM as using nothing.
```

Fetch `StorageContent` once per `List` call and index it by volid, so a fleet
listing stays a small constant number of round trips rather than one per VM.

Also beware: the `mem` field on a VM silently changes meaning — it is host
cgroup RSS by default, but becomes the guest's own `total - free` once the
balloon driver reports. If you surface per-VM memory at all, prefer PVE 9's
`memhost` (always host cgroup RSS) and treat its absence as "unknown".

**UI check.** Read `internal/ui/diskusage.go` and `internal/ui/tile.go`. Confirm
that a `-1` from `DiskAllocBytes` renders as an absent reading. The heartbeat's
`guestSample` already distinguishes absent from zero via `HasMem()`/`HasDisk()`;
follow that same pattern if a guard is missing on the host-side path.

</details>
