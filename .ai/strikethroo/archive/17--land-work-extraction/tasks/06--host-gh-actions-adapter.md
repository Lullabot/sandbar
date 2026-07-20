---
id: 6
group: "gh-host-actions"
dependencies: []
status: "completed"
created: 2026-07-17
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Host-side gh adapter invoking the workstation token; command-injection safety (args never shell-interpolated) and graceful gh-absent fallback are security-relevant. Risk floor applies."
skills:
  - go
  - gh-cli
---
# Host gh actions adapter: PR state, one-shot draft create, browser URLs

## Objective
Provide the **workstation-local** (`os/exec`) host action layer that land's pane
(task 7) and CLI (task 9) call over `(org/repo, branch)`: authoritative PR-state
lookup, one-shot **draft** PR creation, gh-absent detection, and gh-free browser
URL construction + OS opener. All inputs pass as **exec arguments, never
shell-interpolated**, so an attacker-chosen branch name can only become PR text.

## Skills Required
- **go** — `os/exec` with argument vectors, JSON decoding, `runtime.GOOS`-based
  opener selection.
- **gh-cli** — `gh pr list`, `gh pr create --fill --draft`, `gh api POST
  /repos/{org}/{repo}/pulls`, and `gh auth status`.

## Acceptance Criteria
- [ ] `PRState(orgRepo, branch)` runs `gh pr list -R <org/repo> --head <branch>
      --json number,url,state,isDraft` on the **workstation** and returns parsed
      state (exists? number, url, open/closed, draft?). This is the
      **authoritative** branch/PR check.
- [ ] `CreateDraftPR(orgRepo, branch)` opens a **one-shot draft** PR: title/body
      auto-filled from the branch's commits, base = the repo's **default branch**,
      `--draft`, **no local checkout**. Prefer the deterministic `gh api POST
      /repos/<org/repo>/pulls` path (title/body from the branch head commit via
      `gh api`) and/or `gh pr create -R --head --base --fill --draft`; pin the
      exact invocation against gh's headless behavior at implementation time.
- [ ] **All inputs pass as exec args** (`exec.Command("gh", "pr", "list", "-R",
      orgRepo, "--head", branch, ...)`) — never concatenated into a shell string.
      A test feeds a branch name containing shell metacharacters and asserts it is
      passed as one argv element (no interpolation).
- [ ] `Available()` (or equivalent) detects whether host `gh` is present and
      authenticated (`gh` on PATH + `gh auth status` OK).
- [ ] Browser helpers are **gh-free**: `CompareURL(orgRepo, branch)` →
      `https://github.com/<org/repo>/pull/new/<branch>`; `PRURL(...)` for an
      existing PR; and an `OpenInBrowser(url)` that selects
      `xdg-open`/`open`/`start` by `runtime.GOOS`.
- [ ] `go test ./internal/<pkg>/... -race` passes with: argv-injection-safety
      test, URL-construction tests, opener selection by GOOS, and a gh-missing
      path that falls back without panicking (inject a fake exec runner).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Runs on the **machine running `sand`** (the workstation) via `os/exec` — **not**
  on the VM's connection-profile host and **not** in the guest. It needs only
  `(org/repo, branch)`; it never touches a VM.
- Make the exec runner injectable (an interface or func field) so tests can fake
  `gh`/opener without a real binary. GH_TOKEN/`gh auth` is the user's ambient
  workstation credential; do not read the guest token here.
- Never invoke `gh` (or anything) in a way that executes repository content —
  only `pr list`, PR create, and PR view/browser.

## Input Dependencies
None — a pure host-side adapter parameterized by `(org/repo, branch, default
base)`. (Task 2 supplies those values at the call sites, but this package does
not depend on it.)

## Output Artifacts
- A host-actions package (e.g. `internal/landgh` or `internal/host/gh`) exposing
  `Available`, `PRState`, `CreateDraftPR`, `CompareURL`, `PRURL`,
  `OpenInBrowser`, with an injectable exec runner. Consumed by tasks 7 and 9.

## Implementation Notes
<details>
<summary>Detailed implementation guidance</summary>

1. Define an injectable runner: `type runner func(ctx, name string, args
   ...string) ([]byte, error)` defaulting to `exec.CommandContext(...).Output()`.
   Every gh call goes through it so tests fake it.
2. `PRState`: `gh pr list -R orgRepo --head branch --json number,url,state,isDraft`
   → decode JSON array; empty ⇒ no PR. Return a small struct.
3. `CreateDraftPR`: resolve default base branch (from `gh api
   repos/<org/repo> --jq .default_branch`), then create. Two viable paths — try
   `gh pr create -R orgRepo --head branch --base base --fill --draft`; if gh
   resists headless creation without a local checkout, fall back to `gh api
   --method POST repos/<org/repo>/pulls -f head=branch -f base=base -f title=...
   -f body=... -F draft=true`, deriving title/body from the branch head commit
   via `gh api`. Verify against a real remote at implementation time (see plan
   risk note). Both are pure API — no local clone.
4. **Injection safety** is a graded acceptance criterion: assert argv elements,
   never build a `sh -c` string. Add the metacharacter branch-name test.
5. `Available`: check `exec.LookPath("gh")` and `gh auth status`; cache is fine.
6. Browser: construct URLs by string (validate `org/repo` shape) and open via the
   GOOS-appropriate opener; make the opener injectable too for tests.
7. RED→GREEN→REFACTOR on the argv construction, URL construction, and fallback
   selection — the security- and correctness-critical logic. No test for the
   thin exec wrapper itself beyond the fake-runner paths.
</details>
