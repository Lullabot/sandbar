---
id: 2
group: "memory-ui"
dependencies: [1]
status: "completed"
created: 2026-09-22
models:
  anthropic: "sonnet"
  openai: "gpt-5.6-sol"
effort: "high"
skills:
  - bubble-tea
  - go-testing
complexity_score: 7
complexity_notes: "Crosses provider or TUI integration boundaries and requires careful verification."
---
# Render Host Memory And Cache

## Objective

Parse guest filesystem cache and render it as a distinct subset of host-resident memory in the compact VM gauge.

## Skills Required

bubble-tea; go-testing.

## Acceptance Criteria

- [x] The primary number and gauge fill use host VM memory, with unknown host usage rendered as no reading.
- [x] Guest cache is parsed from /proc/meminfo and patterned separately without changing gauge width.
- [x] Missing cache and accounting mismatch remain safe.
- [x] `go test ./internal/ui -run "Test.*(Mem|Tile|Heartbeat)"` exits 0.

## Technical Requirements

Use existing heartbeat and tile paths. Keep host and guest readings separate. Use free-like cache semantics from /proc/meminfo. Clamp cache to [0,host used] and all segments to tile width. Add meaningful parser/render tests for absent and mismatched data.

## Input Dependencies

Task 1 host memory provider API.

## Output Artifacts

Heartbeat/cache model, tile rendering, focused tests.

## Implementation Notes

<details>
<summary>Execution guidance</summary>

Read the relevant existing files and tests before editing. Write a few tests, mostly integration: verify custom logic, critical workflows, edge cases, and integration boundaries; do not test framework behavior, third-party libraries, trivial getters, or static configuration. Keep related test scenarios together. Run the acceptance command and inspect its output before marking this task completed. Do not add scope beyond the work order.

</details>

## Noteworthy Events

- 2026-09-22: Kept guest utilization/cache and provider host occupancy as separate fields in the heartbeat registry. Both the per-VM gauge and fleet header now use provider host memory; guest cache uses free-like `Buffers + Cached + SReclaimable - Shmem` accounting and is clamped within the host-used bar segment. A cache reading remains unknown unless all four source fields are present.
