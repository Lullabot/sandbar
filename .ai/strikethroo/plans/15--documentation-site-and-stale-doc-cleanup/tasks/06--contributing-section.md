---
id: 6
group: "docs-content"
dependencies: [1]
status: "pending"
created: 2026-07-14
model: "sonnet"
effort: "medium"
skills:
  - technical-writing
  - ansible
---
# Contributing section, including the embedded playbook

## Objective

Write the three Contributing pages: development (repo layout, build/test/docs commands), the embedded Ansible playbook (the mechanism, demoted out of the user-facing path and explained where contributors will actually need it), and the release pipeline.

## Skills Required

`technical-writing`; `ansible` to describe the playbook's phase structure and roles accurately.

## Acceptance Criteria

- [ ] `docs/contributing/development.md`, `ansible-playbook.md`, and `releases.md` are written (no stubs remain).
- [ ] `ansible-playbook.md` describes Ansible as an **internal mechanism**: how the fileset is embedded, how it is resolved and mounted, how it runs inside the guest, what the three `provision_phase` values do, and what the six roles are. It contains **no instruction for an end user to install Ansible or run `ansible-playbook` against a VM** — this is the demotion the plan calls for, not a relocation of the old how-to.
- [ ] `development.md` documents the docs build commands (`mkdocs serve` / `mkdocs build --strict` via `uvx`) as well as the Go build/test commands, and states that the repo deliberately has no Makefile and no Node toolchain.
- [ ] `releases.md` describes the release-please → GoReleaser → Homebrew tap pipeline and the manual GitHub Pages setting the docs deploy requires.
- [ ] `uvx --with-requirements docs/requirements.txt mkdocs build --strict` exits 0 with no `WARNING`. Paste the output into your completion report.
- [ ] Confirm in your report that you did not modify, move, or delete `site.yml`, `ansible.cfg`, `inventory`, `roles/`, or `group_vars/`.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Sources of truth: `playbook_embed.go`, `internal/provision/` (`LocatePlaybook`, `vars.go`), `site.yml`, `roles/`, `AGENTS.md`, `.goreleaser.yaml`, `release-please-config.json`, `.github/workflows/`.
- **Read-only with respect to Ansible assets.** This task documents them; it must not touch them.

## Input Dependencies

Task 1: scaffold, nav, stubs.

## Output Artifacts

Three written pages under `docs/contributing/`.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**This task is the load-bearing half of the "clean up stale Ansible docs" ask.** The Ansible content is not being deleted — it is true, and a contributor needs it. It is being *demoted*: moved out of the user-facing path and into Contributing, and rewritten as "here is how the machine works" rather than "here is how you drive it". Read that distinction carefully before you write. If a sentence tells a reader to run `ansible-playbook`, it does not belong on this page.

**`ansible-playbook.md`** — the mechanism, verified against the code:

- The fileset (`site.yml`, `ansible.cfg`, `inventory`, `roles/`, `group_vars/`) is `go:embed`ed into the binary — see `playbook_embed.go`.
- At run time `provision.LocatePlaybook()` resolves **working-tree first** (it walks up looking for a toplevel containing `site.yml`) and otherwise extracts the embedded copy to a temp directory. This is why a contributor's local edits to `roles/` take effect when they run `go run ./cmd/sand` from the tree — worth stating, it is the single most useful thing on this page.
- That directory is the VM's **only** mount, and it is **read-only**.
- Ansible is installed *inside the guest*, and the playbook runs there with `--connection=local`. It does not run on the host, and the host does not need Ansible.
- Per-phase variables (including any clone token) are streamed into guest tmpfs, never placed on argv.
- `provision_phase` selects the phase: `base` (heavy, identity-free, produces the shared base image), `finalize` (light, per-VM identity), `full` (both). See `site.yml`.
- Six roles: `base`, `user`, `samba`, `dev-tools`, `claude-code`, `project`. Note that `samba` is force-disabled for every `sand` run by `internal/provision/vars.go`, so the role exists but does not execute on this path.
- Note the CI signal: the `lint` job in `.github/workflows/test.yml` runs an `ansible-playbook --syntax-check`, so a syntax error in the playbook fails CI.
- `inventory` still contains `ansible_host=CHANGE_ME` and is embedded — it is vestigial on the `sand` path. Say so rather than leaving a contributor to wonder.

**`development.md`:**

- Package layout (`cmd/sand`, `internal/...`) — `AGENTS.md` has the current map; reuse it, don't invent a new one.
- Build: `go build ./cmd/sand`. Run: `go run ./cmd/sand`. Format: `gofmt -l .`. Vet: `go vet ./...`.
- Test: `go test ./...` (unit plus teatest golden TUI snapshots — no VM required). Regenerate goldens with `go test ./internal/ui -run TestTUI -update`. Real-VM end-to-end tests are behind a build tag: `go test -tags limae2e ./...`.
- Docs: `uvx --with-requirements docs/requirements.txt mkdocs serve` to preview, `... mkdocs build --strict` to check. Note that `--strict` is the docs quality gate and that it runs on pull requests.
- State the deliberate absences: **no Makefile** (`AGENTS.md` says so), **no Node/npm toolchain anywhere**, no `.golangci.yml`. A contributor who assumes `make build` works should be corrected on this page, not by a failing command.
- CI triggers: `push` to `main`, `pull_request`, and `workflow_dispatch`. A plain feature-branch push runs **no** CI — that surprises people; say it.

**`releases.md`:**

- release-please (`release-type: go`, `bump-minor-pre-major`, draft releases, `force-tag-creation`) opens the release PR and, on merge, creates the tag and a **draft** GitHub release.
- GoReleaser adopts that draft (`use_existing_draft`), cross-compiles darwin+linux × amd64+arm64 with `CGO_ENABLED=0`, uploads the archives, publishes the release, and pushes the Homebrew formula. Both jobs live in one workflow because immutable releases mean assets can only be added while the release is still a draft — that is the reason, and it is worth recording.
- Homebrew tap `lullabot/homebrew-sandbar`; a **formula**, not a cask, so `brew install` works on macOS and Linux alike.
- Docs releases: a tag also triggers the docs workflow, which publishes that version with `mike` and moves the `latest` alias.
- **The manual step**: GitHub Pages must be set, once, to *Settings → Pages → Deploy from a branch → `gh-pages` / `/ (root)`*. The workflow creates the branch on first run, but the site 404s until this setting is flipped. Document it here — it is the one thing automation cannot do for the next maintainer.
</details>
