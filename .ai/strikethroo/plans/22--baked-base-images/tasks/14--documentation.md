---
id: 14
group: "documentation"
dependencies: [11, 16, 17]
status: "pending"
created: 2026-09-12
models:
  anthropic: "claude-sonnet-5"
  openai: "gpt-6-sol"
effort: "medium"
skills:
  - technical-writing
---
# Update the documentation for the baked-image model

## Objective

Bring the documentation site and contributor docs in line with the new model: first create is a download-and-clone, shared-base dependency checkboxes are gone, coding agents remain per-VM choices, the base is a pinned published artifact, and user-created golden templates remain a separate layer above it.

## Skills Required

`technical-writing` — the changes are substantive rewrites of conceptual pages, not find-and-replace.

## Acceptance Criteria

- [ ] `docs/getting-started/how-it-works.md` is rewritten around download-and-clone plus finalize, including a corrected mermaid diagram. While rewriting, fix the two statements that are **already stale** independent of this plan: that finalize runs `apt upgrade`, and that the VM always restarts at the end of finalize (it is now conditional on `/var/run/reboot-required`).
- [ ] `docs/contributing/ansible-playbook.md` narrows the working-tree-edit promise (lines 35-38) explicitly to the **finalize** phase, and points at the local image build script for base-role work.
- [ ] `docs/contributing/releases.md` gains a section on image releases: the `base-image-YYYY.MM.DD` tag namespace, the draft-then-publish dance forced by immutable releases, and how to bump the pinned manifest. (It does not mention `base-image.yml` at all today.)
- [ ] `docs/using-sand/cli-reference.md` drops the DDEV/Go/Java selection flags and the "configures the SHARED base image" note, preserves the four agent flags, updates `--rebuild` semantics, and refreshes pasted help.
- [ ] `docs/getting-started/available-tools.md` distinguishes always-present base dependencies from opt-in per-VM coding agents (Claude Code, Codex, OpenCode, Pi).
- [ ] Agent documentation states that selected Claude Code and Codex commands install their current release on first invocation, while OpenCode and Pi retain their existing install timing.
- [ ] `docs/getting-started/first-vm.md` replaces "the first VM builds a shared base image, which can take a while" with the one-time image download.
- [ ] `docs/using-sand/proxmox.md` updates the `base_image` row and the "why the default image is a project-built one" admonition, now that both providers work this way.
- [ ] `docs/reference/troubleshooting.md` updates the stale-base and `--rebuild` guidance and adds image download/verification failure modes (digest mismatch, interrupted download, unsupported architecture).
- [ ] `docs/reference/files-and-state.md` documents the image cache location and the changed base-version stamp.
- [ ] `docs/using-sand/golden-templates.md` replaces playbook/toolset freshness wording with baked-image-version provenance, explains that template create/reset bypass the shared base, and uses “published base image” versus “golden template” consistently.
- [ ] `docs/reference/security-model.md` gains the image supply-chain posture: what is baked in, what is generalized per VM (machine-id, SSH host keys, locked password), and how the download is verified.
- [ ] `AGENTS.md` documents the published-base image stamp, provider-aware acquisition, preserved per-VM agent lifecycle, and golden-template image lineage. These invariants change in this plan, so the update is required.
- [ ] Verification: `mkdocs build --strict` succeeds with no warnings. Paste the output.
- [ ] Verification: every code block, flag and path quoted in the changed pages is checked against the actual binary and tree — run `sand create --help` and confirm the pasted help text matches, and confirm each referenced file path exists. Paste the comparison.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- `mkdocs build --strict` runs in CI on PRs; a broken internal link or missing nav entry fails the build.
- The docs are versioned with `mike` and published to `gh-pages`; do not change the versioning setup.
- Do not document the per-repo user-Ansible idea — it is explicitly out of scope for this plan and documenting it would promise something that does not exist.

## Input Dependencies

- Task 11's removed base-tool surface and preserved agent selection.
- Task 16's selected-agent first-use behavior.
- Task 17's golden-template image provenance.
- Task 03's build script and task 07's cache location, both of which are referenced by the docs.

## Output Artifacts

- Updated user/contributor documentation plus the required `AGENTS.md` invariant changes.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

**The hardest page is `how-it-works.md`.** It is 49 lines and essentially *is* a description of the two-pass local build, mermaid diagram included. Rewrite it rather than patching it. The new story is short and clearer than the old one: sand downloads one verified image (once per machine, per image version), creates a stopped base instance from it, and clones every VM from that base; each clone then runs a short finalize pass in-guest that applies the things that must be per-VM — hostname, git identity, timezone, tmux config, and an optional project clone.

**Be honest about the contributor regression.** `docs/contributing/ansible-playbook.md:35-38` currently promises that running `go run ./cmd/sand` from inside a checkout makes uncommitted playbook edits take effect on the very next provision. That promise survives for **finalize** and dies for **base**. Do not soften this into vagueness. State plainly that base-role changes now require building an image locally with `scripts/build-base-image.sh`, and link to it. A contributor who discovers this by spending an hour wondering why their edit did nothing is worse served than one who reads it here.

**Keep the two selection classes distinct.** DDEV, Go, and Java become fixed image content. Coding agents do not: `main` installs them per VM during finalize and remembers the user's selection. Documentation that says “all tools are always present” without that distinction would regress the newly landed generic-agent lifecycle.

**Avoid the overloaded word “golden.”** The distributed artifact is the published base image; “golden template” is the user-created snapshot feature exposed by `sand template`. `golden-templates.md` must describe freshness using the image version while keeping those concepts separate.

**`security-model.md` is new ground.** The trust story genuinely changed: it used to be "we run a playbook on your machine that you can read"; it is now "we hand you a disk image we built". Cover what makes that safe — builds run from a committed, reviewable script against upstream Debian genericcloud; every download is SHA-256 verified against a digest pinned in the binary; a mismatch is a hard failure with no fallback; images ship with no user password, no SSH host keys and an empty machine-id, so nothing identity-bearing is shared between users. Also state what is *not* claimed (the images are not reproducible builds, and are not signed beyond the checksum) rather than implying stronger guarantees than exist.

**Troubleshooting is where the new failure modes land.** The old entries about a stale base and `--rebuild` largely go away. The new ones users will actually hit: a digest mismatch (what it means, what to do), an interrupted download (the cache is self-healing — a corrupt cached file is detected and re-downloaded), an unsupported architecture, and a full disk when the image cache plus the base plus clones add up.

**Verify rather than assume.** The acceptance criteria require checking quoted help text and file paths against reality. `cli-reference.md` is 548 lines and contains pasted help output that will be stale the moment task 11 lands; regenerate it from the actual binary rather than hand-editing.

</details>
