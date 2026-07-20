---
id: 2
group: "profiles"
dependencies: []
status: "completed"
created: 2026-07-20
model: "sonnet"
effort: "high"
complexity_score: 6
complexity_notes: "Small surface, but it is the credential-handling boundary — the secret-free profiles invariant and the 0600 permission check are security-sensitive, so the risk floor applies."
skills:
  - go
  - credential-handling
---
# profiles: add the proxmox profile type and secret-free token-file loading

## Objective

Extend `internal/profiles` with a `proxmox` profile type carrying the
connection fields the provider needs, and add loading of the API token from a
referenced **file path** so that `profiles.yaml` remains strictly secret-free.

## Skills Required

`go` (structs, YAML tags, validation) and `credential-handling` (file permission
checks, never logging secret material).

## Acceptance Criteria

- [ ] `go test ./internal/profiles/ -race` passes, including new tests that
      assert: a `proxmox` profile with no `host` is rejected; a `proxmox` profile
      with no `token_file` is rejected; two enabled proxmox profiles with the
      same host+node+pool are rejected as duplicate targets.
- [ ] Loading a token from a file whose mode is `0644` returns an error
      mentioning the permissions; mode `0600` succeeds. Verified by a test that
      creates both with `os.Chmod`.
- [ ] `grep -rn "token" internal/profiles/*.go` shows the token **value** is
      never stored on the `Profile` struct and never written to YAML — only
      `token_file` (a path) is persisted.
- [ ] A round-trip test writes a proxmox profile, reloads it, and confirms every
      field survives; the resulting YAML contains no token value.

## Technical Requirements

- Add `TypeProxmox Type = "proxmox"` to `internal/profiles/profiles.go`.
- New `Profile` fields, all `omitempty` and zero for other types:
  `Node string` (`node`), `Pool string` (`pool`), `Storage string` (`storage`),
  `Bridge string` (`bridge`), `TokenFile string` (`token_file`),
  `Insecure bool` (`insecure`), `CAFile string` (`ca_file`).
- Extend `validate` (`internal/profiles/store.go:324`) with a `TypeProxmox` case
  requiring non-empty `Host`, `Node`, `Pool`, and `TokenFile`.
- Extend the duplicate-target check: a proxmox profile's target identity is
  `"<host>:<node>/<pool>"`.
- New exported helper `LoadToken(path string) (string, error)`: expands a leading
  `~`, stats the file, **rejects any mode with group or world bits set**, reads
  it, trims surrounding whitespace/newline, and rejects an empty result. The
  error must never include the file contents.

## Input Dependencies

None.

## Output Artifacts

Updated `internal/profiles/profiles.go` and `store.go`, plus
`internal/profiles/token.go` — consumed by tasks 5 and 9.

## Implementation Notes

<details>

In `profiles.go`, add the type constant next to the existing ones and document
it in the same voice as the file's existing comments:

```go
// TypeProxmox is a Proxmox VE host reached over its REST API.
TypeProxmox Type = "proxmox"
```

Add the fields to `Profile` with a comment explaining the invariant:

```go
// Proxmox-only connection fields; zero for other types. Like IdentityPath,
// TokenFile is a PATH to a credential file, never credential material — the
// profiles store is secret-free and must stay that way, because these fields
// are folded into the registry scope that gets persisted.
Node      string `yaml:"node,omitempty"`
Pool      string `yaml:"pool,omitempty"`
Storage   string `yaml:"storage,omitempty"`
Bridge    string `yaml:"bridge,omitempty"`
TokenFile string `yaml:"token_file,omitempty"`
Insecure  bool   `yaml:"insecure,omitempty"`
CAFile    string `yaml:"ca_file,omitempty"`
```

Add a `proxmoxTarget()` method mirroring the existing `remoteTarget()`:

```go
func (p Profile) proxmoxTarget() string {
    return fmt.Sprintf("%s:%s/%s", p.Host, p.Node, p.Pool)
}
```

In `store.go`'s `validate`, add before the `default` case:

```go
case TypeProxmox:
    if p.Host == "" {
        return fmt.Errorf("profile %q: proxmox profile requires a host", p.ID)
    }
    if p.Node == "" {
        return fmt.Errorf("profile %q: proxmox profile requires a node", p.ID)
    }
    if p.Pool == "" {
        return fmt.Errorf("profile %q: proxmox profile requires a pool", p.ID)
    }
    if p.TokenFile == "" {
        return fmt.Errorf("profile %q: proxmox profile requires a token_file", p.ID)
    }
```

Then extend the duplicate-target block. The existing code only guards
`TypeRemoteSSH`; generalise it so a proxmox profile contributes
`p.proxmoxTarget()` to the same `seenTargets` map.

New file `internal/profiles/token.go`:

```go
// LoadToken reads a Proxmox API token from path. The token is deliberately NOT
// a Profile field: profiles.yaml is secret-free by design (see Profile), so the
// profile records only where the credential lives and this function is the one
// place it is read. A file readable by group or other is refused outright
// rather than warned about — a leaked API token is not a recoverable mistake.
func LoadToken(path string) (string, error) {
    expanded, err := expandHome(path)
    if err != nil { return "", err }
    fi, err := os.Stat(expanded)
    if err != nil { return "", fmt.Errorf("proxmox token file: %w", err) }
    if mode := fi.Mode().Perm(); mode&0o077 != 0 {
        return "", fmt.Errorf("proxmox token file %s has mode %04o; it must not be readable by group or other (chmod 600)", expanded, mode)
    }
    b, err := os.ReadFile(expanded)
    if err != nil { return "", fmt.Errorf("proxmox token file: %w", err) }
    tok := strings.TrimSpace(string(b))
    if tok == "" { return "", fmt.Errorf("proxmox token file %s is empty", expanded) }
    return tok, nil
}
```

Implement `expandHome` with `os.UserHomeDir()` for a leading `~/`. **Never**
include `b` or `tok` in any error or log line.

Write tests in `internal/profiles/token_test.go` using `t.TempDir()`, covering
both permission modes, the missing-file case, and the empty-file case. Skip the
permission test on Windows if the repo builds there (check existing build tags;
the repo already uses platform tags elsewhere).

</details>
