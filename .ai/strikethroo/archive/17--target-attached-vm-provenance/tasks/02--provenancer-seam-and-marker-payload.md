---
id: 2
group: "provenance-seam"
dependencies: []
status: "completed"
created: 2026-07-17
model: "sonnet"
effort: "medium"
complexity_score: 5
complexity_notes: "Architecture-defining interface that must fit future Proxmox/cloud providers without redesign."
skills:
  - go
---
# Define the Provenancer seam and marker payload type

## Objective
Introduce the provider-agnostic `Provenancer` interface and the provenance
payload type that mirrors what `registry.Entry` records today (base name,
`vm.CreateConfig`, sandbar version, created-at) plus a marker schema version.
This is the contract every later task depends on. The interface must be shaped
so a future Proxmox/cloud provider can implement it as VM tags/labels without
redesign, but only the type + interface are defined here (no implementation).

## Skills Required
- `go` — define an interface and a serializable struct in the provider layer.

## Acceptance Criteria
- [ ] A `Provenance` (marker) struct exists with, at minimum: `SchemaVersion int`,
  `Base string`, `Config vm.CreateConfig`, `SandbarVersion string`,
  `CreatedAt` (string/RFC3339), and JSON tags. It must carry the base name
  because recreate-gating depends on it.
- [ ] A `Provenancer` interface exists exposing: a batched read returning
  `map[string]Provenance` for all listed instances, a single-instance read,
  a mark/write, and an unmark/clear. Reads distinguish "not managed" (no marker)
  from an I/O error.
- [ ] An `ErrUnsupported` (or equivalent sentinel) is defined for providers that
  cannot support provenance.
- [ ] The package compiles: `go build ./internal/provider/... ./internal/vm/...`
  passes. Verification command: `go vet ./internal/provider/...` exits 0.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Place the type + interface in `internal/provider` (or a small subpackage) so
  both `local.go` and `remote.go` can implement/inherit it, avoiding an import
  cycle with `internal/registry`.
- Reuse `vm.CreateConfig` (already embedded in `registry.Entry`).
- Do not import `internal/registry` if it would create a cycle; the payload is a
  standalone mirror of the same fields.

## Input Dependencies
None. Leaf task; sits at the root of the dependency graph.

## Output Artifacts
- `Provenance` struct + `Provenancer` interface + `ErrUnsupported` sentinel,
  consumed by tasks 3, 4, 5, 6.

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

The registry `Entry` holds `Base string`, `Config vm.CreateConfig`,
`Provider string`, `RemoteTarget string`. The marker payload should mirror the
provenance-relevant subset — `Base` and `Config` are load-bearing; add
`SandbarVersion` (from `internal/version`) and `CreatedAt` for observability, and
`SchemaVersion` so the marker format can evolve.

Suggested shape (adjust names to house style):

```go
type Provenance struct {
    SchemaVersion  int             `json:"schema"`
    Base           string          `json:"base"`
    Config         vm.CreateConfig `json:"config"`
    SandbarVersion string          `json:"sandbar_version"`
    CreatedAt      string          `json:"created_at"`
}

type Provenancer interface {
    // Provenance returns a marker for every listed instance that carries one.
    // Instances with no marker are simply absent from the map.
    Provenance(ctx context.Context) (map[string]Provenance, error)
    // ProvenanceOf returns the marker for one instance, or ok=false if none.
    ProvenanceOf(ctx context.Context, name string) (Provenance, bool, error)
    MarkManaged(ctx context.Context, name string, p Provenance) error
    Unmark(ctx context.Context, name string) error
}
```

Define `var ErrUnsupported = errors.New("provider does not support provenance")`.

Only define types here. Do NOT implement them for Lima (task 3) or wire consumers
(tasks 4/5). Keep the interface small — the batched `Provenance` is the primary
entry point for the board; `ProvenanceOf` serves CLI paths that target one VM.

Import-cycle caution: if `internal/provider` already imports `internal/registry`,
putting `Provenance` here is fine; if the reverse import would be needed later,
keep `Provenance` free of any registry dependency (it is a plain data mirror).
</details>
