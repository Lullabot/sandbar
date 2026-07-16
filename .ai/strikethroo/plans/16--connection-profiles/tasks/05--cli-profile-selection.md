---
id: 5
group: "fleet"
dependencies: [1, 4]
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "medium"
complexity_score: 5
complexity_notes: "Two CLI entrypoints gain a --profile flag and cross-profile disambiguation; straightforward once the fleet builder exists."
skills:
  - go
  - cli
---
# Add `--profile` selection to `sand create` and cross-profile `sand shell`

## Objective
Adapt the non-TUI entrypoints to the fleet: `sand create --profile <name>`
(defaulting to last-used, then Local) acts on that one profile; `sand shell NAME`
resolves which profile owns `NAME` and disambiguates when a name exists under more
than one profile. Each CLI command preflights only the single profile it acts on.

## Skills Required
- `go` — flag parsing, command wiring.
- `cli` — user-facing flag/error UX.

## Acceptance Criteria
- [ ] `sand create` accepts `--profile <name>`; default is the last-used profile (from the profiles store), falling back to Local when there is no last-used. An unknown profile name is a clear error naming the bad value.
- [ ] `sand create` provisions on the selected profile's provider/scope and, on success, records that profile as last-used (by ID) in the profiles store.
- [ ] `sand create` preflights **only** the selected profile (synchronous is fine for a one-shot CLI command) and errors out if that preflight fails.
- [ ] `sand shell NAME` resolves the owning profile from the registry scope. If `NAME` exists under exactly one profile, it connects there. If under **more than one**, it errors and **lists the candidate profiles**; an explicit `--profile <name>` disambiguates.
- [ ] `sand shell` preflights only the resolved profile.
- [ ] No interactive CLI picker is added (YAGNI — see Decision Log).
- [ ] `go build ./...` and `go test ./cmd/... -race` pass; add a test for unknown-profile error and for the ambiguous-name `shell` case (using fakes/fixtures, no real backend).

## Technical Requirements
- Files: `cmd/sand/create.go` (runCreate, Resolve ~156, Preflight ~160-162), `cmd/sand/shell.go` (runShell, Resolve ~88, Preflight ~92-94), and the flag wiring in `cmd/sand/main.go`.
- Uses `provider.BuildFleet` / bindings (task 4) and the profiles store (task 1).

## Input Dependencies
- Task 1: profiles store (last-used, name→profile lookup).
- Task 4: fleet builder / bindings (to pick one binding by profile).

## Output Artifacts
- `sand create --profile` and cross-profile `sand shell` behavior.
- Consumed by: task 11 (cli-reference.md docs).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- **Selecting one binding.** Build the fleet (or a single binding) for the chosen
  profile only — you do not need to construct every remote just to run one CLI
  command. A helper `bindingForProfile(store, name)` that resolves the profile then
  constructs just that provider/scope is ideal, so an unrelated broken remote does
  not fail `sand create --profile local`.
- **Default resolution order for create:** explicit `--profile` > store's last-used
  > Local. Validate the name exists and is enabled; a disabled target profile
  should error clearly ("profile X is disabled").
- **`sand shell` ownership lookup:** query the registry for which scope(s) contain
  a managed VM named `NAME` (the registry is scope-aware — `IsManagedInScope` and
  friends). Map scope back to profile via the fleet/store. Zero owners → "no such
  VM"; one → connect; many → list candidates and require `--profile`.
- **Preflight only the one profile.** Both commands currently call
  `p.Preflight()` right after `Resolve()`. Keep synchronous preflight but on the
  single selected binding's provider only. (The async fleet preflight is the TUI's
  concern in task 7 — do not import UI async machinery here.)
- **last-used write** happens only on a successful create.
</details>
