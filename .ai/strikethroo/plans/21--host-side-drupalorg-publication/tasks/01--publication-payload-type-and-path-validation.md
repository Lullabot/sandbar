---
id: 1
group: "drupalorg-core"
dependencies: []
status: "completed"
created: 2026-08-28
model: "sonnet"
effort: "high"
skills:
  - go
  - unit-testing
complexity_score: 7
complexity_notes: "The payload type carries the plan's central structural security claim — it must be incapable of expressing a destination — and its path validation must refuse hostile input rather than normalise it."
---
# Publication payload type and repository-path validation

## Objective

Create `internal/drupalorg` and give it the publication payload type: an
ordered list of commits, each carrying its message, its author name and email,
and an ordered list of file actions with an explicit kind and encoding. The
type must be **structurally incapable of naming a destination**, and its paths
must be validated as repository-relative with traversal refused rather than
normalised.

## Skills Required

`go` for the type design and validation logic; `unit-testing` for the hostile
path cases, which are the point of the task rather than an afterthought.

## Acceptance Criteria

- [ ] Package `internal/drupalorg` exists with a package doc that states the
      guest-decides-content / host-decides-destination split and says plainly
      that the payload type carries no destination.
- [ ] A `Commit` type carries `Message`, `AuthorName`, `AuthorEmail`, and an
      ordered `[]FileAction`. A `ChangeSet` (or equivalent) is an ordered
      `[]Commit`.
- [ ] `FileAction` carries an explicit `Kind` — create, update, delete, move —
      a repository-relative `Path`, a `PreviousPath` for move, and, for the
      kinds that have one, `Content` with an `Encoding` of text or base64.
      A delete carries no content and a move carries both paths.
- [ ] No type in the payload has any field naming a project, branch, remote,
      URL, or host. A test asserts this by reflection over the payload types'
      exported fields, so a later field addition fails the tests rather than
      passing review unnoticed.
- [ ] Path validation refuses: absolute paths, any path containing a `..`
      element, any path that normalises outside the tree, an empty path, and
      a path with a leading `./` or a drive letter. Refusal returns an error
      naming the offending path; the function never returns a "corrected"
      path.
- [ ] An encoding helper chooses text for valid UTF-8 content and base64
      otherwise, and round-trips binary content unchanged.
- [ ] A size helper reports a commit's encoded byte size so callers can apply
      the content API's 20 MB rate-limit and 300 MB rejection thresholds
      before sending.
- [ ] `go test ./internal/drupalorg/... -race` passes and `go build ./...`
      succeeds.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Go 1.25, stdlib only. Follow the repository's existing doc-comment density:
  types here carry comments that say *why*, not only *what*.
- Use `path` (not `filepath`) semantics for repository-relative paths so
  behaviour does not vary by host OS — a repository path is always
  slash-separated.
- Base64 is `encoding/base64` StdEncoding, matching what GitLab's content API
  expects for `encoding: base64`.
- The validation function must be exported so both surfaces and the publish
  client call the same one.

## Input Dependencies

None. This is the first task and defines the type every later task consumes.

## Output Artifacts

- `internal/drupalorg/payload.go` — the payload types, path validation, the
  encoding chooser, and the size helper.
- `internal/drupalorg/payload_test.go` — hostile path cases, the
  no-destination reflection test, binary round-trip, and size accounting.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

The plan's Architectural Approach section, under "The publication payload", is
the contract. Two of its points corrected earlier defects and must not be
re-broken:

1. An earlier revision described the payload as "paths and their resulting
   contents". That cannot express a **deletion** (no content) or a **rename**
   (two paths), though GitLab's content API has `delete` and `move` actions.
   The explicit `Kind` is what fixes this. A change set that removes a file
   must be expressible.
2. The payload must carry **no destination field of any kind** — no project,
   no branch, no remote, no URL. The plan states this must be "enforced by the
   type, not by convention: if the payload cannot express a destination, no
   amount of prompt injection or agent compromise can supply one."

Authorship is the one guest-supplied field that reaches drupal.org verbatim,
and that is deliberate: replaying a commit without its author would rewrite
it. Say so in the doc comment. An author name is not a destination and cannot
redirect a write; the committer of record is the PAT owner regardless.

Suggested shape:

```go
type ActionKind string

const (
    ActionCreate ActionKind = "create"
    ActionUpdate ActionKind = "update"
    ActionDelete ActionKind = "delete"
    ActionMove   ActionKind = "move"
)

type Encoding string

const (
    EncodingText   Encoding = "text"
    EncodingBase64 Encoding = "base64"
)

type FileAction struct {
    Kind         ActionKind
    Path         string // repository-relative, validated
    PreviousPath string // move only
    Content      string // empty for delete
    Encoding     Encoding
}

type Commit struct {
    Message     string
    AuthorName  string
    AuthorEmail string
    Actions     []FileAction
}

type ChangeSet struct {
    Commits []Commit
}
```

For validation, reject before normalising:

```go
func ValidateRepoPath(p string) error {
    if p == "" { ... }
    if strings.HasPrefix(p, "/") { ... }
    if len(p) > 1 && p[1] == ':' { /* windows drive letter */ }
    for _, seg := range strings.Split(p, "/") {
        if seg == ".." { ... }
    }
    if path.Clean(p) != p { /* refuse rather than accept the cleaned form */ }
    ...
}
```

Refusing `path.Clean(p) != p` also catches `a//b`, `./a`, and trailing
slashes. The plan is explicit that "silently correcting a hostile path is how
a payload ends up writing somewhere unintended" — so return an error, never a
rewritten path.

The no-destination test is the interesting one. Walk the exported fields of
`FileAction`, `Commit`, and `ChangeSet` with `reflect` and fail on any field
whose name matches a destination-ish set (`Project`, `Branch`, `Remote`,
`URL`, `Host`, `Target`, `Namespace`, `Fork`, `Repo`). It guards future edits,
so its failure message should explain why the field is forbidden rather than
only that it exists.

Validate that a `move` carries both `Path` and `PreviousPath`, and validate
both.

**Test philosophy — write a few tests, mostly integration.** Meaningful tests
verify custom business logic, critical paths, and edge cases specific to this
application. Test *your* code, not the framework or library. Write tests for:
custom business logic and algorithms; critical user workflows and data
transformations; edge cases and error conditions for core functionality;
integration points between components; complex validation logic. Do **not**
write tests for: third-party library functionality; framework features; simple
CRUD without custom logic; trivial getters/setters or static configuration;
obvious functionality that would break immediately if incorrect. Combine
related scenarios into one test rather than one per method; favour integration
and critical-path coverage over per-method unit tests.

Here the path validation and the no-destination invariant *are* the custom
logic, so they get real table-driven coverage. Getters do not.

</details>

### Per-task completion gate (required by the plan)

This task is not complete until, after its own tests pass:

1. `/code-review --fix` has run against this task's changes and every finding
   was applied or consciously rejected.
2. `/simplify` has then run, and its findings applied or consciously rejected.
3. The task's tests were **re-run** afterwards and pass — both commands modify
   the working tree, so an earlier green result is not evidence.
