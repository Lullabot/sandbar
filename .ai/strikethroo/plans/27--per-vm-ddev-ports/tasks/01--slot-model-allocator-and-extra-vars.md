---
id: 1
group: "slot-core"
dependencies: []
status: "pending"
created: 2026-11-05
models:
  anthropic: "claude-opus-5-5"
  openai: "gpt-6-sol"
effort: "high"
skills:
  - go
  - go-testing
complexity_score: 6
complexity_notes: "Persisted-record compatibility (registry, provenance marker, reset.json) makes this data-format-sensitive; the allocation itself is simple."
---
# ddev port slot model, allocator and finalize extra vars

## Objective
Add the per-VM ddev port slot to `vm.CreateConfig` with an explicit "absent" state, the pure slot/port math, a shared allocator in `internal/manage`, and the seven ddev port extra vars that `BuildExtraVars` emits for finalize/full on Lima with a recorded slot.

## Skills Required
Go (struct/JSON encoding, stdlib `hash/fnv`), Go testing (table tests, fixtures for previously written records).

## Acceptance Criteria
- [ ] `vm.CreateConfig` has `DDEVPortSlot *int` tagged `json:",omitempty"`; nil means "no slot recorded"; slot 0 is a valid, distinguishable value.
- [ ] `CreateConfig.Validate` rejects a non-nil slot outside `[0, DDEVPortSlots)`.
- [ ] Pure helpers in `internal/vm` (`ddevports.go`): constants `DDEVPortBase = 20000`, `DDEVPortStride = 10`, `DDEVPortSlots = 1000`; `PreferredDDEVSlot(name string) int` (FNV-1a 32-bit of the name, mod `DDEVPortSlots`); `AllocateDDEVSlot(name string, held map[int]bool) (int, error)` (linear probe from preferred, wrapping, error when all held); `DDEVPorts` struct + `DDEVPortsForSlot(slot int) DDEVPorts` with RouterHTTP +0, RouterHTTPS +1, MailpitHTTP +2, MailpitHTTPS +3, XHGuiHTTP +4, XHGuiHTTPS +5, TraefikMonitor +6.
- [ ] `manage.AssignDDEVSlot(cfg *vm.CreateConfig, kind profiles.Type, markers map[string]provider.Provenance, managed []registry.ManagedEntry) error`: no-op unless `kind` is `profiles.TypeLocal` or `profiles.TypeRemoteSSH` AND `cfg.WithDDEV` AND `cfg.DDEVPortSlot == nil`; otherwise builds the held set from every marker's and every managed entry's `Config.DDEVPortSlot` whose name != `cfg.Name`, allocates, and sets `cfg.DDEVPortSlot`. Performs no I/O.
- [ ] `provision.BuildExtraVars` emits, only when `phase != "base"`, `backend != profiles.TypeProxmox`, `cfg.WithDDEV` and `cfg.DDEVPortSlot != nil`, these integer vars: `ddev_router_http_port`, `ddev_router_https_port`, `ddev_mailpit_http_port`, `ddev_mailpit_https_port`, `ddev_xhgui_http_port`, `ddev_xhgui_https_port`, `ddev_traefik_monitor_port`.
- [ ] Tests cover: deterministic preferred slot for a fixed name (pin the expected value); probing past a held slot; wrap-around from slot 999 to 0; exhaustion error; slot 0 vs absent round-trips through JSON; `AssignDDEVSlot` excludes the VM's own name, keeps an existing slot, and is a no-op for Proxmox and `WithDDEV=false`; `BuildExtraVars` emits the vars only for finalize/full + Lima + slot (base phase and Proxmox omit them).
- [ ] A previously written registry entry, provenance marker and `reset.json` (fixture JSON without the field) decode with `DDEVPortSlot == nil` and otherwise unchanged.
- [ ] `go test ./internal/vm/... ./internal/manage/... ./internal/provision/... ./internal/registry/... ./internal/provider/...` passes; `gofmt -l .` is empty; `go vet ./...` passes.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- `internal/vm/vm.go` (CreateConfig), new `internal/vm/ddevports.go`, `internal/manage/` (new file e.g. `ddevslot.go`), `internal/provision/vars.go`.
- Standard library only (`hash/fnv`). No new module dependency.
- `profiles.Type` constants: `TypeLocal`, `TypeRemoteSSH`, `TypeProxmox` (`internal/profiles`).

## Input Dependencies
None.

## Output Artifacts
- `vm.CreateConfig.DDEVPortSlot`, `vm.DDEVPortsForSlot`, `vm.AllocateDDEVSlot`, `manage.AssignDDEVSlot` — consumed by task 3 (entrypoint wiring).
- Ansible extra vars named above — consumed by task 2's role and templates (names are fixed; do not rename).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- `CreateConfig` has no JSON tags today; fields serialize under their Go names. Add the tag only to the new field: `DDEVPortSlot *int \`json:",omitempty"\`` so the key is `DDEVPortSlot` and absent when nil. Document on the field: allocated once for Lima + WithDDEV, carried by every reset path explicitly, nil means unrecorded (pre-feature records keep ddev defaults until a reset allocates one).
- Pointer aliasing: never mutate through the pointer (`*cfg.DDEVPortSlot = x`); always assign a fresh pointer. Grep for `==` comparisons of whole `vm.CreateConfig` values (e.g. `cfg == rec`) — a pointer field makes `==` compare addresses; switch any such comparison to `reflect.DeepEqual` or field-wise as appropriate, and say so in your report.
- Do NOT bump `provider.MarkerSchemaVersion`: the field is an additive key inside `config`, like earlier CreateConfig additions.
- `PreferredDDEVSlot`: `h := fnv.New32a(); h.Write([]byte(name)); return int(h.Sum32() % DDEVPortSlots)`.
- `AllocateDDEVSlot`: for i in 0..DDEVPortSlots-1: s := (pref+i) % DDEVPortSlots; if !held[s] return s. Return a clear error (e.g. "no free ddev port slot: all 1000 slots in use") after the loop.
- `AssignDDEVSlot` must be pure (no context, no provider calls) so the TUI can call it on the submit path with its cached `mem.provenance` and the CLI with the result of one `Provenancer.Provenance(ctx)` call. The held set is the union of marker and registry slots; a union is safe (over-holding only costs a probe step). Template records are not in either input and do not hold slots.
- `BuildExtraVars`: add the block alongside the existing `phase != "base"` block; compute ports via `vm.DDEVPortsForSlot(*cfg.DDEVPortSlot)`. Add a short comment explaining why the base phase never gets them (per-VM values must not enter the base image or its version stamp) and why the gate is here rather than in Ansible (`toolset_ddev` is base-only, so finalize cannot gate on it).
- Fixtures: build a JSON literal for a registry entry/marker/reset manifest in the shape the previous release wrote (copy an existing test fixture or marshal a config, delete the key) and assert decode.
- Test philosophy — write a few tests, mostly integration. Meaningful tests verify custom business logic, critical paths, and edge cases specific to this application; test our code, not the framework or library. Write tests for custom logic, critical workflows, edge cases and integration points; do not test third-party libraries, framework features, trivial getters/setters or static configuration. Combine related scenarios into table tests rather than one test per function.
- No `t.Parallel()` (repo convention). Isolate host state per AGENTS.md if a test touches the registry on disk (`XDG_DATA_HOME` temp dir).
- Verification commands: `go test ./internal/...`, `go vet ./...`, `gofmt -l .` (must print nothing).
</details>
