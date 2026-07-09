---
id: 1
group: "secrets-core"
dependencies: []
status: "completed"
created: 2026-07-09
model: "sonnet"
effort: "high"
skills:
  - go
  - secure-storage
---
# Host Secrets Store Package (`internal/secrets`)

## Objective
Create the host-side, per-VM secrets store that is the single source of truth for all secrets, persisted next to the existing managed-VM registry, with correct permissions and value redaction. This is the foundation every other task builds on.

## Skills Required
- **go** — a new `internal/secrets` package: types, JSON (de)serialization, atomic file writes, permission enforcement.
- **secure-storage** — plaintext-at-rest with `0700` dir / `0600` file, values never logged, redaction for display.

## Acceptance Criteria
- [ ] `go build ./...` and `go test ./internal/secrets/...` pass.
- [ ] A unit test writes a store via the package, then asserts (via `os.Stat`) the file mode is exactly `0600` and its parent dir is `0700`.
- [ ] A round-trip unit test: save a store containing a global secret, a GitHub scoped token, and a directory-scoped env var; reload; assert deep-equality.
- [ ] A redaction unit test: the display/list helper returns masked values (e.g. `****`) and never the cleartext.
- [ ] The store path resolves to `${XDG_DATA_HOME:-$HOME/.local/share}/sandbar/secrets/<vm-name>.json` (assert with `XDG_DATA_HOME` set in the test).

## Technical Requirements
- Mirror the path convention in `internal/registry/registry.go` (`defaultPath()` uses `${XDG_DATA_HOME:-$HOME/.local/share}/sandbar/...`). Put secrets under a `secrets/` subdir, one file per VM.
- Writes must be atomic (write temp + `os.Rename`) and set the mode on create; ensure the parent dir is `0700`.
- No secret value may reach any log; provide an explicit `Redacted()`/mask helper for callers that display.

## Input Dependencies
None.

## Output Artifacts
- `internal/secrets/` package exposing: the store type + the three secret categories, `Load(vm string)`, `Save(...)`, mutators to add/remove a global / github / dir_env secret, and a redaction/list helper.
- The canonical JSON schema and the Go types other tasks import.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

Define the on-disk JSON schema exactly as (this is the contract tasks 3, 4, 5, 6 depend on):

```json
{
  "version": 1,
  "global":  [ { "name": "MY_VAR", "value": "..." } ],
  "github":  [ { "scope": "github.com/acme", "token": "..." } ],
  "dir_env": [ { "scope": "github.com/acme", "name": "SOME_VAR", "value": "..." } ]
}
```

Semantics:
- `global` — VM-wide environment variables.
- `github` — a GitHub token bound to a home-relative directory `scope`. An empty `scope` (or `.`) means the VM-wide default GitHub token; a non-empty `scope` (e.g. `github.com/acme`) is an org/subtree override.
- `dir_env` — a generic environment variable scoped to a home-relative directory `scope`.

Suggested Go shape:
```go
type Store struct {
    Version int            `json:"version"`
    Global  []GlobalSecret `json:"global"`
    GitHub  []GitHubSecret `json:"github"`
    DirEnv  []DirEnvSecret `json:"dir_env"`
}
```
Provide: `Load(vm string) (*Store, error)` (missing file → empty store, not an error), `(*Store) Save(vm string) error` (atomic, `0600`, ensure `0700` dir), add/remove helpers keyed by (category, scope, name), and a `Redact()` that returns a copy or a display list with values masked.

Follow the registry's existing style for path resolution and `os.MkdirAll(dir, 0o700)`. Do **not** touch `managed-vms.json` — the registry must stay secret-free; this is a sibling store.

Keep the package free of any `fmt.Print`/log of values.
</details>
