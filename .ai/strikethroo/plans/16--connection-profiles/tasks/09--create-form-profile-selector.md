---
id: 9
group: "profile-ui"
dependencies: [1, 3, 7]
status: "completed"
created: 2026-07-15
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Adds a create-form field and wires create-time provisioning to the selected profile's provider/scope AND host-access (correctness-sensitive: provisions on the right host)."
skills:
  - go
  - bubbletea
---
# Add a create-form profile selector and target the selected profile's provider

## Objective
Give the create form a first-class **profile selector** defaulting to the
**last-used** profile (falling back to Local), and make VM creation provision on
that profile's provider, tag it under that profile's scope, sample host-scaled
defaults from that profile's host, and pass that profile's host-access into
provisioning. This is Component 4's create-time half.

## Skills Required
- `go` — wiring create to a chosen binding + host-access.
- `bubbletea` — a new form field with selection UX.

## Acceptance Criteria
- [ ] The create form has a **profile selector** field listing enabled profiles, defaulting to the **last-used** profile (by id) and falling back to Local when none.
- [ ] Creating a VM provisions on the **selected** profile's provider and records the entry under that profile's **scope** in the registry, so it appears only under that profile's tiles.
- [ ] Provisioning passes the selected profile's **host-access** into the provision seam (task 3's per-operation argument) — the base-image touches happen on the correct host.
- [ ] Host-scaled defaults (cpu/memory/user — already host-aware from plan 15) are sampled from the **selected** profile's host, not always the local one.
- [ ] On successful create, the selected profile is persisted as **last-used** (by id) via the profiles store.
- [ ] `go test ./internal/ui/... -race` passes with a test asserting the selector defaults to last-used and that a create routes to the chosen member's provider/scope (via `providerfake`). Update golden files for the new form field. **No real backend.**

## Technical Requirements
- Files: `internal/ui/` create-form model + render + key handling; `model.go` create action; the profiles store (task 1); the provision seam (task 3); the fleet model (task 7).

## Input Dependencies
- Task 1: profiles store (enabled list, last-used get/set).
- Task 3: per-operation provision host-access argument.
- Task 7: fleet model (the create targets a specific member).

## Output Artifacts
- Create form with a profile selector; create routed to the chosen profile.
- Consumed by: task 11 (tui.md/cli docs), task 12 (create-on-profile integration test).

## Implementation Notes
<details>
<summary>Detailed guidance</summary>

- **Selector field.** Add a field to the create-form model listing enabled
  profiles (from the store). Default the highlighted option to the last-used id,
  else the Local profile. Keep it within the form's existing field-navigation UX.
- **Routing the create.** The create action currently uses the single provider.
  Now it must resolve the selected profile → its `fleetMember` (task 7) → provider,
  scope, host-access. Provision via task 3's host-access argument; add the registry
  entry with the member's scope (`AddScoped`).
- **Host-scaled defaults.** Where the form seeds cpu/memory/user from host capacity,
  read them from the **selected member's** host sample (each member already carries
  its own host-capacity sample in task 7), not the local header values.
- **last-used.** On successful create, call the store's last-used setter with the
  selected profile's id.
- **Goldens.** The extra field changes rendered output — regenerate the create-form
  goldens.
</details>
