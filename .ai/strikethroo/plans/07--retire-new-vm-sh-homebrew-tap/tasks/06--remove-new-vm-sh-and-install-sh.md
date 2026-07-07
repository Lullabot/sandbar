---
id: 6
group: "ci-migration-and-removal"
dependencies: [4, 5]
status: "pending"
created: 2026-07-06
model: "sonnet"
effort: "medium"
skills:
  - documentation
---
# Delete `new-vm.sh` + `install.sh` and switch docs to `brew`

## Objective
Complete the retirement: delete `scripts/new-vm.sh` and the curl|bash
`install.sh`, scrub every remaining in-repo reference to them (starting with the
two comments that ship inside the embedded playbook), and rewrite the docs so
`brew install lullabot/sandbar/sand` is the sole documented install path and the
`sand create` headless mode / working-tree-first-with-embedded-fallback
resolution are documented. This is the final gate, run only after the release
tap exists (task 4) and CI is green on the binary (task 5).

## Skills Required
- **documentation** — rewriting the README quick-start and the relocated `sand`
  README around `brew`, and scrubbing stale script references across Markdown,
  YAML, and Go comments.

## Acceptance Criteria
- [ ] `scripts/new-vm.sh` and the repo-root `install.sh` are deleted (the
      `scripts/` dir is removed if now empty).
- [ ] `grep -rn "new-vm.sh" . --exclude-dir=.git --exclude-dir=.ai` returns
      nothing — including the two playbook comments (`site.yml` "see
      scripts/new-vm.sh" and `roles/project/defaults/main.yml` "set via
      new-vm.sh …", which ship inside the **embedded** playbook), the docs, and
      the "Mirrors new-vm.sh"-style comments in Go source.
- [ ] `grep -rn "install.sh" . --exclude-dir=.git --exclude-dir=.ai` returns
      **only** the unrelated third-party installer URLs
      (`claude.ai/install.sh` in `roles/claude-code`, `astral.sh/uv/install.sh`
      in `roles/dev-tools`) — the repo-root `install.sh` and all references to it
      are gone.
- [ ] `README.md`'s quick-start installs via `brew install lullabot/sandbar/sand`;
      the curl|bash instructions and every `install.sh` reference are removed.
- [ ] The relocated `sand` README (moved in task 1) has its "Relationship to
      new-vm.sh" section rewritten to the `brew` story and documents: the
      `sand create` headless mode, the working-tree-first / embedded-fallback
      playbook resolution, and the repo-root build path (`go build ./cmd/sand`).
- [ ] Release/tap docs exist (in the README, the relocated README, or a docs
      file) covering how a release is cut (tag → GoReleaser), how the tap is
      maintained, and the human prerequisites (`lullabot/homebrew-sandbar` +
      `HOMEBREW_TAP_GITHUB_TOKEN`).
- [ ] `go build ./cmd/sand` and `go test ./...` still pass after the comment
      scrubs (editing Go comments must not break the build).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- The two playbook comment references are load-bearing for the embed: they ship
  inside the binary, so the shipped `sand` must not reference a deleted script.
  Exact locations: `site.yml:6` (`# cloned cheaply (see scripts/new-vm.sh):`)
  and `roles/project/defaults/main.yml:2`
  (`# Optional initial project clone. Empty by default — set via new-vm.sh or`).
- Files known to reference the doomed scripts (scrub or delete each; the greps
  above are the completeness check): `install.sh` (delete), `scripts/new-vm.sh`
  (delete), `README.md`, the relocated `sand` README, `site.yml`,
  `roles/project/defaults/main.yml`, `group_vars/all.yml.example`, `.gitignore`,
  and Go source comments across `internal/…` and `cmd/…` that say
  "Mirrors new-vm.sh" / "matches new-vm.sh" etc. `.github/workflows/test.yml`
  was already scrubbed by task 5 — confirm it stays clean.
- Leave the third-party installer URLs alone: `claude.ai/install.sh` (in
  `roles/claude-code/tasks/main.yml`) and `astral.sh/uv/install.sh` (in
  `roles/dev-tools/tasks/main.yml`) are guest-tool installers, not our script.
- Rewriting "Mirrors new-vm.sh" comments: replace with a script-free phrasing
  (e.g. "Mirrors the original bash provisioner" or drop the clause) — the intent
  is that no live artifact points at a file that no longer exists. Do not alter
  the code the comments describe.

## Input Dependencies
- **Task 4** (release + tap) — the `brew install lullabot/sandbar/sand` path the
  docs now point users to must exist before curl|bash is removed.
- **Task 5** (CI migrated + green) — the binary must be proven to cover the
  script in CI before the script is deleted.

## Output Artifacts
- A repo with no `new-vm.sh` / curl|bash `install.sh` and docs centred on `brew`
  + `sand create`. This satisfies the plan's final Success Criterion (removal is
  clean) and closes the work order.

## Implementation Notes
Use the two greps as your definition of done — resolve every hit until the
`new-vm.sh` grep is empty and the `install.sh` grep shows only the two
third-party URLs. Verify the release tap actually exists (task 4) and the
migrated `lima-e2e` is green (task 5) before deleting anything; the deletion is
irreversible-in-spirit (it removes the only non-brew install path), so confirm
its replacements are live first.

<details>
<summary>Step-by-step</summary>

1. Confirm preconditions: task 4's tap/release path exists and task 5's
   `lima-e2e` is green on the built binary. If either is not done, stop — do not
   delete the scripts.
2. Delete the files: `git rm install.sh scripts/new-vm.sh`; remove `scripts/` if
   empty.
3. Scrub the embedded-playbook comments (highest priority — they ship in the
   binary):
   - `site.yml:6` — remove the `(see scripts/new-vm.sh)` reference (keep the
     surrounding explanatory comment, just drop the dead pointer).
   - `roles/project/defaults/main.yml:2` — reword "set via new-vm.sh or …" to
     drop the script (e.g. "set via `sand create --clone-url` or …").
4. Rewrite `README.md`'s quick-start around
   `brew install lullabot/sandbar/sand`; delete the curl|bash block and every
   `install.sh` mention. Add (here or in the relocated README) the release/tap
   docs: tag → GoReleaser, tap maintenance, and the human prerequisites
   (`lullabot/homebrew-sandbar` repo + `HOMEBREW_TAP_GITHUB_TOKEN`).
5. Rewrite the relocated `sand` README's "Relationship to new-vm.sh" section to
   the brew story, and document `sand create` (headless flags), the
   working-tree-first / embedded-fallback resolution, and `go build ./cmd/sand`.
6. Scrub the remaining references so the grep is empty: `group_vars/all.yml.example`,
   `.gitignore`, and the "Mirrors new-vm.sh"-style comments in Go source. Reword,
   don't delete code.
7. Run the completeness checks:
   - `grep -rn "new-vm.sh" . --exclude-dir=.git --exclude-dir=.ai` → empty.
   - `grep -rn "install.sh" . --exclude-dir=.git --exclude-dir=.ai` → only
     `claude.ai/install.sh` and `astral.sh/uv/install.sh`.
   - `go build ./cmd/sand` and `go test ./...` → still green.
</details>
