---
id: 1
group: "provider"
dependencies: []
status: "completed"
created: 2026-09-22
models:
  anthropic: "sonnet"
  openai: "gpt-5.6-sol"
effort: "high"
skills:
  - provider-design
  - go-testing
complexity_score: 7
complexity_notes: "Crosses provider or TUI integration boundaries and requires careful verification."
---
# Provider Memory Capability

## Objective

Expose an optional memory-reclaim capability and host VM memory measurement; implement Proxmox reclaim through guest SSH.

## Skills Required

provider-design; go-testing.

## Acceptance Criteria

- [ ] Proxmox advertises support and executes sync plus privileged drop_caches over existing SSH; Lima and other providers remain unsupported.
- [ ] Proxmox returns its host-side VM mem reading; other providers use an existing host-side reading where available, otherwise unknown.
- [ ] Tests assert operation, capability, and measurement.
- [ ] `go test ./internal/provider ./internal/pve` exits 0.

## Technical Requirements

Use an optional interface or capability convention rather than extending every Provider implementation. Do not call QEMU monitor or change VM configuration. Reuse SSH and privileged guest command patterns. Keep measurement distinct from guest metrics. Include fake-backed tests at the provider boundary.

## Input Dependencies

Existing provider seam and Proxmox/PVE VM status code.

## Output Artifacts

Provider APIs and focused tests.

## Implementation Notes

<details>
<summary>Execution guidance</summary>

Read the relevant existing files and tests before editing. Write a few tests, mostly integration: verify custom logic, critical workflows, edge cases, and integration boundaries; do not test framework behavior, third-party libraries, trivial getters, or static configuration. Keep related test scenarios together. Run the acceptance command and inspect its output before marking this task completed. Do not add scope beyond the work order.

</details>
