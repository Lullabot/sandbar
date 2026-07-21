---
id: 2
group: "tui-form"
dependencies: [1]
status: "completed"
created: 2026-07-20
model: "sonnet"
effort: "medium"
complexity_score: 5
complexity_notes: "A new lightweight picker view plus rerouting the single create entry point; the tricky integration (the proxmox form it opens into) is done in task 1."
skills:
  - bubbletea-tui
---
# tui: pre-form type picker for creating a profile

## Objective

Add a pre-form **type picker** so that pressing `n` on the profile-management
screen (`p`) lets the user choose which kind of profile to create — "Remote SSH"
or "Proxmox" — before the field form opens. Local is excluded (it is permanent
and pre-seeded). This is the create entry point that reaches the Proxmox form
built in task 1.

## Skills Required

`bubbletea-tui` (a small selection view, key routing, view state).

## Acceptance Criteria

- [ ] `go build ./... && go vet ./... && gofmt -l internal/ui` are clean.
- [ ] `go test ./internal/ui/ -race` passes, including a new behavioural test:
      pressing `n` shows the picker with "Remote SSH" and "Proxmox" (not
      "Local"); selecting "Proxmox" opens the field form with
      `profileFormType == profiles.TypeProxmox` and the Proxmox field set;
      selecting "Remote SSH" opens the existing remote-ssh form unchanged.
- [ ] `esc` from the picker returns to the profile list; `esc` from the field
      form returns to the picker or the list (match the existing form's back
      behaviour — pick one and test it).
- [ ] The **edit** path is unchanged: editing an existing profile opens its
      field form directly with the profile's own type, never the picker.

## Technical Requirements

- Change the single create entry point `openProfileCreateForm`
  (`internal/ui/profilesview.go` ~:421), which today hardcodes
  `profileFormType = profiles.TypeRemoteSSH`, to instead open the picker.
- Add a new view (a `viewProfileTypePicker`-style state) and its update/render,
  plus routing from the `n` key (`updateProfiles`, ~:632) and back-navigation.
- The picker lists the **creatable** types only. Source them from a small
  ordered slice (`[]profiles.Type{TypeRemoteSSH, TypeProxmox}`) so a future type
  is one line to add.
- Selecting a type calls into the form-open path with that type
  (`newProfileInputs(t)` + `profileFormType = t` + focus the first field).

## Input Dependencies

Task 1 (the Proxmox field form the picker opens into).

## Output Artifacts

Updated `internal/ui/profilesview.go` (+ `model.go` view/cursor state) and a new
behavioural test — consumed by tasks 3 and 4.

## Implementation Notes

<details>

Add a view constant (near the other `view*` constants) and model state for the
picker cursor. The `p` screen's key dispatch routes `esc`/`↑↓`/`enter` while the
picker view is active.

```go
// creatableProfileTypes are the types `n` can create, in menu order. Local is
// omitted: it is permanent and pre-seeded (there is exactly one, created on
// first run), so it is editable but never creatable.
var creatableProfileTypes = []profiles.Type{profiles.TypeRemoteSSH, profiles.TypeProxmox}
```

`openProfileCreateForm` becomes "open the picker" (reset cursor, set
`m.view = viewProfileTypePicker`). A new `openProfileFormForType(t profiles.Type)`
does what the old create did but for an arbitrary creatable type: set
`profileFormID = ""`, `profileFormType = t`, `profileInputs = newProfileInputs(t)`,
focus field 0, `m.view = viewProfileForm`.

Render the picker with the same title/footer styling the form uses: a title
("New Connection Profile"), one row per creatable type with the cursor marker and
a human label ("Remote SSH", "Proxmox"), and a footer help
(`↑↓ move • enter select • esc back`). Reuse `profileRowText`'s human labels if
convenient, or a small `profileTypeLabel(t)` helper.

Human labels for the menu:
- `TypeRemoteSSH` → "Remote SSH"
- `TypeProxmox` → "Proxmox"

Keep the edit path (`openProfileEditForm`, ~:432) exactly as it is — it already
sets `profileFormType = p.Type` and never shows the picker.

**Test** with the teatest/key-driven pattern (profilesview_management_test.go):
press `n`, assert the picker is shown and lists exactly the two creatable types,
move to Proxmox, press enter, and assert `m.profileFormType == TypeProxmox` and
the proxmox field set is present. A second assertion: selecting Remote SSH yields
the unchanged remote-ssh form. One focused test is enough — do not enumerate a
case per key.

</details>
