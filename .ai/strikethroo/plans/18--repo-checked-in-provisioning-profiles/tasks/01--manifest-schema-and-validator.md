---
id: 1
group: "profile-schema"
dependencies: []
status: "pending"
created: 2026-07-17
model: "sonnet"
effort: "high"
skills:
  - ansible
  - python
complexity_score: 7
complexity_notes: "Defines the single source-of-truth schema every other component (shipped profiles, finalize stage, docs) reads; the validator is the malformed-input trust boundary between repo-supplied content and the guest."
---
# Define the `.sandbar/` manifest schema and standalone in-guest validator

## Objective

Establish the declarative schema for the `.sandbar/` provisioning-profile manifest (exactly five groups: `packages`, `services`, `roles`, `seed`, `toolset` — see plan section "Profile Schema and Guest-Side Validation") and implement a small, standalone, unit-testable validator that enforces it. The validator is the fast per-PR guard for the most common authoring mistake (a typo'd key or malformed value) and must run identically whether invoked against static CI fixtures or a real cloned checkout inside the guest.

## Skills Required

Ansible (playbook/embed conventions, where shipped scripts live) and a scripting language usable standalone and in-guest without new package dependencies (Python 3 using only the standard library is recommended, since the guest already requires Python 3 for Ansible's own module execution; a POSIX shell script is an acceptable alternative if it can express the required checks clearly — pick one and be consistent).

## Acceptance Criteria

- [ ] A manifest schema is documented (in code comments or a short schema doc) covering exactly the five groups named above, with no additional groups, no conditional/per-OS logic, and no profile inheritance (per the plan's Notes section scope guard).
- [ ] `packages`: list of apt package names, shape-checked (valid Debian package-name characters).
- [ ] `services`: list of systemd unit names, shape-checked.
- [ ] `roles`: list of role names expected to exist under `.sandbar/roles/<name>/` in the cloned checkout.
- [ ] `seed`: a single path (default location under `.sandbar/`) to a repo-supplied Ansible tasks file.
- [ ] `toolset`: list of known shipped-profile names (must match whatever names Task 2 establishes for the shipped profiles — coordinate the known-name list so it is trivially extendable, e.g. a single constant/list the validator reads).
- [ ] Unknown top-level keys are a validation error naming the offending key.
- [ ] A malformed value in any group fails with a clear, specific message naming the offending key/field (not a generic parse error).
- [ ] The validator is a self-contained script (no new package dependency required in the guest beyond what's already provisioned) shipped as playbook content — added to the `go:embed` list in `playbook_embed.go`, the in-guest rsync allowlist filter in `internal/provision/provision.go`, and covered by `TestGuestSyncCopiesOnlyThePlaybook` (coordinate with Task 2/3, which also touch this triple-pin — do not let this task's addition go unpinned even if Tasks 2/3 land the invocation wiring).
- [ ] A fixture corpus exists (e.g. under a `testdata/` path near the validator or the playbook) with at least: one fully valid manifest, and one deliberately-malformed manifest per failure mode above (unknown key, bad package name, bad unit name, bad toolset name, missing/invalid seed path).
- [ ] A Go test (placed to run in the `unit` CI job, no VM required) invokes the validator script directly against every corpus fixture and asserts: valid fixtures exit 0, each malformed fixture exits non-zero with a message naming the specific offending key/field. Run it with `go test ./... -race` and confirm it passes and is included in the coverage-instrumented package set.
- [ ] `ansible-playbook --syntax-check site.yml` still passes (no playbook syntax regressions from adding the embedded file).

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Read the plan's "Profile Schema and Guest-Side Validation" section (`.ai/strikethroo/plans/18--repo-checked-in-provisioning-profiles/plan-18--repo-checked-in-provisioning-profiles.md`) for the authoritative field-group list and rationale (why a standalone script, not inline Jinja).
- The validator takes a manifest file path as input and communicates success/failure via exit code, with the failure message on stderr or stdout naming the specific bad key/field.
- Do not build a host-side loader or parser — the validator's only production invocation path is inside the guest (wired by Task 3), against the cloned checkout. The CI unit test runs the same artifact against static fixtures on the runner; it does not reimplement validation logic in Go.
- Follow `internal/provision`'s existing convention (see `playbookContentHash`, `TestGuestSyncCopiesOnlyThePlaybook` in `internal/provision/playbooksync_test.go`) for how new embedded files must be added to all three of: `playbook_embed.go`'s embed directive, the rsync filter string in `internal/provision/provision.go` (`inGuestScript`), and the guard test fixture.

## Input Dependencies

None — this task establishes the contract other tasks build on.

## Output Artifacts

- The manifest schema definition (documented).
- The validator script, embedded in the playbook fileset.
- The fixture corpus (good + malformed samples).
- The Go unit test exercising the validator against the corpus.

## Implementation Notes

Test philosophy for this task: write a few tests, mostly integration. Meaningful tests verify custom business logic, critical paths, and edge cases specific to this application — test *your* code, not the framework. Favor one Go test that iterates the fixture corpus (table-driven) over one test per malformed case as separate test functions. This is exactly the "validator good/bad corpus" row from the plan's Verification Surface table — keep it a fast, VM-free `unit`/`lint` job citizen.

Follow RED → GREEN → REFACTOR: write one failing corpus-driven test first (e.g. against a hand-written malformed fixture), confirm it fails for the right reason, then implement just enough validator logic to pass it, then extend to the remaining fixtures.

Coordinate the `toolset` group's list of known names with whatever Task 2 (shipped provisioning profiles) calls its shipped profiles — if Task 2 has not landed yet when this task starts, use the four known names from the current optional tools (`claude`, `ddev`, `go`, `java`) as the initial known-list; Task 2/3 can extend it without touching this task's validator logic if the known-name list is a simple, easily-editable constant.
