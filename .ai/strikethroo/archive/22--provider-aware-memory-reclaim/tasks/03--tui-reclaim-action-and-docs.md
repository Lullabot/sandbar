---
id: 3
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
# Tui Reclaim Action And Docs

## Objective

Expose Reclaim memory only for capable providers and refresh guest and host readings after completion; document the action and gauge.

## Skills Required

bubble-tea; go-testing.

## Acceptance Criteria

- [ ] Supported running Proxmox VMs offer the action; Lima/unsupported VMs do not.
- [ ] The action shows progress and errors, invokes provider reclaim, then refreshes host and guest metrics.
- [ ] Integration tests cover eligibility and refresh.
- [ ] User docs explain the action and memory numbers.
- [ ] `go test ./internal/ui` and `uvx --with-requirements docs/requirements.txt mkdocs build --strict` exit 0.

## Technical Requirements

Use the central command registry, existing async action/status patterns, and provider capability. Avoid provider-name checks in UI and avoid background reclaim. Test the far side of refresh dispatch. Update docs/ rather than README.md.

## Input Dependencies

Task 1 capability and reclaim operation.

## Output Artifacts

TUI action and tests; docs site update.

## Implementation Notes

<details>
<summary>Execution guidance</summary>

Read the relevant existing files and tests before editing. Write a few tests, mostly integration: verify custom logic, critical workflows, edge cases, and integration boundaries; do not test framework behavior, third-party libraries, trivial getters, or static configuration. Keep related test scenarios together. Run the acceptance command and inspect its output before marking this task completed. Do not add scope beyond the work order.

</details>

## Noteworthy Events

- [2026-09-22] Bound Reclaim memory to `m`; completion replaces the scoped heartbeat so the new guest cache sample and provider host-memory query share a fresh epoch and stale pre-reclaim results cannot land afterward.
