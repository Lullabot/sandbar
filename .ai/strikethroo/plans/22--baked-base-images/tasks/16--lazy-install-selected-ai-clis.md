---
id: 16
group: "agent-lifecycle"
dependencies: [11]
status: "pending"
created: 2026-09-13
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
complexity_score: 7
complexity_notes: "Changes first-run behavior for selected agents and introduces concurrency and failure-recovery requirements without regressing the generic per-VM agent preference lifecycle."
skills:
  - ansible
  - shell
  - testing
---
# Lazy-install selected Claude Code and Codex agents on first use

## Objective

Preserve `main`'s per-VM coding-agent selection and remembered preferences while changing the selected Claude Code and Codex roles to install robust first-use shims instead of downloading fast-moving binaries during VM finalization. OpenCode and Pi retain their existing role behavior unless separately requested.

## Skills Required

`ansible` for the finalize-only agent roles; `shell` for concurrency-safe shims; `testing` for first-run and failure-path verification at the guest boundary.

## Acceptance Criteria

- [ ] Selecting Claude Code or Codex still records the choice through `agentprefs` and runs its role during finalize, but installs a shim at the command path rather than the vendor binary.
- [ ] Not selecting an agent leaves both its binary and shim absent.
- [ ] OpenCode and Pi selections and installation behavior are unchanged.
- [ ] Everything else the Claude/Codex roles do is preserved: configuration, onboarding defaults, remote-control wrappers/services, PATH setup, and clipboard integration.
- [ ] First invocation installs the current vendor release and transparently execs it with all arguments, standard streams, environment, and exit status preserved.
- [ ] Successful installation supersedes the shim, so later invocations perform no installer network request or shim recursion.
- [ ] Failed installation leaves a working shim and an actionable error; concurrent invocations serialize and cannot leave a partial binary or stale permanent lock.
- [ ] Reset with Preserve Agents keeps installed state; reset without it restores the selected agent's first-use shim.
- [ ] Verification reaches the guest: create a VM with Claude selected and Codex unselected, assert the expected shim/absence, run Claude twice, and verify the real binary on the second run. Repeat the failure and concurrent-first-run cases.
- [ ] `go test ./...`, Ansible syntax checks, and focused role tests pass.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Coding agents are finalize-only on current `main`; do not add them to the published base image or its manifest/version.
- Reuse the generic agent selection and preservation paths. Do not reintroduce agent bits into `CreateConfig.ToolsetKey` or shared-base staleness.
- Use an atomic lock primitive available in the guest and bound any wait; recovery from a crashed installer must be possible without rebuilding the VM.
- Vendor installers may overwrite the command path. Detect success by verifying that the path no longer contains the shim marker before execing it.
- Keep installer output on stderr so a caller parsing agent stdout is not corrupted.

## Input Dependencies

- Task 11's final separation between fixed base dependencies and per-VM agents.

## Output Artifacts

- Updated Claude Code and Codex roles with tested first-use shims.

## Implementation Notes

This task no longer exists to reduce the published image: `main` already moved all coding agents out of the base. It preserves the user's explicit first-use decision while fitting it into the newer generic-agent lifecycle. Limit the change to Claude Code and Codex; automatically applying it to OpenCode and Pi would be an unrequested product change.
