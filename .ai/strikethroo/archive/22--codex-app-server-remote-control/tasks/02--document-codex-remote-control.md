---
id: 2
group: "codex-onboarding"
dependencies: [1]
status: "completed"
created: 2026-09-22
model: "haiku"
effort: "low"
skills:
  - technical-writing
---
# Document Codex Remote Control

## Objective
Update the first-VM guide and role commentary so users understand the implemented one-time Codex remote-control choice, device authentication, local agent access, and additional device pairing.

## Skills Required
- `technical-writing`: produce concise, accurate user instructions that match the implemented wrapper and upstream CLI vocabulary.

## Acceptance Criteria
- [ ] `docs/getting-started/first-vm.md` explains that the first bare interactive `codex` run asks whether to enable remote-control support.
- [ ] The guide explains the affirmative device-code login, persistent app-server setup, and initial ChatGPT device pairing without asking users to run the setup commands manually.
- [ ] The guide tells users to run `codex agents` for local access and `codex remote-control pair` to pair more devices.
- [ ] The guide explains that a decline is remembered and ordinary Codex behavior continues.
- [ ] The obsolete claim that Codex has no CLI-reachable remote control or phone support is removed.
- [ ] Comments in the Codex role accurately summarize the implemented first-run behavior.
- [ ] `uvx --with-requirements docs/requirements.txt mkdocs build --strict` exits 0 with no warnings.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Describe only behavior implemented by Task 1 and observed command names; do not promise account/workspace availability or unrequested notification semantics.
- Keep user-facing prose in `docs/`, not `README.md`.
- Preserve the existing guidance that Codex is opt-in through `--with-codex` or the TUI toggle.
- Link to existing relevant site pages only when the target is stable and useful.

## Input Dependencies
Task 1 must be completed so wording can match the final prompt, state behavior, and command sequence.

## Output Artifacts
- Updated Codex first-use documentation.
- Updated Codex role comments where necessary.

## Implementation Notes

<details>
<summary>Execution guidance</summary>

1. Read Task 1's completed implementation and test expectations before drafting.
2. Rewrite the existing “Logging into Codex” section rather than appending contradictory paragraphs.
3. Lead with the wrapper's user-visible first-run experience, then explain the commands available after setup.
4. Keep device authentication phrased consistently with OpenAI documentation and the actual `codex login --device-auth` invocation.
5. Run the strict MkDocs build and inspect the edited section for stale claims before marking the task completed.

</details>
