---
id: 2
group: "drupalorg-core"
dependencies: []
status: "completed"
created: 2026-08-28
model: "sonnet"
effort: "high"
skills:
  - go
  - unit-testing
complexity_score: 6
complexity_notes: "Small in code, but it is the one credential read site in the design and its file-mode refusal is a security control rather than a nicety."
---
# Host-side drupal.org PAT loader at a conventional path

## Objective

Give `internal/drupalorg` the single read site for the workstation's
account-level drupal.org personal access token: a fixed conventional path
`${XDG_CONFIG_HOME:-~/.config}/sandbar/drupalorg.token`, a hard refusal of any
file readable by group or other, and a distinguishable "no token, publication
unavailable" outcome that both surfaces can report **up front** rather than
mid-publish.

## Skills Required

`go` for the loader; `unit-testing` for the file-mode refusal and the
absent-file path, which are the security-relevant behaviours.

## Acceptance Criteria

- [ ] An exported function returns the token path, honouring `XDG_CONFIG_HOME`
      when set and non-empty and falling back to `~/.config` otherwise, and
      joining `sandbar/drupalorg.token`.
- [ ] The loader refuses outright — returns an error, does not warn and
      continue — when the file's permission bits have any group or other bit
      set (`mode&0o077 != 0`), naming the path and the offending mode and
      saying `chmod 600`.
- [ ] An absent file returns a distinguishable sentinel error (checkable with
      `errors.Is`) meaning "publication is unavailable", not a generic I/O
      error, so a surface can say so before doing any work.
- [ ] An empty or whitespace-only file is an error. The token value never
      appears in any error message, log line, or `String()` output; a test
      asserts the token text is absent from every error returned.
- [ ] The loader trims surrounding whitespace from the token value.
- [ ] A package-level doc comment states explicitly that this credential must
      **not** live in `internal/secrets`, and why: that package exists to
      deliver secrets *into* guests, which is the one thing this design
      forbids. The comment is the guard against a future one-line change that
      looks correct.
- [ ] `go test ./internal/drupalorg/... -race` passes, including a test that
      creates files at modes 0600, 0640, 0604, and 0666 and asserts the first
      is accepted and the rest refused.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- Follow `internal/profiles/token.go` in *intent* — one loader, a hard refusal
  of over-permissive modes, and no secret in configuration. Do **not** add a
  config field: the path is a convention, not a configured value, and
  `profiles.yaml` stays untouched.
- `internal/profiles.ExpandHome` already exists for the `~/` expansion and
  should be reused rather than re-written; `os.UserHomeDir` is the fallback if
  importing `profiles` from `drupalorg` would create an awkward coupling —
  decide and say which in a comment.
- Mode tests must skip on Windows, where permission bits do not carry the same
  meaning (`runtime.GOOS == "windows"`).

## Input Dependencies

None.

## Output Artifacts

- `internal/drupalorg/token.go` — the path convention and the loader.
- `internal/drupalorg/token_test.go` — mode refusal matrix, absent-file
  sentinel, empty-file error, and the "token never appears in an error" test.

## Implementation Notes

<details>
<summary>Detailed guidance</summary>

The plan's "The host-side account PAT" section is the contract, and the
decision it records is a deliberate *narrowing* of what an earlier revision
said. `internal/profiles/token.go`'s `TokenFile` is a field on a **Proxmox
connection profile**, and `profiles.yaml` is a per-location store — so a
workstation-global credential that has nothing to do with where VMs run has no
coherent home there. A new global config file was rejected under the plan's
scope-control hook: it would introduce a persisted schema, its versioning, and
a TUI editing surface for a single string. A convention has nothing to
mis-record and nothing to migrate.

Shape:

```go
// TokenPath returns the conventional location of the workstation's
// drupal.org account PAT. It is a convention rather than a configured value
// — see the package doc for why there is deliberately no config key.
func TokenPath() (string, error) { ... }

// ErrNoToken reports that no PAT file exists, which means publication is
// unavailable rather than that something went wrong.
var ErrNoToken = errors.New("...")

func LoadToken() (string, error) { ... }
```

Model the mode refusal on `profiles.LoadToken` verbatim in intent, including
its reasoning that "a leaked API token is not a recoverable mistake".

The "absence disables publication with a clear message rather than failing
mid-publish" requirement (success criterion 2) is why `ErrNoToken` must be a
sentinel: the CLI and the TUI both need to check for it cheaply *before*
collecting a change set or resolving a fork, not discover it at the first
authenticated call.

**Test philosophy — write a few tests, mostly integration.** Meaningful tests
verify custom business logic, critical paths, and edge cases specific to this
application. Test *your* code, not the framework or library. Write tests for:
custom business logic and algorithms; critical user workflows and data
transformations; edge cases and error conditions for core functionality;
integration points between components; complex validation logic. Do **not**
write tests for: third-party library functionality; framework features; simple
CRUD without custom logic; trivial getters/setters or static configuration;
obvious functionality that would break immediately if incorrect. Combine
related scenarios into one test rather than one per method.

Here the mode matrix and the sentinel are the meaningful cases. Use
`t.TempDir()` and `t.Setenv("XDG_CONFIG_HOME", ...)` so no test touches the
developer's real `~/.config`.

</details>

### Per-task completion gate (required by the plan)

This task is not complete until, after its own tests pass:

1. `/code-review --fix` has run against this task's changes and every finding
   was applied or consciously rejected.
2. `/simplify` has then run, and its findings applied or consciously rejected.
3. The task's tests were **re-run** afterwards and pass.
