---
id: 8
group: "provider"
dependencies: [3, 5]
status: "pending"
created: 2026-07-20
model: "sonnet"
effort: "medium"
skills:
  - go
  - proxmox-api
---
# provider: implement Provenancer using PVE tags and the description field

## Objective

Satisfy the optional `provider.Provenancer` interface for Proxmox by recording
sandbar's managed-VM provenance in PVE's own VM metadata — tags plus the
description field — rather than a sidecar file.

## Skills Required

`go` (interface satisfaction, JSON round-tripping) and `proxmox-api` (config
tags and description semantics).

## Acceptance Criteria

- [ ] `var _ provider.Provenancer = (*proxmoxProvider)(nil)` compiles.
- [ ] `go test ./internal/provider/ -race` passes with mock-server tests
      asserting:
      - `MarkManaged` writes a `sandbar` tag and a description block, and a
        subsequent `Provenance` round-trips every field unchanged.
      - `Provenance` is a **single** batched request for the whole fleet — assert
        the mock receives one `/cluster/resources` call and not one call per VM.
      - `Unmark` removes the tag and the description block while **preserving**
        any pre-existing description text not written by sandbar.
      - A VM with an unparseable description block is reported as not-managed
        rather than causing an error that aborts the whole batch.

## Technical Requirements

- Implement `Provenance`, `ProvenanceOf`, `MarkManaged`, and `Unmark` on
  `*proxmoxProvider`.
- `Provenance(ctx)` must be **one host round trip** for the whole fleet, matching
  the interface's documented contract.
- A VM whose provenance is missing or corrupt must be absent from the result map,
  never an error that fails the batch — mirroring the existing
  `ReadInstanceMarkers` contract.
- Never clobber operator-authored description text.

## Input Dependencies

Task 3 (`GetConfig`/`SetConfigSync`, `ListVMs`), task 5 (`proxmoxProvider`).

## Output Artifacts

`internal/provider/proxmoxprovenance.go` and tests.

## Implementation Notes

<details>

The `Provenancer` doc comment already anticipates this exact implementation:
"a future Proxmox/cloud backend can satisfy the same interface with VM
tags/labels, with no redesign."

**Storage shape.** Use two complementary places:

- A **tag** `sandbar` on the VM (`tags` in the config; PVE stores them
  semicolon-separated). This is what makes managed VMs visible and filterable in
  the Proxmox web UI, which is a real operator benefit.
- A **fenced block in the description** carrying the serialized
  `provider.Provenance` as JSON:

```
<!-- sandbar:begin -->
{"...":"..."}
<!-- sandbar:end -->
```

Fencing is what lets `Unmark` remove sandbar's data while leaving any
operator-authored notes intact. Parse with a non-greedy match between the
markers; treat "markers absent" and "JSON does not parse" identically as
not-managed.

**Batching.** `GET /cluster/resources?type=vm` includes `tags` but **not** the
description. Two viable shapes:

1. Use `/cluster/resources` to find tagged VMs, then fetch descriptions only for
   those — one call plus N-managed calls.
2. Accept that full provenance needs the config and fetch configs concurrently
   with a bounded worker pool.

Prefer (1): the tag filter usually shrinks N sharply, and unmanaged VMs cost
nothing. Document the choice in a comment. Note the interface's contract says
"one host round trip for the whole fleet" — (1) honours the *intent* (constant
work per managed VM, no per-unmanaged-VM cost); say so explicitly rather than
leaving a reader to wonder whether the contract was overlooked.

Be aware of PVE's connection behaviour when fanning out: **any response ≥400
closes the connection**, and there is no admission control server-side, so bound
concurrency (4–8) rather than firing N requests at once.

**Writing.** Read the current config with `GetConfig` (remember `current=1`),
splice the description block, merge the tag into the existing semicolon-separated
`tags` value without dropping operator tags, and write with `SetConfigSync`
(PUT — synchronous, returns nothing). Description text must be sent as a normal
form value; do not hand-escape it.

**`ProvenanceOf`** returns `(Provenance, bool, error)` — the bool is
"is managed", so a clean not-managed answer is `(zero, false, nil)`, never an
error.

</details>
