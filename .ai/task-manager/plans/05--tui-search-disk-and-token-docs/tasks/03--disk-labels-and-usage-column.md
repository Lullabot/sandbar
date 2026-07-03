---
id: 3
group: "disk-usage"
dependencies: [2]
status: "pending"
created: "2026-07-03"
skills:
  - "go"
  - "bubbletea"
---
# Relabel the Disk column and add a real "Disk Used" column/field

## Objective
Stop the disk figure from misleading users and surface real consumption. Relabel the existing virtual/maximum disk size (item 2): the list header `Disk` becomes `Max Disk` and the detail field `Disk` becomes `Maximum Disk Size` (the displayed value is unchanged). Then add the actual usage (item 3): carry a new `DiskUsed` field on the shared `vm.VM` record, populate it once per list load from each VM's `Dir` using the `diskUsedBytes` helper (Task 2), and render it humanized in a new `Disk Used` list column and a `Disk Used (allocated)` detail field. An unmeasurable disk renders a **blank** cell, not `0 B`.

## Skills Required
- `go`: add a struct field, populate it in the message handler, and format via the existing humanizer.
- `bubbletea`: adjust the `bubbles/table` column layout and the detail render.

## Acceptance Criteria
- [ ] `vm.VM` (in `tui/internal/vm/vm.go`) gains a `DiskUsed string` field (raw-bytes string, empty when unknown) that round-trips through `humanizeBytes`.
- [ ] In `model.go`'s `vmsLoadedMsg` handler, after `m.vms = msg.vms`, each VM's `DiskUsed` is populated by calling `diskUsedBytes(v.Dir)`: a **positive** result becomes the raw-byte decimal string; a non-positive (`-1`/unknown) result leaves `DiskUsed` empty (`""`). Population happens here (once per load), **not** in `lima.Client.List()` and **not** in `refreshRows`.
- [ ] The list column header `Disk` is renamed to `Max Disk`, and a new `Disk Used` column is added (column order: Name, Status, CPUs, Memory, Max Disk, Disk Used, Managed), with widths tuned so the row still fits (the table clamps to terminal width on `WindowSizeMsg`).
- [ ] `refreshRows` emits the new `Disk Used` cell as `humanizeBytes(v.DiskUsed)`, which renders **blank** for an empty/unknown value (verified: `humanizeBytes("")` returns `""`).
- [ ] The detail view's `Disk` field label becomes `Maximum Disk Size`, and a new `Disk Used (allocated)` field renders `humanizeBytes(v.DiskUsed)`.
- [ ] The `Max Disk`/`Maximum Disk Size` **value** is unchanged from today (still `humanizeBytes(v.Disk)`, Lima's reported virtual/maximum size).
- [ ] `go build ./...` and `go vet ./...` pass in the `tui/` module.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Files touched: `tui/internal/vm/vm.go`, `tui/internal/ui/model.go`, `tui/internal/ui/list.go`, `tui/internal/ui/detail.go`.
- Uses the existing `humanizeBytes` (`tui/internal/ui/format.go`), which turns a raw-bytes string into a binary-unit size and returns an empty/already-formatted string unchanged.
- `model.go` will need to import `strconv` (for `strconv.FormatInt`) — it does not currently.
- Depends on `diskUsedBytes` from Task 2.

## Input Dependencies
- Task 2: `diskUsedBytes(dir string) int64`.

## Output Artifacts
- The `vm.VM.DiskUsed` field, the populated values, and the two relabelled + one new UI element — documented by Task 5 and exercised by Task 4.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**1. `tui/internal/vm/vm.go` — add the field.**

In the `VM` struct (around line 18-26), add `DiskUsed` after `Disk`:
```go
type VM struct {
	Name     string
	Status   string
	CPUs     int
	Memory   string
	Disk     string // virtual/maximum size Lima reports (qcow2 max)
	DiskUsed string // allocated on-disk bytes (raw string); "" = unknown/unmeasurable
	Dir      string
	Arch     string
}
```
Do **not** touch `lima.Client.List()` (`tui/internal/lima/client.go`) — `DiskUsed` is intentionally not populated there (that would force the `lima` package to import a build-tagged `ui` helper). It is populated in the `ui` layer.

**2. `tui/internal/ui/model.go` — populate on load.**

- Add `"strconv"` to the import block (currently only `"context"` plus the module/bubbles imports).
- In `Update`, in the `case vmsLoadedMsg:` branch (around line 159-175), after `m.vms = msg.vms` and before `m.refreshRows()`, add:
  ```go
  // Measure each VM's real disk consumption once per load (a single stat per
  // VM, microseconds, no file contents read). A non-positive result means the
  // disk couldn't be measured; leave DiskUsed empty so the cell renders blank.
  for i := range m.vms {
      if n := diskUsedBytes(m.vms[i].Dir); n > 0 {
          m.vms[i].DiskUsed = strconv.FormatInt(n, 10)
      }
  }
  ```
  (Range by index so the assignment mutates the slice element, not a copy.)

**3. `tui/internal/ui/list.go` — relabel + new column.**

- In `newTable()` (around line 19-34), change the columns. Replace `{Title: "Disk", Width: 14}` and insert the new column so the set reads:
  ```go
  cols := []table.Column{
      {Title: "Name", Width: 20},
      {Title: "Status", Width: 10},
      {Title: "CPUs", Width: 6},
      {Title: "Memory", Width: 12},
      {Title: "Max Disk", Width: 10},
      {Title: "Disk Used", Width: 10},
      {Title: "Managed", Width: 8},
  }
  ```
  (Trim `Memory`/`Max Disk`/`Disk Used` widths as shown so the extra column fits; exact widths can be tuned — the table already clamps to the terminal width on `WindowSizeMsg` at `model.go` ~line 141. `Name` must stay the first column because `SelectedRow()[0]` is used as the instance name.)
- In `refreshRows()` (around line 64-67), add the `Disk Used` cell between the `Disk` and `owner` cells:
  ```go
  rows = append(rows, table.Row{
      v.Name, v.Status, strconv.Itoa(v.CPUs),
      humanizeBytes(v.Memory), humanizeBytes(v.Disk), humanizeBytes(v.DiskUsed), owner,
  })
  ```
  `strconv` is already imported in `list.go`. `humanizeBytes(v.DiskUsed)` renders `""` (blank) when `DiskUsed` is empty.

**4. `tui/internal/ui/detail.go` — relabel + new field.**

In `detailView()` (the `fields` slice around line 37-46), change the `Disk` entry and add the `Disk Used (allocated)` entry:
```go
fields := [][2]string{
    {"Name", v.Name},
    {"Status", v.Status},
    {"CPUs", strconv.Itoa(v.CPUs)},
    {"Memory", humanizeBytes(v.Memory)},
    {"Maximum Disk Size", humanizeBytes(v.Disk)},
    {"Disk Used (allocated)", humanizeBytes(v.DiskUsed)},
    {"Arch", v.Arch},
    {"Dir", v.Dir},
    {"Managed", managed},
}
```

**Self-verify:** a VM with a 100 GiB virtual disk (`v.Disk` = "107374182400") whose `disk` file allocates ~5 GiB shows `Max Disk` = `100 GiB` and `Disk Used` = ~`5 GiB`; a VM whose `Dir` has no readable `disk` file shows a blank `Disk Used` cell (not `0 B`) and no error.
</details>
