---
id: 10
group: "provider"
dependencies: [6]
status: "pending"
created: 2026-07-20
model: "sonnet"
effort: "medium"
skills:
  - go
  - proxmox-api
---
# provider: golden-template methods for PR #70 interface alignment

## Objective

Implement the template methods that PR #70 (golden VM templates) adds to the
`Provider` interface — `SnapshotTemplate`, `DeleteTemplate`, and
`TemplateDiskBytes` — for the Proxmox provider, isolated in one file so the
eventual merge is a small additive change.

## Skills Required

`go` and `proxmox-api` (clone and template-conversion semantics).

## Acceptance Criteria

- [ ] `go build ./internal/provider/` succeeds.
- [ ] `go test ./internal/provider/ -race` passes with mock-server tests
      asserting:
      - `SnapshotTemplate` from a **running** source stops it, clones, converts
        the clone to a template, and **restarts the source**.
      - `SnapshotTemplate` from a **stopped** source leaves it stopped.
      - A failure partway through deletes the partial clone and leaves the source
        in its original power state.
      - `TemplateDiskBytes` returns the volume size from storage content.
- [ ] All new code lives in `internal/provider/proxmoxtemplate.go`; no other file
      is modified except to add the methods to the interface if #70 has not yet
      landed.

## Technical Requirements

- Check whether PR #70 has landed on `main` first
  (`git log --oneline main -- internal/provider/provider.go | head`, and grep the
  interface for `TemplateDiskBytes`).
  - **If it has landed**: implement against the merged signatures exactly.
  - **If it has not**: implement the three methods on `*proxmoxProvider` with the
    signatures described below and do **not** add them to the `Provider`
    interface — leaving the interface untouched keeps this change additive and
    conflict-free.
- Preserve the source VM's power state across the snapshot, including on failure.

## Input Dependencies

Task 6 (base template build, clone helpers, cleanup pattern).

## Output Artifacts

`internal/provider/proxmoxtemplate.go` and tests.

## Implementation Notes

<details>

Expected signatures (verify against #70 if it has landed — the merged names win):

```go
SnapshotTemplate(ctx context.Context, source, templateName string, out io.Writer) error
DeleteTemplate(ctx context.Context, templateName string) error
TemplateDiskBytes(templateName string) int64
```

Proxmox maps onto this almost natively — a PVE template *is* the primitive
golden templates want, so there is no emulation layer here.

**`SnapshotTemplate`:**

1. Read the source's current status. Remember whether it was running.
2. If running, `StopVM` and wait on the UPID. (Cloning a running VM is possible
   but produces a crash-consistent disk; stopping first matches the behaviour
   #70 specifies.)
3. `CloneVM` with `full=1`, the target `pool` and `storage`, and the new name.
4. `ConvertToTemplate` on the clone.
5. If the source was running, `StartVM` it again — **including on the failure
   path**. Use `defer` so an early return still restores the power state:

```go
// Restore the source's power state no matter how we leave: a snapshot that
// silently leaves the user's VM stopped is worse than a failed snapshot.
if wasRunning {
    defer func() { _ = p.restart(context.WithoutCancel(ctx), source) }()
}
```

Note `context.WithoutCancel` — if the caller cancelled, the restart must still
run, or cancelling a snapshot leaves the VM off.

**Failure cleanup**: if the clone succeeded but conversion failed, delete the
partial clone (`DeleteVM` purge=1) before returning, following task 6's cleanup
pattern.

**`DeleteTemplate`**: `DeleteVM` with `purge=1` against the template's VMID.
Guard that the target actually *is* a template (`template: 1` in its config)
so a mis-typed name cannot destroy a live VM. This guard is the whole reason the
method is not just an alias for `Delete`.

**`TemplateDiskBytes`**: read the template's config for its boot disk volid, then
look that volid up in `StorageContent` for its `size`. Do **not** read `maxdisk`
from the status endpoint — it reports the boot disk's *provisioned* size only,
and QEMU `disk` is hardcoded to 0.

Return `-1` (or 0, matching whatever #70's convention turns out to be) when the
size cannot be determined, rather than guessing.

</details>
