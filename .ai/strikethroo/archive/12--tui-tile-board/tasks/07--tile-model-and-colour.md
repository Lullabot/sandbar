---
id: 7
group: "board"
dependencies: [3, 4, 5]
status: "completed"
created: 2026-07-12
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "The rendering is straightforward; the rules are not. Derived status (job registry ahead of Lima), a genuine fleet-uniformity test for exception-only fields, honest absence of gauges, and a last-used mtime probe — each is a place where a plausible shortcut produces a dishonest tile."
skills:
  - go
  - terminal-ui
---
# The tile model and the colour system

## Objective

Define what a tile shows and how it is coloured, such that **every line carries live signal and
nothing on it is a lie**.

A tile is a bordered card whose content is **derived, not fixed**: a title (the VM name), a status
line (coloured glyph + status word), a cpu gauge and a mem gauge **only when the VM is running and a
heartbeat sample exists**, a disk gauge (always — this is real data today), and a closing line
reading `up 2h14m` when running or `last used 3d ago` when stopped. A **building** tile replaces its
gauges with an in-place progress bar and the current Ansible role and task count.

## Skills Required

`go`, `terminal-ui` — lipgloss v2 card rendering, gauges, and a colour system that degrades to
monochrome.

## Acceptance Criteria

- [x] A new `internal/ui/tile.go` renders one tile from: a `vm.VM`, the job registry's snapshot for
      that VM (task 04), the heartbeat's latest sample if any (task 05), the fleet-uniformity result,
      and the layout mode's tile budget (task 03).
- [x] **Status is derived, job registry first, Lima as fallback.** A VM with a live provision job
      renders **Building** (animated, with its progress bar); a VM whose last job **failed** renders
      **Failed** (red, sticky, until the user acts); otherwise Lima's `Running` / `Stopped`.
      `grep -n "vm.Status" internal/ui/tile.go` must show it is only ever reached through the derived
      function — **never rendered directly**. Rendering `vm.Status` directly would mean a **failed
      provision leaves a reassuring green "Running" tile**, which is the plan's top-billed risk.
      Test each of the four derivations.
- [x] **Honest absence.** A **stopped** VM renders **no cpu and no mem gauge at all** — not a zeroed
      bar, not a greyed bar: **absent**. The gauge's presence is itself information. Test it: assert
      the rendered stopped tile contains neither "cpu" nor "mem".
- [x] **Exception-only fields, implemented as a genuine fleet-uniformity test** — not as two
      hardcoded deletions. A field whose value is **uniform across the whole fleet** is hidden; it
      surfaces automatically, as a badge, on the tiles that differ, the moment it varies.
      **Test the rule, not its current consequences**: a fleet where every VM shares an architecture
      hides the arch field; a fleet with one `aarch64` VM among `x86_64` ones **shows** it on the
      differing tile. Same for base image. Do not write `if field == "arch" { skip }`.
- [x] **`last used` for stopped VMs**, sourced from the **mtime of `~/.lima/<name>/ha.stderr.log`**
      (the hostagent's last write, which lands within seconds of shutdown), with the instance's `disk`
      file mtime as a corroborating fallback. A VM that has **never been started** has no
      `ha.stderr.log` and must read as **"never used"**. This is a `stat` at list time — no new
      persisted state, no schema change, and it works for VMs created long before this feature.
- [x] `up <duration>` for running VMs, so every tile has a symmetric, always-populated bottom line.
- [x] **Colour.** Extend the existing `internal/ui/styles.go` palette (ANSI-256 indices, chosen to
      degrade gracefully) — do **not** introduce a second palette. Status is the primary scanning
      channel: running green, stopped dim grey, building amber and animated, failed red. The focused
      tile wears a **bold border**; chrome is **dim**, not absent.
- [x] **Colour is never the only carrier of meaning** — a glyph **and** a text label always accompany
      it. This is also what makes the ANSI-stripped goldens meaningful. Test: `ansi.Strip` of each of
      the four status renderings still distinguishes them.
- [x] **`NO_COLOR` is honoured** and the tile stays readable on a monochrome terminal.
- [x] The concept's `✖ Broken` status is **not** implemented — nothing in Lima or `sand` produces it.
      **Failed**, sourced from the job registry, is the real state it was gesturing at.
- [x] `go test ./...` passes.

## Technical Requirements

- `vm.VM` fields available today: `Name`, `Status` (`"Running"` | `"Stopped"`), `CPUs` (int,
  **allocation**), `Memory` (string, **allocation**), `Disk` (max qcow2), `DiskUsed` (allocated
  on-disk; `""` = unknown), `Dir`, `Arch`.
- **`CPUs` and `Memory` are ALLOCATIONS, not utilization.** They must never be rendered as a
  utilization gauge. Live utilization comes only from the heartbeat (task 05), and only for running
  VMs. This is the dishonesty the whole plan exists to avoid.
- Reuse, do not reinvent: `humanizeBytes` (`internal/ui/format.go`), `diskUsedBytes(dir)`
  (`internal/ui/diskusage_unix.go`, returns `-1` when unmeasurable → render blank, not zero).
- The tile's six-line content budget and its width/height come from task 03's `layoutMode`. Render
  into that budget; do not compute your own offsets.
- The managed/external badge is **not deleted** — it is simply **never exceptional**, because the
  board is unconditionally filtered to managed clones (task 08), so it is uniform across the fleet by
  construction and the exception-only rule hides it with **no special case**. If unmanaged tiles ever
  return, so does the badge, for free. Do not hardcode its removal.
- The subtitle line from the original mockup (`feat: login form work`) is **cut** — no description or
  label field exists on `vm.VM` or in `registry.entry`. Do not invent one.

## Input Dependencies

- Task 03 — `layoutMode` and the tile size budget.
- Task 04 — the job registry's snapshot type and the derived-status function's job half.
- Task 05 — the heartbeat sample type.

## Output Artifacts

- `internal/ui/tile.go` — the tile renderer, the fleet-uniformity rule, and the `last used` probe.
- An extended `internal/ui/styles.go` palette (status colours, focused border, dim chrome).
- The card the board (task 08) composes into a grid.

## Implementation Notes

<details>
<summary>Guidance</summary>

The failure mode of a card UI is **a fixed schema rendered identically on every card** — a form with
the same answer typed into every row. The exception-only rule is the defence, and it only works if it
is implemented as a real uniformity test over the fleet. Written as two hardcoded deletions it will
be right today and wrong the first time someone adds a second base image, which is exactly the
scenario it exists to serve.

The subtlest thing on the tile is the **derived status**, and it is the easiest to get wrong. Lima
does not have a Building status. To Lima, a VM being provisioned is simply `Running`, because Ansible
is just a process inside it. So a tile that renders `vm.Status` shows a build in flight as a healthy
idle VM — and, far worse, shows a **failed** provision as a reassuring green "Running". Consult the
job registry first. Always.

`last used` is the field that makes a **stopped** VM actionable: "last used 6 weeks ago" is how a user
decides what to delete, and it is the only decision they routinely make about a stopped VM. Get the
"never used" case right — a VM that has never started has no `ha.stderr.log` at all, and that reads
naturally as "never used" rather than as an error.

**Test philosophy**: write a few tests, mostly integration. Meaningful tests verify custom business
logic, critical paths, and edge cases specific to this application — test *your* code, not the
framework. Here: the four status derivations, honest absence on a stopped tile, the uniformity rule
**exercised in both directions** (uniform → hidden, varying → shown), the `last used` mtime including
"never used", and `ansi.Strip` still distinguishing the statuses. Not: a test per style var.

Per `PRE_TASK_EXECUTION.md`, RED → GREEN → REFACTOR.

The **real-Lima** proof — that `last used` reads correctly after an actual stop, and that a genuinely
failed provision renders **Failed** and not green "Running" — is task 10. In-process tests here are
necessary but not sufficient.
</details>
