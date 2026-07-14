---
id: 8
group: "docs-cleanup"
dependencies: [2, 3, 4, 5, 6, 7]
status: "completed"
created: 2026-07-14
model: "sonnet"
effort: "high"
skills:
  - technical-writing
  - go
---
# Retire the stale docs: rewrite README, delete README-sand, reconcile AGENTS.md

## Objective

Now that the docs site carries the content: rewrite `README.md` as a short landing page that defers to the site, delete `README-sand.md`, and update `AGENTS.md` so it is correct about the new repository layout and the docs commands — removing every user-facing instruction to install Ansible or run `ansible-playbook`, and every reference to the product as "the playbook" or "the script".

## Skills Required

`technical-writing`; `go` to spot-check any claim retained in the README against the code.

## Acceptance Criteria

- [ ] `README.md` is a landing page: what `sand` is, `brew install`, a minimal quick start, and a prominent link to the published site. It no longer contains the "Other provisioning methods" section, the Ansible variable table, or the `ansible-playbook` instructions.
- [ ] `README.md` is not titled "Claude Code Development VM Playbook" and does not open by calling the project an Ansible playbook.
- [ ] `README-sand.md` is deleted (`test ! -e README-sand.md`). No redirect stub or deprecation notice is left behind.
- [ ] `AGENTS.md` is updated for: the new `docs/` directory and `mkdocs.yml`, the docs build/serve commands, the deletion of `README-sand.md`, and the new `docs.yml` CI job. Its existing content that is still accurate is left alone.
- [ ] `CHANGELOG.md` is **not** modified (release-please owns it; its `new-vm.sh` entries are correct history).
- [ ] **No Ansible asset is deleted.** `test -f site.yml && test -f ansible.cfg && test -f inventory && test -d roles && test -d group_vars` passes, and `go build ./cmd/sand && go test ./...` passes. Paste both outputs.
- [ ] This returns nothing: `grep -rniE 'ansible-playbook|apt install ansible|new-vm\.sh' --include='*.md' . | grep -vE '^\./(\.ai|\.claude|\.agents)/|CHANGELOG\.md'` — paste the (empty) result.
- [ ] `uvx --with-requirements docs/requirements.txt mkdocs build --strict` still exits 0 with no `WARNING`.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- The published site URL is `https://lullabot.github.io/sandbar/` — link `latest/` from the README as the reference repo does.
- The boundary is absolute: user-facing *instructions* to run Ansible are removed; Ansible *assets* and the contributor documentation of the mechanism (task 6) stay.

## Input Dependencies

Tasks 2–6 (the site must actually carry the content before the READMEs stop carrying it) and task 7 (so the README can link to a site that is set up to publish).

## Output Artifacts

- Rewritten `README.md`
- Deleted `README-sand.md`
- Updated `AGENTS.md`

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Before you delete anything, confirm the content landed.** Read the pages produced by tasks 2–6 and satisfy yourself that everything a reader currently gets from `README.md` (526 lines) and `README-sand.md` (512 lines) now exists somewhere in `docs/`, corrected. If something was dropped, write it into the right docs page *first*, then delete. Deleting content that has no new home is the one way this task can do real damage.

**The new `README.md`** — model it on `playwright-drupal`'s, which is a short pitch that ends by deferring to the site. Roughly:

- Title: `sandbar` (or `sand`), not "Claude Code Development VM Playbook".
- One paragraph: `sand` is a single Go binary that provisions disposable Claude Code development VMs on Lima.
- Install: `brew install lullabot/sandbar/sand`.
- Quick start: `sand` (opens the board, press `n`) or `sand create`, then `sand shell claude`.
- **"For full documentation, visit https://lullabot.github.io/sandbar/latest/"** — prominent, near the top.
- Optionally a short "Development" pointer to `AGENTS.md` and the contributing docs.
- Keep it well under 100 lines. If you find yourself explaining the base-image model, stop — that is `getting-started/how-it-works.md`.

**What is being removed from `README.md`, specifically** (the audit's line references, for orientation — verify against the current file):

- The title and opening line, which call the project an Ansible playbook.
- The whole "Other provisioning methods" section: `apt install ansible`, `cp group_vars/all.yml.example group_vars/all.yml`, `ansible-playbook -i localhost, --connection=local site.yml`, editing `inventory` to replace `CHANGE_ME`, `ansible-playbook -i inventory site.yml`.
- The Ansible variable table (it documents role defaults that `sand` overrides — it is actively misleading).
- Prose calling the product "the script" or "this playbook".
- Everything now covered by the site: the TUI keybindings, the secrets editor, the security model, the roles list, the release pipeline, the token lifecycle.

**`AGENTS.md`.** It is accurate today and it serves its audience (AI agents and human contributors); it is not being replaced. Update only what this plan makes wrong:

- The file/package map gains `docs/` and `mkdocs.yml`, and loses `README-sand.md`.
- The commands section gains the docs build/serve commands via `uvx`.
- The CI description gains the `docs.yml` workflow (strict build on PRs, mike deploy on main/tags).
- If it says "There is no Makefile" — that is still true, leave it.

**Do not touch `CHANGELOG.md`.** Its `new-vm.sh` entries are correct history of a script that did exist. Release-please owns the file and hand-edits corrupt it. The grep in the acceptance criteria excludes it for exactly this reason.

**Final check.** Run the grep from the acceptance criteria, and read the diff of `README.md` end to end before you finish. This task is where the work order's "clean up the stale docs" half either lands or doesn't.
</details>
