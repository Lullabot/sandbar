---
id: 9
group: "wiring"
dependencies: [2, 5]
status: "pending"
created: 2026-07-20
model: "sonnet"
effort: "medium"
skills:
  - go
---
# wiring: select the Proxmox provider from a profile in the fleet builder and CLI

## Objective

Make a `proxmox` profile actually construct a Proxmox provider, in both the
fleet builder used by the TUI and the single-profile resolution path used by the
CLI.

## Skills Required

`go` (switch extension, keeping two deliberately-duplicated code paths in
agreement).

## Acceptance Criteria

- [ ] `go test ./internal/provider/ ./cmd/sand/ -race` passes with new tests
      asserting:
      - `BuildFleet` with an enabled proxmox profile produces a `Binding` whose
        `Scope` is `{Provider: "proxmox", RemoteTarget: "<host>:<node>/<pool>"}`.
      - A proxmox profile whose `token_file` is missing becomes an **error
        binding** (`Err` set, `Prov` nil) rather than aborting the whole fleet.
      - `providerForProfile` in `cmd/sand/resolve.go` returns a Proxmox provider
        for a proxmox profile, and `scopeForProfile` returns the identical scope
        to the fleet path.
- [ ] `go build ./... && go vet ./...` is clean.
- [ ] Constructing the fleet performs **no network I/O** — assert by building a
      fleet against an unreachable host and confirming it returns promptly with a
      usable binding (failures belong to `Preflight`).

## Technical Requirements

- Extend `buildBinding` (`internal/provider/fleet.go:76`) with a
  `profiles.TypeProxmox` case.
- Extend `targetConfigFor` (`internal/provider/fleet.go:106`) **and** its
  knowing duplicate in `cmd/sand/resolve.go:117`. Both carry "keep these in
  agreement" comments; update both and preserve the comments.
- Extend `providerForProfile` and `scopeForProfile` in `cmd/sand/resolve.go`.
- Add `newProxmox` to the package-level constructor vars
  (`internal/provider/fleet.go:34`) so tests can stub construction failure,
  matching the existing `newDefault`/`newRemoteLima` pattern.
- Token loading (`profiles.LoadToken`) happens at **construction** time, so a
  bad token file surfaces as a clear error binding rather than a confusing
  failure on first use. This is the one thing construction may do that touches
  the filesystem — it still performs no network I/O.

## Input Dependencies

Task 2 (profile type, `LoadToken`), task 5 (`NewProxmox`, `TargetConfig` fields,
`Scope()`).

## Output Artifacts

Updated `internal/provider/fleet.go`, `internal/provider/select.go`,
`cmd/sand/resolve.go` and tests — consumed by tasks 12 and 13.

## Implementation Notes

<details>

In `buildBinding`, add before the `default` case, mirroring the existing
`TypeRemoteSSH` shape (validate the obviously-broken case first so it surfaces
as a clear per-profile error rather than a cryptic failure later):

```go
case profiles.TypeProxmox:
    if p.Host == "" {
        return Binding{Profile: p, Err: fmt.Errorf("profile %q has no host", p.Name)}
    }
    cfg, err := targetConfigFor(p)
    if err != nil {
        return Binding{Profile: p, Err: err}
    }
    prov, err := newProxmox(cfg)
    if err != nil {
        return Binding{Profile: p, Err: err}
    }
    return Binding{Profile: p, Prov: prov, Scope: cfg.Scope()}
```

Note this requires `targetConfigFor` to gain an `error` return (it currently has
none) because token loading can fail. Update both copies and both call sites. If
that ripples further than expected, the alternative is to keep
`targetConfigFor` total by carrying `TokenFile` (the path) into `TargetConfig`
and loading inside `NewProxmox` — that keeps `TargetConfig` secret-free, which is
its documented contract, and is the **preferred** option if the error-return
change proves invasive. Pick one, and leave a comment saying why.

Whichever you choose, `TargetConfig` must **not** gain a field holding the token
value — its doc comment states it is deliberately secret-free precisely because
`Scope()` folds its fields into persisted registry identity. A path is fine; a
secret is not.

In `cmd/sand/resolve.go`, extend the parallel switches. The file's existing
comment explains it duplicates the fleet mapping deliberately, to avoid
constructing every remote when only one profile is needed — preserve that
property: do not "fix" the duplication by importing one into the other.

Registry scope: `registry.Scope{Provider: "proxmox", RemoteTarget: ...}`. The
registry is already scoped by provider so entries from different backends cannot
collide; no registry schema change is needed.

Deliberately **out of scope**: a TUI form for creating Proxmox profiles. Profiles
can be added by editing `profiles.yaml`, which is what the documentation (task
13) will describe. Adding a wizard was not requested.

</details>
