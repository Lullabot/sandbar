---
id: 1
group: "codex-onboarding"
dependencies: []
status: "pending"
created: 2026-09-22
model: "sonnet"
effort: "high"
skills:
  - bash-integration
  - ansible
complexity_score: 7
complexity_notes: "Interactive shell behavior, authentication sequencing, persistent daemon setup, failure retryability, and pass-through compatibility require careful boundary testing."
---
# Implement and Test Codex Remote Onboarding

## Objective
Add a Codex-role shell wrapper that offers one-time remote-control onboarding on the first bare interactive invocation, sequences the required upstream commands safely, preserves ordinary Codex behavior, and proves the critical paths with automated shell-level tests.

## Skills Required
- `bash-integration`: implement terminal-sensitive Bash command wrapping, exact argv delegation, persistent first-run state, and failure propagation.
- `ansible`: install the wrapper only when the opt-in Codex role runs and keep provisioning idempotent.

## Acceptance Criteria
- [ ] The first bare interactive `codex` invocation without an onboarding marker asks whether to enable remote-control support.
- [ ] An affirmative answer runs `codex login --device-auth`, `codex app-server daemon bootstrap --remote-control`, and `codex remote-control pair` in order through the upstream executable.
- [ ] The affirmative marker is created only after all setup steps succeed; any failed step returns failure, does not launch the TUI, and leaves onboarding retryable.
- [ ] A decline is remembered and immediately launches the ordinary upstream Codex TUI.
- [ ] Successful setup explains both `codex agents` for local access and `codex remote-control pair` for additional devices before launching the ordinary TUI.
- [ ] Calls with arguments and calls without terminal stdin forward the original argv unchanged and do not prompt.
- [ ] The wrapper is installed only by `roles/codex`, is idempotent under repeat provisioning, and never calls itself recursively.
- [ ] A runnable focused test command exercises affirmative success, decline persistence, setup failure retryability, exact argument forwarding, and non-interactive behavior and exits 0.
- [ ] `ansible-playbook --syntax-check -i localhost, -c local site.yml --extra-vars 'user_name=root toolset_codex=true'` exits 0.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Use a Bash function named `codex` that reaches the installed executable with `command codex`.
- Scope the managed shell definition to `roles/codex`; do not put it in the general user role.
- Store onboarding completion under the existing per-user `~/.config/sandbar` directory without editing upstream auth or Codex configuration files.
- Prompt only for a bare interactive invocation while no completion marker exists.
- Accept an affirmative `y` or `Y`; treat other input as decline.
- Use `codex app-server daemon bootstrap --remote-control` as the durable daemon setup command exposed by the installed CLI.
- Make every affirmative step short-circuit on failure and defer marker creation until pairing succeeds.
- Keep the wrapper source independently sourceable or otherwise testable with a fake upstream executable and isolated `HOME`.
- Do not contact OpenAI, perform a real login, or install/start a real daemon during automated tests.

## Input Dependencies
None. Use the existing `roles/codex` installation, the Bash wrapper convention in `roles/user/tasks/main.yml`, and the command contracts recorded in Plan 22.

## Output Artifacts
- Codex-role provisioning changes that deploy the wrapper.
- A sourceable wrapper definition or equivalent managed Bash block.
- Focused automated tests and any minimal test fixtures needed to fake the upstream `codex` executable.

## Implementation Notes

<details>
<summary>Execution guidance</summary>

1. Read the Codex role, the existing Claude wrapper, and nearby Ansible test conventions before editing.
2. Prefer a role-owned wrapper file sourced from an idempotently managed `~/.bashrc` line if that makes the shell logic directly testable. Ensure the deployed file is user-owned and readable but not writable by other users.
3. In the function, immediately delegate when arguments are present, stdin is not a terminal, or the onboarding marker exists.
4. For the first bare interactive call, print a concise question and read one line. On decline, create the state directory and marker, then delegate the bare call.
5. On acceptance, run the three required commands in order with `|| return $?`-equivalent failure propagation. After pairing succeeds, create the marker, print the two follow-up commands, and delegate the bare call.
6. Build a fake executable that records each argv vector in an unambiguous format. Use a pseudo-terminal tool available in the repository environment for prompt-path tests; do not weaken the implementation to make a pipe appear interactive.
7. Keep tests focused on custom workflow logic and integration boundaries. Test this application's command sequencing, marker behavior, and edge cases—not Codex's own authentication or daemon internals. Favor the critical end-to-end shell flow over per-line assertions.
8. Run the focused tests and the Ansible syntax check, recording exact commands and results in the task before marking it completed.

</details>
