---
id: 2
group: "disk-usage"
dependencies: []
status: "pending"
created: "2026-07-03"
skills:
  - "go"
---
# Build-tagged disk-usage helper (allocated blocks of `<dir>/disk`)

## Objective
Add a small cross-platform helper to the `ui` package that, given a Lima instance directory, returns the **allocated** on-disk size (`st_blocks × 512`) of that instance's single qcow2 image file named `disk`. This is the honest "blocks consumed" figure — a qcow2 only allocates the blocks it actually holds, so a 100 GiB-virtual disk holding ~5 GiB reports ~5 GiB. The helper follows the repository's existing `hostres` build-tag split so it adds no new dependency and degrades gracefully on unsupported platforms, returning a negative sentinel when the size cannot be measured.

## Skills Required
- `go`: build-tagged files (`//go:build`), `golang.org/x/sys/unix` `Stat`, and `unix.Stat_t.Blocks`.

## Acceptance Criteria
- [ ] A new file `tui/internal/ui/diskusage_unix.go` with build constraint `//go:build linux || darwin` defines `func diskUsedBytes(dir string) int64`.
- [ ] `diskUsedBytes` stats **only** `<dir>/disk` (Lima 2.x layout) via `golang.org/x/sys/unix` and returns `int64(st.Blocks) * 512`.
- [ ] It returns a negative sentinel (`-1`) when `dir` is empty, when `<dir>/disk` is missing, or when the stat call fails — it must not return `0` for an unmeasurable disk (so the UI can render a blank cell rather than `0 B`).
- [ ] A new file `tui/internal/ui/diskusage_other.go` with build constraint `//go:build !linux && !darwin` defines a fallback `func diskUsedBytes(string) int64 { return -1 }`.
- [ ] No fallback is added for the legacy Lima `diffdisk`/`basedisk` layout, and no file other than `<dir>/disk` (e.g. `cidata.iso`, logs) is measured — per the plan's YAGNI decision.
- [ ] `go build ./...` and `go vet ./...` pass in the `tui/` module (both the unix and the non-unix build paths must compile).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Package: `github.com/deviantintegral/claude-code-ansible/tui/internal/ui`.
- Dependency: `golang.org/x/sys/unix` (already vendored and used by `hostres_unix.go`) — do **not** add any new dependency.
- Mirror the existing build-tag pattern in `tui/internal/ui/hostres_unix.go` (`//go:build linux || darwin`) and `tui/internal/ui/hostres_other.go` (`//go:build !linux && !darwin`).
- `st_blocks` is reported in 512-byte units on both Linux and macOS/APFS (the POSIX convention), so multiply by the literal `512`, not the filesystem block size.

## Input Dependencies
None. This is a standalone helper with no dependency on other tasks.

## Output Artifacts
- `diskUsedBytes(dir string) int64` — consumed by Task 3 to populate the `vm.VM.DiskUsed` field on list load, and exercised by Task 4's unit tests.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Create `tui/internal/ui/diskusage_unix.go`:**
```go
//go:build linux || darwin

package ui

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

// diskUsedBytes returns the allocated on-disk size (st_blocks × 512) of the Lima
// instance's qcow2 image at <dir>/disk, or -1 when it can't be measured (empty
// dir, missing/unreadable disk file). Allocated blocks — not logical length — is
// the honest "blocks consumed" figure: a qcow2 only allocates the blocks it
// holds, so a 100 GiB-virtual disk holding ~5 GiB reports ~5 GiB. On CoW
// filesystems (APFS, Btrfs/XFS reflinks) it additionally reflects blocks shared
// with a clone source. Returns -1 (not 0) so an unmeasurable VM renders a blank
// cell rather than "0 B". Lima 2.x writes a single file named `disk` per
// instance; no fallback for the legacy diffdisk/basedisk layout is provided.
func diskUsedBytes(dir string) int64 {
	if dir == "" {
		return -1
	}
	var st unix.Stat_t
	if err := unix.Stat(filepath.Join(dir, "disk"), &st); err != nil {
		return -1
	}
	return int64(st.Blocks) * 512
}
```
Note: `unix.Stat_t.Blocks` is `int64` on both linux and darwin, but wrapping in `int64(...)` keeps it portable across arches — keep the conversion.

**Create `tui/internal/ui/diskusage_other.go`:**
```go
//go:build !linux && !darwin

package ui

// diskUsedBytes has no allocated-block probe on platforms without unix.Stat;
// return -1 so the "Disk Used" cell renders blank there.
func diskUsedBytes(string) int64 { return -1 }
```

**Why -1, not 0:** downstream (Task 3) converts a positive result to a raw-bytes string for `humanizeBytes`, and leaves the field empty (blank cell) for any non-positive result. A genuinely tiny disk still reports a small positive value; only an unmeasurable one yields `-1`. This is what makes "cannot measure" visibly distinct from "almost empty".

**Do not** import or modify `hostres_*.go`; just place these two new files alongside them so the build tags select the right one per platform.
</details>
