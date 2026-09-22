---
id: 2
group: "memory-ui"
dependencies: [1]
status: "pending"
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

- [ ] The primary number and gauge fill use host VM memory, with unknown host usage rendered as no reading.
- [ ] Guest cache is parsed from /proc/meminfo and patterned separately without changing gauge width.
- [ ] Missing cache and accounting mismatch remain safe.
- [ ] `go test ./internal/ui -run "Test.*(Mem|Tile|Heartbeat)"` exits 0.

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
