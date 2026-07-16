---
id: 10
group: "profile-ui"
dependencies: [7]
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "medium"
complexity_score: 6
complexity_notes: "Layout budgeting for a variable number of status bands at 80 cols plus a per-tile label within a fixed budget; golden-test heavy but single-domain."
skills:
  - go
  - bubbletea
---
# Tile profile labels and a per-profile status bar with error banners

## Objective
Make *where* each VM runs visible per tile, and grow the status bar from one
host-capacity band to **one band per connected profile**, with **banner rows** for
disabled/errored profiles — degrading gracefully at the narrowest supported
terminal (80 columns). This is Component 5.

## Skills Required
- `go` — layout/width budgeting.
- `bubbletea`/`lipgloss` — tile and header rendering, golden tests.

## Acceptance Criteria
- [ ] Each **tile** gains a compact **profile label** identifying which profile it runs through, fitting within the tile's existing fixed content budget (no unpredictable growth of tile line counts).
- [ ] The **status bar/header** renders **one host-stats band per connected profile** (that profile's host cpu/mem/disk, sampled on that host off the UI goroutine, exactly as the single-host sample works today).
- [ ] A **disabled** or **errored** profile contributes a **banner row** (not a stats band) naming the profile and its state (disabled, or the connection error), so the user sees why its VMs are absent.
- [ ] Layout **budgeting** accommodates a variable number of bands without breaking the board at **80 columns**, with explicit degradation rules (e.g. compact/truncate bands) when the fleet is larger than the bar can fully show.
- [ ] Golden tests lock the rendering at 80×24 and a wide size, with **one, two, and several** profiles, including a disabled and an errored profile.
- [ ] `go test ./internal/ui/... -race` passes with updated/added goldens. **No real backend.**

## Technical Requirements
- Files: `internal/ui/header.go` (`hostCapacityText` ~129-202, `headerCounts` ~82), `internal/ui/tile.go` (tile content budget ~389-444), and the golden harness under `internal/ui/testdata/`.
- The per-profile host samples and statuses come from the fleet members (task 7).

## Input Dependencies
- Task 7: fleet members carry per-profile host samples + connection status + last error.

## Output Artifacts
- Per-tile provenance labels + a multi-band, banner-aware status bar.
- Consumed by: task 11 (tui.md docs), task 12 (integration goldens).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- **Tile label.** The tile already reserves fixed line counts (tile.go). Add the
  profile label within that budget — e.g. a short truncated profile name in an
  existing line, not a new line. Keep it stable so golden diffs are minimal.
- **Header bands.** `headerCounts`/`hostCapacityText` build one band today from
  precomputed `m.headerMem/headerDiskFree/headerCPUs` (sampled off-goroutine). Now
  iterate the fleet members: one band per **connected** member using that member's
  host sample; one banner row per **disabled/errored** member. Do not probe on the
  render goroutine — read the already-sampled values (task 7 keeps them per member).
- **80-col degradation.** Define and implement explicit rules: if N bands do not
  fit, compact (drop labels/units) or truncate, and if still too many, summarize
  (e.g. "+K more"). Lock every rule with a golden at 80×24. The board layout below
  must not break — verify tile grid still renders.
- **Goldens.** Add fixtures for 1/2/several profiles including disabled+errored.
  Use the existing `providerfake` + teatest harness to construct deterministic
  member states.
</details>
