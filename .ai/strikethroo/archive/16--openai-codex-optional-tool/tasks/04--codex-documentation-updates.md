---
id: 4
group: "docs"
dependencies: [1, 2]
status: "completed"
created: 2026-07-16
model: "haiku"
effort: "low"
skills:
  - documentation
---
# Document Codex: available tools, login, security model, and CLI reference

## Objective

Update the four documentation surfaces the plan names so users can discover the opt-in Codex tool, log into it, understand its friction-free configuration, and see accurate `sand create` flags — including stating plainly that Codex has no CLI-reachable remote control or phone notifications.

## Skills Required

`documentation` — MkDocs Markdown edits matching the existing docs' voice, plus one mechanical help-dump regeneration.

## Acceptance Criteria

- [x] `docs/getting-started/available-tools.md`: the "Claude Code & git" section (or a sensibly renamed equivalent) lists the OpenAI Codex CLI as **opt-in** (`sand create --with-codex` or the TUI toggle), contrasting with the default-on tools.
- [x] `docs/getting-started/first-vm.md`: a "Logging into Codex" passage parallel to "Logging into Claude Code": no credential is provisioned; run `codex` once and sign in with a ChatGPT account; note the sign-in callback targets a localhost port so headless login needs the upstream-documented workaround (SSH port forwarding, or copying an existing `~/.codex/auth.json`); adjacent to the existing remote-control paragraph, state that Codex offers **no** CLI-reachable remote control or phone notifications (its phone pairing requires the Codex desktop app on macOS/Windows), so users choosing Codex should not expect Claude-style phone alerts from the VM.
- [x] `docs/reference/security-model.md`: bullets mirroring Claude's — Codex (when selected) runs with approvals and its own sandbox off (`approval_policy = "never"`, `sandbox_mode = "danger-full-access"` in the provisioned config), deliberate because the ephemeral VM is the sandbox; and no Codex credential is provisioned.
- [x] `docs/using-sand/cli-reference.md`: the `sand create` flags table documents `--with-codex` (default false, opt-in) — and the stale `sand create --help` dump is regenerated from the built binary so ALL five `--with-*` flags appear (the four existing ones are currently missing too).
- [x] Verification: `go run ./cmd/sand create --help 2>&1 | grep -c "with-"` prints `5`, and the same five lines appear verbatim in the cli-reference help dump.
- [x] Verification: `grep -ri "codex" docs/ | grep -iv "remote control\|desktop\|phone\|notification" | wc -l` returns non-zero (content landed) AND `grep -ri "codex" docs/getting-started/first-vm.md | grep -ci "remote"` returns non-zero (the limitation is stated).
- [x] Verification: `pip install -r docs/requirements.txt && mkdocs build --strict` exits 0 (or, if the strict build already fails on main for unrelated reasons, document that baseline and show the new pages introduce no NEW warnings).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Match the existing docs' voice: direct, security-explicit, no marketing. Read each target section fully before editing.
- The remote-control limitation wording must be factual: phone pairing is a Codex **desktop app** (macOS/Windows) feature using QR device pairing; OpenAI documents it as not available from the Codex CLI or IDE extension. Do not speculate about future availability.
- `docs/using-sand/tui.md` and `AGENTS.md` need NO changes (verified during plan refinement — do not touch them).
- `CHANGELOG.md` is release-please-managed — do not edit it.

## Input Dependencies

- Task 1 — the provisioned config keys the security model describes must match `roles/codex/templates/codex-config.toml.j2`.
- Task 2 — `--with-codex` must exist so the regenerated help dump includes it.

## Output Artifacts

- Updated `docs/getting-started/available-tools.md`, `docs/getting-started/first-vm.md`, `docs/reference/security-model.md`, `docs/using-sand/cli-reference.md`.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

Structure to follow per file:

- **available-tools.md**: the page groups tools under headings; Claude Code sits under "Claude Code & git". Add Codex with a one-line description and an explicit "opt-in: pass `--with-codex` to `sand create` (or enable the toggle in the TUI create form)" note. Consider retitling the heading to "Agent CLIs & git" only if it reads better; otherwise leave headings alone.
- **first-vm.md**: mirror the "Logging into Claude Code" section's structure (why no token is provisioned, run-once login). For Codex add: `codex` sign-in uses a ChatGPT account; the OAuth callback binds to a localhost port inside the VM, so from a host terminal use `ssh -L` port forwarding per OpenAI's headless-login docs, or copy an existing `~/.codex/auth.json` from a machine you already signed in on. Then the remote-control expectation note, placed right after the existing paragraph that says notifications arrive through Claude Code's remote control.
- **security-model.md**: the page is a bullet list of deliberate decisions; add two bullets styled like the Claude ones (see "Claude Code runs with permission prompts skipped" and "sand does not provision a Claude Code credential") and keep the "when selected / opt-in" qualifier so readers don't think Codex is always present.
- **cli-reference.md**: add `--with-codex` to the flags table with the same phrasing pattern as other rows, then regenerate the verbatim `--help` block: `go run ./cmd/sand create --help` (capture stderr — Go's flag package prints usage to stderr) and replace the stale dump. Preserve surrounding prose. Note the dump currently ends before the `--with-*` flags; the regeneration fixes four missing flags plus the new one — mention nothing about it beyond accuracy.

The mkdocs build uses `mkdocs.yml` at the repo root with `docs/requirements.txt`. If installing requirements is impractical, at minimum run a link-level sanity pass and state exactly what was and wasn't verified — do not claim the build passed without running it.
</details>
