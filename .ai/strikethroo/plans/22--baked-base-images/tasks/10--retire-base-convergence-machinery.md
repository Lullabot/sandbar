---
id: 10
group: "cleanup"
dependencies: [8, 9]
status: "pending"
created: 2026-09-12
model: "sonnet"
effort: "high"
complexity_score: 8
complexity_notes: "A large deletion across the core provisioning layer with subtle behavioural consequences; over-deleting breaks creates, under-deleting leaves dead code that re-triggers rebuilds users no longer expect."
skills:
  - go
  - refactoring
---
# Remove the base staleness and convergence machinery

## Objective

Delete the machinery that existed solely to manage a locally built, drifting base — staleness classification, in-place convergence, the 30-day apt self-refresh and toolset version merging — and replace it with a simple image-version equality check plus a rebuild-from-image path.

## Skills Required

`go` for the provisioning layer; `refactoring` for removing a large, entangled subsystem without breaking the paths that remain.

## Acceptance Criteria

- [ ] `ensureBaseStopped`'s four-outcome logic (`internal/provision/provision.go:645-736`) is reduced to: base exists and its stamped image version matches the pinned one → use it; otherwise → rebuild from image.
- [ ] `reapplyBase` (`:768-830`), `baseConvergeable` (`internal/provision/baseoverlay.go:131-157`), the 30-day `baseMaxAge` refresh (`:882`) and `mergeToolsetVersion` (`internal/provision/baseversion.go:396-411`) are removed.
- [ ] The "de-selected but remain installed… Rebuild the base to remove them" advisory (`internal/provision/provision.go:671-673`) and `shrunkTools` (`baseversion.go:308`) are removed.
- [ ] `PlaybookVersion` loses its toolset component; the base version stamp is the image version.
- [ ] The explicit rebuild path (the `--rebuild` flag's successor) still works and re-verifies the cached image rather than trusting it blindly.
- [ ] No dead code remains: the removed functions have no remaining callers, and their tests are removed or rewritten rather than left asserting deleted behaviour.
- [ ] Verification: `go build ./...` and `go vet ./...` pass. Paste the output.
- [ ] Verification: `go test ./...` passes, and the repo's `COVERAGE_FLOOR` gate still holds. Paste the coverage summary.
- [ ] Verification: `grep -rn "reapplyBase\|baseConvergeable\|mergeToolsetVersion\|shrunkTools\|baseMaxAge" internal/` returns no hits. Paste it.
- [ ] Verification: touch a file under `roles/base/`, run a create, and confirm **no** base rebuild and **no** image re-download occurs. Paste the output showing the base was reused.
- [ ] Verification: change the pinned image version (temporarily, in the generated manifest), run a create, and confirm the base **is** rebuilt from the new image. Paste the output, then revert.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- The base advisory lock (`internal/provision/baselock.go`) and its discipline in `prepareBaseAndClone` stay — serialization of base mutation is still required.
- `migrateLegacyBase` (`internal/provision/provision.go:569-627`), which renames a `claude-base` instance to `sandbar-base`, is unrelated to this change; leave it alone.
- The playbook fileset hash may still be needed for the **finalize** phase's own purposes. Check before removing `PlaybookVersion` wholesale — the requirement is that it no longer drives *base* staleness and no longer carries a toolset component.
- `TestGuestSyncCopiesOnlyThePlaybook` pins the playbook fileset across four locations (`playbook_embed.go:20`, `internal/provision/provision.go:60-63`, `internal/provision/baseversion.go:63-70`, `internal/provider/proxmoxprovision.go:759`). If this task touches `baseversion.go`'s `playbookFileset`, that test must still pass.
- Do not remove the tool-selection surface here — task 11 owns that. This task removes the *version-merging* consequences of toolsets, not the user-facing flags.

## Input Dependencies

- Task 08's rewired Lima base path.
- Task 09's rewired Proxmox base path.

## Output Artifacts

- A simplified provisioning layer — consumed by task 11 (toolset surface removal), task 12 (tests) and task 13 (CI assertions).

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

**Why all of this can go.** Every one of these mechanisms answers a question that no longer exists. The base used to be built from a playbook whose content hash could drift with every binary upgrade, so sand needed to classify *how* stale a base was and whether it could be repaired in place. With the base pinned to a released image, there is exactly one question — "is the instance I have built from the image I am pinned to?" — and exactly one remedy.

The specific mechanisms and why each dies:

- **`baseConvergeable`** compared a base instance's own `lima.yaml` playbook mount and bootstrap script against what the current create would render, to decide whether in-place re-application was possible. There is no in-place re-application any more.
- **`reapplyBase`** performed that re-application.
- **`mergeToolsetVersion`** existed because Ansible cannot uninstall: an in-place converge could only ever *add* tools, so the wanted version had to be merged with the union of the base's existing tools to stop de-selection from ping-ponging the shared base forever. With one image containing everything, there is nothing to merge.
- **`shrunkTools`** and the de-selection advisory were the user-facing half of that asymmetry.
- **`baseMaxAge`** (30 days) triggered an apt self-refresh, because a long-lived locally built base drifts from security updates. A published image is refreshed by the monthly CI build instead — the freshness problem moved upstream.

**The most important behavioural check** is the pair of verifications at the end of the acceptance criteria, which encode success criterion 4 of the plan: a `roles/base/` edit must **not** rebuild, and an image version change **must**. Those two are the observable definition of "we stopped constantly rebuilding base images on updates", which is half of the user's stated reason for doing this work at all. Do not sign this task off without running both.

**Deletion order that keeps the tree green.** Work outward from the callers: first simplify `ensureBaseStopped` to the two-outcome form, which orphans most of the rest; then delete the orphans; then delete or rewrite their tests. Running `go build ./...` between steps tells you what still references what. Resist the temptation to delete leaf functions first — you will spend the whole task fighting the compiler.

**Tests will need judgment.** `internal/provision` has roughly 5,900 lines of tests against 2,500 lines of code, much of it covering exactly the version-merging and staleness-classification behaviour being removed. Delete tests of deleted behaviour; do not contort them into testing something else. Where a test covers a *surviving* path incidentally, keep it. Watch the `COVERAGE_FLOOR` gate — removing well-tested code alongside its tests can move the ratio in either direction, and if the floor trips, adjust the committed floor with a note rather than writing filler tests.

**A subtlety about `PlaybookVersion`.** It is used for the base stamp, but check whether anything else (the finalize path, the Proxmox template version, PR #70's template provenance) reads it. The requirement is narrow: base staleness stops depending on the playbook hash, and the toolset component goes away. If a playbook hash is still genuinely useful somewhere, keep the function and change what the base stores.

</details>
