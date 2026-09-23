---
id: 12
group: "testing"
dependencies: [7, 11]
status: "pending"
created: 2026-09-12
model: "sonnet"
effort: "medium"
skills:
  - go
  - testing
---
# Unit-test manifest resolution, image acquisition and the config migration

## Objective

Cover the genuinely new logic introduced by this plan — architecture resolution, digest verification and its failure modes, cache behaviour, and the toolset config migration — with focused tests that exercise this project's own logic rather than the standard library's.

## Skills Required

`go` and `testing` — table-driven tests with an `httptest` server and temporary directories.

## Acceptance Criteria

- [ ] Manifest resolution is tested: a known `GOARCH` returns the right entry; an unsupported one returns a clear error rather than a default.
- [ ] Acquisition happy path is tested: a served fixture is downloaded, verified, and lands at the expected cache path.
- [ ] **Digest mismatch is tested**: a served body whose hash differs from the manifest produces an error, and **no file is left in the cache**.
- [ ] **Truncated download is tested**: a server that closes the connection mid-body produces an error and leaves no file in the cache that a later run would mistake for complete.
- [ ] Cache hit is tested: a second acquisition with a valid cached file performs **no** HTTP request.
- [ ] Corrupt cache is tested: a cached file whose contents no longer match the digest is detected, discarded and re-downloaded rather than used.
- [ ] Config migration is tested: a config with old toolset fields is rewritten without them and warns; an already-migrated config is left alone and does not warn; **unrelated fields survive the migration unchanged**.
- [ ] Verification: `go test ./internal/... -run 'Manifest|Acquire|Migrat' -v` passes with every case above visible in the output. Paste it.
- [ ] Verification: `go test ./... -race` passes and the `COVERAGE_FLOOR` gate holds. Paste the coverage summary.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Use `httptest.NewServer` for the download tests; no test may reach the real network.
- Use `t.TempDir()` for cache directories so tests are hermetic and parallel-safe.
- Follow the repo convention that no test requiring a real external target compiles without a build tag — these tests need no tag because they use fakes, and they must stay that way.
- Tests must not depend on the specific pinned image version in the generated manifest, or they will break on every image bump. Construct manifests in the test rather than importing the pinned one.

## Input Dependencies

- Task 07's manifest type and acquisition helper.
- Task 11's config migration.

## Output Artifacts

- Test files covering acquisition and migration — consumed by CI.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

**Test philosophy: "write a few tests, mostly integration."**

*Definition.* Meaningful tests verify custom business logic, critical paths, and edge cases specific to this application. Test *your* code, not the framework or library.

*When TO write tests:*
- Custom business logic and algorithms.
- Critical user workflows and data transformations.
- Edge cases and error conditions for core functionality.
- Integration points between components.
- Complex validation logic or calculations.

*When NOT to write tests:*
- Third-party library functionality.
- Framework features.
- Simple CRUD operations without custom logic.
- Trivial getters/setters or static configuration.
- Obvious functionality that would break immediately if incorrect.

*Test task creation rules:*
- Combine related test scenarios into a single task (e.g. "Test user authentication flow" not separate tasks for login, logout, validation).
- Favor integration and critical-path coverage over per-method unit tests.
- Avoid one test task per CRUD operation.
- Question whether simple functions need a dedicated test task.

**Applying that here.** Do not test that `http.Get` fetches bytes or that `sha256.Sum256` hashes them. Test the decisions *this* code makes on top of them: what happens when the digest disagrees, what happens when the body is short, whether a cached file is trusted without re-checking, and whether the migration preserves fields it does not own. Those are the places a real bug would hide, and three of them are security-relevant.

**The two highest-value tests** are the digest mismatch and the truncated download, because both are the difference between "fails safely" and "boots an unverified disk image". For the truncation case, an `httptest` handler that writes a `Content-Length` larger than the body it actually sends, or that panics mid-write to force a connection close, reproduces it. Assert on the *filesystem state* afterwards, not only on the returned error — the bug that matters is a partial file surviving in the cache under its final name, which a later run would accept as complete.

**For the migration**, the "unrelated fields survive" case is the one that protects real user data. Build a config containing both toolset fields and several unrelated ones (a connection profile, a disk size, a project setting — whatever the real format holds), migrate it, and assert every unrelated field is byte-identical afterwards. If the implementation round-trips through a typed struct, this test is what will catch silent field loss.

**Keep it proportionate.** This is roughly two test files. The plan does not call for a comprehensive suite over the provisioning layer, and task 10 already deleted a large body of tests covering removed behaviour; do not backfill replacements for those.

</details>
