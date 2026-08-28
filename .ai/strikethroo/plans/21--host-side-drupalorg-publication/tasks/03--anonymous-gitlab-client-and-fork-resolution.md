---
id: 3
group: "drupalorg-core"
dependencies: []
status: "pending"
created: 2026-08-28
model: "sonnet"
effort: "high"
skills:
  - go
  - rest-api
complexity_score: 7
complexity_notes: "A typed HTTP client plus the convention-based fork derivation and the blocked-endpoint detection that the plan requires be reported honestly rather than as a generic 404."
---
# Anonymous GitLab client and fork resolution by convention

## Objective

Give `internal/drupalorg` a `net/http` client for git.drupalcode.org and the
**credential-free** half of publication: derive an issue fork's path from a
module name and issue number, confirm it exists, list its branches, read its
canonical parent from `forked_from_project`, and look up whether a merge
request is already open for a source branch. Recognise a drupal.org edge
refusal — an HTML body where JSON was expected — and report it as such.

## Skills Required

`go` and `rest-api`: a typed client on the pattern `internal/pve` already
establishes, plus GitLab REST semantics for projects, branches, and merge
requests.

## Acceptance Criteria

- [ ] A `Client` type wraps `*http.Client` with a base URL of
      `https://git.drupalcode.org/api/v4`, injectable for tests, following
      `internal/pve/client.go`'s shape (config struct, `New`, one request
      helper that decodes JSON).
- [ ] Every request builds its URL with `url.PathEscape` on the project path,
      so `issue/drupal-3181657` becomes `issue%2Fdrupal-3181657`. Guest-derived
      text is never concatenated into a URL or a command string; a test feeds a
      module name containing `/`, `..`, and shell metacharacters and asserts
      the resulting request path.
- [ ] `ForkPath(module, issue)` returns `issue/<module>-<nid>` per drupal.org's
      documented `PROJECT-ISSUE_NUMBER` convention, and rejects a module name
      or issue number that is not of the expected shape rather than building a
      path from it.
- [ ] A function derives the module name from **a checkout's origin remote
      URL** (the checkout being targeted), handling both HTTPS and SSH forms
      of `git.drupalcode.org/project/<module>.git`. A comment records that the
      module must **never** be derived from the VM's create-time `CloneURL`,
      and why: that value is recorded once per VM and identifies only the
      first module in a multi-module VM.
- [ ] `Project(ctx, path)` returns the project's `id`, `default_branch`, and
      `forked_from_project` (`id`, `path_with_namespace`, `default_branch`),
      using no credential.
- [ ] `Branches(ctx, path)` lists branch names, and a helper reports whether a
      named branch exists — the check that decides whether `start_branch` is
      needed later.
- [ ] `OpenMergeRequest(ctx, parentPath, sourceProjectID, sourceBranch)`
      returns the existing open merge request or nil. It queries the
      **canonical parent**, not the fork: an issue fork holds no merge
      requests, so listing on the fork returns `[]`.
- [ ] A blocked-endpoint response — a non-JSON, HTML body, characteristically a
      large 404 page from the drupal.org Drupal site — returns a distinct,
      `errors.Is`-checkable error saying drupal.org refused the request and
      naming the endpoint, rather than a JSON decode error or a plain 404.
- [ ] All tests run against `httptest.Server` fixtures; no test contacts
      git.drupalcode.org. `go test ./internal/drupalorg/... -race` passes.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Stdlib only: `net/http`, `net/url`, `encoding/json`. No `glab`, no `gh`, no
  git, and no shell-out of any kind — success criterion 4 says the host needs
  no Drupal tooling and no git, and criterion 15 says no forge CLI.
- Anonymous calls must send no `PRIVATE-TOKEN` header at all. Authentication
  belongs to task 5 and to nothing here.
- Follow `internal/pve/client.go` for the config/New/do-request shape and for
  its habit of encoding the semantics that matter in typed errors.

## Input Dependencies

None. Task 5 consumes this client; task 4 consumes the resolution results.

## Output Artifacts

- `internal/drupalorg/client.go` — the typed client, the request helper, and
  the blocked-endpoint detection.
- `internal/drupalorg/fork.go` — `ForkPath`, module derivation from a remote
  URL, project/branch/merge-request reads.
- Tests for both, driven by `httptest` fixtures.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

The plan's "Fork resolution by convention" section is the contract, and its
Background section records what was verified rather than assumed:

- `ls-remote` against `git.drupalcode.org/issue/drupal-3181657.git` succeeded
  with no credential. Existence and branch lists are anonymously readable.
- GitLab's project endpoints accept a URL-encoded project path, so **no
  project-ID lookup is needed** for the fork.
- `GET /projects/issue%2Fdrupal-3181657` returns
  `forked_from_project: {path_with_namespace: "project/drupal", id: 59858}`
  anonymously. The merge request's target is therefore host-derived, never
  guest-supplied.
- Merge requests are cross-project on drupal.org. On `project/dubbot` the
  recent MRs run `source_project_id` 241450 -> `target_project_id` 106348 with
  `target_branch` `2.x`. Listing MRs on the issue fork itself returns `[]`.

For the open-MR lookup, query the parent with the source project and branch,
for example
`GET /projects/<parentEnc>/merge_requests?state=opened&source_branch=<branch>`
and filter on `source_project_id`. Write down in a comment why the fork is the
wrong project to ask.

Blocked-endpoint detection: the plan records that a blocked request "returns a
byte-identical 56 KB HTML 404 from the drupal.org Drupal site, on every project
tried, authenticated or not", while a routed request returns GitLab JSON. So
the discriminator is content type and body shape, not status code alone. Check
`Content-Type` for `text/html` (and, as a fallback, a body that begins with
`<!DOCTYPE` or `<html` after whitespace) and return something like:

```go
var ErrDrupalOrgRefused = errors.New("drupal.org refused this request")
```

wrapped with the method and path. The plan calls this out twice — as a risk
mitigation and as success criterion 10 — because "a generic API error or a
misleading 404" would send someone hunting for a bug in sand.

`ForkPath` should validate rather than sanitise, matching task 1's posture: a
module name is `[a-z0-9_]+` on drupal.org and an issue number is digits. A
module name containing a `/` is not something to escape and carry on with —
it means the derivation went wrong.

`internal/checkouts.Checkout` already records `Forge` and `OrgRepo` from the
guest sweep, which is where the origin remote is available; the module
derivation helper should accept a remote URL string so it is testable without
a VM, and callers pass the checkout's remote.

**Test philosophy — write a few tests, mostly integration.** Meaningful tests
verify custom business logic, critical paths, and edge cases specific to this
application. Test *your* code, not the framework or library. Write tests for:
custom business logic and algorithms; critical user workflows and data
transformations; edge cases and error conditions for core functionality;
integration points between components; complex validation logic. Do **not**
write tests for: third-party library functionality; framework features; simple
CRUD without custom logic; trivial getters/setters; obvious functionality that
would break immediately if incorrect. Combine related scenarios into one test
rather than one per method; favour integration coverage.

Here the meaningful tests are: the URL-escaping of a hostile project path, the
blocked-endpoint HTML detection, `forked_from_project` parsing, the branch
existence check, and the open-MR lookup hitting the parent rather than the
fork. A `httptest` handler that records the request path and serves recorded
JSON covers most of them at once.

</details>

### Per-task completion gate (required by the plan)

This task is not complete until, after its own tests pass:

1. `/code-review --fix` has run against this task's changes and every finding
   was applied or consciously rejected.
2. `/simplify` has then run, and its findings applied or consciously rejected.
3. The task's tests were **re-run** afterwards and pass.
