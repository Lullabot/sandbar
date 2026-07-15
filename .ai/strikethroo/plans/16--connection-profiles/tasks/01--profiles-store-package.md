---
id: 1
group: "profiles-store"
dependencies: []
status: "pending"
created: 2026-07-15
model: "sonnet"
effort: "medium"
complexity_score: 6
complexity_notes: "New package with YAML persistence, atomic-write discipline, seeding, and validation — single domain but several moving parts."
skills:
  - go
  - yaml
---
# Create the `internal/profiles` store package

## Objective
Create a new `internal/profiles` package that owns the persisted, secret-free
profile model that replaces the `SAND_*` environment variables — the single
source of truth for every location Sandbar can run VMs on. This is the
foundation every later task builds on (Component 1 of the plan).

## Skills Required
- `go` — new package, structs, XDG path resolution, atomic file writes.
- `yaml` — persistence via `gopkg.in/yaml.v3` (already a dependency).

## Acceptance Criteria
- [ ] A `Profile` type carries: an immutable `ID` (generated at creation, never mutated), a renameable `Name`, a `Type` (enum: `Local` or `RemoteSSH`, modeled so it can grow but with NO other variants implemented), an `Enabled` bool, and — for `RemoteSSH` — the connection fields that mirror `provider.TargetConfig` (host, user, port, identity **path**, remote LIMA_HOME). No secret/key material fields exist.
- [ ] A `Store` loads/saves `${XDG_CONFIG_HOME:-~/.config}/sandbar/profiles.yaml` using `yaml.v3`, with atomic write (temp file + `os.Rename`) and corrupt-file quarantine (rename to `.corrupt` on parse failure), mirroring `internal/registry/registry.go`'s discipline.
- [ ] On first load with no file present, the store **seeds a single enabled Local profile** (fixed reserved `ID`, default name e.g. `"local"`) and persists it, so an unconfigured sand behaves as today.
- [ ] The store records a **last-used profile pointer stored by `ID`** (not name), with getter/setter that survive a rename.
- [ ] `Enabled`/`disabled` is a persisted flag toggled without losing config. There is **no** persisted `error` field (error is a runtime state derived later, not here).
- [ ] Profile creation/edit **validates target uniqueness**: two enabled profiles must not resolve to the same `user@host:port`, and only **one** Local profile may exist. Attempting to violate returns a clear error.
- [ ] Unit tests cover: seed-on-empty, save→load round-trip, atomic write, corrupt-file quarantine, enable/disable toggle, last-used-by-ID survives a rename, duplicate-target rejection, second-Local rejection.
- [ ] `go test ./internal/profiles/... -race` passes; `go build ./...` succeeds. **No test requires a real limactl/ssh target.**

## Technical Requirements
- Package path: `internal/profiles`.
- Reuse `gopkg.in/yaml.v3` (already used for Lima instance-file parsing).
- Follow the XDG + atomic-write + `.corrupt` quarantine pattern in `internal/registry/registry.go` (see its `Load`, temp-file rename, and `os.Rename(path, path+".corrupt")` at ~line 175).
- The Local profile's fixed `ID` must be a stable constant so other packages can reference "the local profile" deterministically.
- ID generation for remote profiles: a short stable unique string (e.g. a random hex/UUID-like token). Do **not** derive the ID from the name (names are renameable) or from the target (targets are editable).

## Input Dependencies
None. This is a Phase 1 foundational task.

## Output Artifacts
- `internal/profiles/` package exposing the `Profile` type, `Type` enum, `Store` (Load/Save/List/Add/Update/Remove/Enable/Disable), last-used getter/setter, and target-uniqueness validation.
- Consumed by: task 4 (fleet builder), task 5 (CLI), task 8 (management screen), task 9 (create form).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- **Model shape.** Suggested:
  ```go
  type Type string
  const ( TypeLocal Type = "local"; TypeRemoteSSH Type = "remote-ssh" )
  const LocalProfileID = "local" // fixed reserved id for the permanent Local profile

  type Profile struct {
      ID      string `yaml:"id"`
      Name    string `yaml:"name"`
      Type    Type   `yaml:"type"`
      Enabled bool   `yaml:"enabled"`
      // Remote SSH only; zero for Local:
      Host        string `yaml:"host,omitempty"`
      User        string `yaml:"user,omitempty"`
      Port        int    `yaml:"port,omitempty"`
      IdentityPath string `yaml:"identity_path,omitempty"`
      LimaHome    string `yaml:"lima_home,omitempty"`
  }
  ```
  The on-disk file wraps a version, the profile list, and the last-used id:
  ```go
  type fileSchema struct {
      Version  int       `yaml:"version"`
      LastUsed string    `yaml:"last_used,omitempty"` // profile ID
      Profiles []Profile `yaml:"profiles"`
  }
  ```
- **Secret-free invariant is load-bearing** (see plan Notes): identity is a key **path**, never key material. Do not add any field that holds a secret.
- **Atomic write:** marshal to bytes, write to `profiles.yaml.tmp` in the same dir, `os.Rename` over the target. Create the `sandbar` config dir with `0o755` and the file `0o644` (it is secret-free and shareable).
- **Corrupt quarantine:** on `yaml.Unmarshal` error during load, `os.Rename(path, path+".corrupt")` and then seed a fresh default (log/return a soft signal as registry does), so a mangled file never bricks startup.
- **Seeding:** `Load` on a missing file returns a store containing exactly one Local profile (enabled, `ID = LocalProfileID`) and immediately persists it.
- **Validation:** expose e.g. `func (s *Store) validate() error` run on every mutating op: reject a second `TypeLocal`; reject a duplicate `user@host:port` among enabled RemoteSSH profiles. Compute the target string the same way task 4 will (`user@host:port`) so the two agree — a tiny helper `func (p Profile) remoteTarget() string` shared conceptually with `provider.TargetConfig.Scope()` (do not import provider here to avoid a cycle; duplicate the trivial `fmt.Sprintf("%s@%s:%d", user, host, port)` and add a comment pointing at `provider.TargetConfig.Scope()` in select.go:86 as the source of truth).
- Keep the package free of any dependency on `internal/provider` or `internal/ui` to avoid import cycles — later tasks convert a `Profile` into a `provider.TargetConfig` in the provider layer, not here.
</details>
