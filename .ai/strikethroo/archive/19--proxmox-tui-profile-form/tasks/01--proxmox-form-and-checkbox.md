---
id: 1
group: "tui-form"
dependencies: []
status: "completed"
created: 2026-07-20
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "Introduces a boolean checkbox input kind into a form that is homogeneous text inputs today, touching focus traversal, key handling, and rendering — plus the save/build/edit/row/validation wiring and a latent connectionFieldsEqual bug fix. Multi-site, in one file."
skills:
  - bubbletea-tui
  - go
---
# tui: Proxmox field form, insecure checkbox, and save/build/edit/row wiring

## Objective

Make the connection-profile form in `internal/ui/profilesview.go` fully support
the `proxmox` type: a Proxmox field set, a real boolean **insecure checkbox**
(a new input kind), in-form validation, and the save / provider-build /
edit-prefill / list-row wiring — so that once `profileFormType` is
`TypeProxmox`, the form renders, focuses, edits, validates, and persists a
proxmox profile. (Reaching the form via the create *picker* is task 2; this task
is verifiable through the EDIT path, which already opens the form for an existing
proxmox profile.)

## Skills Required

`bubbletea-tui` (Bubble Tea v2 model/update/view, Bubbles text inputs, focus and
key handling, rendering) and `go`.

## Acceptance Criteria

- [ ] `go build ./... && go vet ./... && gofmt -l internal/ui` are clean.
- [ ] `var _` compiles and `go test ./internal/ui/ -race` passes (existing tests
      stay green; the local and remote-ssh flows are unchanged).
- [ ] A new behavioural test proves: opening the edit form for a seeded
      `type: proxmox` profile prefills host, node, pool, storage, bridge,
      token_file, insecure, and ca_file; toggling the insecure checkbox (space)
      flips it; saving writes those fields back via `Store.Update`, with
      `token_file` a **path** and no token value anywhere.
- [ ] A test proves a proxmox profile missing host, node, pool, **or** token_file
      shows an in-form error (`profileFormErr`) and does **not** leave the form /
      call the store to success.
- [ ] A test proves `connectionFieldsEqual` returns **false** when two otherwise-
      identical proxmox profiles differ in node, pool, storage, bridge,
      token_file, insecure, or ca_file (today it ignores all of them).
- [ ] `buildProfileProvider` constructs a `NewProxmox` provider for a proxmox
      profile — asserted by a test that a rebuilt proxmox member is non-error
      (construction does no network I/O, mirroring the fleet path).

## Technical Requirements

- All production changes in `internal/ui/profilesview.go` (+ minimal
  `internal/ui/model.go` state if a new field needs storing).
- **Proxmox field set**: name, host, node, pool, storage, bridge, token_file,
  insecure (checkbox), ca_file. Extend `newProfileInputs(t)` (profilesview.go
  ~:403) and the label list.
- **Checkbox input kind**: `insecure` is a focusable, non-text element rendering
  `[x] Insecure` / `[ ] Insecure`, toggled by space (and enter) when focused,
  and included in focus traversal (`profileFormFocusNext/Prev` ~:454) and the
  view (`profileFormView` ~:570). The text-input path must stay unchanged.
- **Save**: `submitProfileForm` (~:479) gains a `TypeProxmox` branch mapping
  inputs → `Profile.{Host,User?,Node,Pool,Storage,Bridge,TokenFile,Insecure,CAFile}`.
  token_file is a path, never a secret. In-form required checks:
  host, node, pool, token_file (mirror `profiles.validate`, store.go ~:340).
- **Edit prefill**: `openProfileEditForm` (~:432) prefills the proxmox fields.
- **Provider build**: `buildProfileProvider` (~:171) gains a `TypeProxmox`
  branch building the `TargetConfig{Provider: provider.ProxmoxProviderID, ...}`
  and calling `provider.NewProxmox` — mirror `cmd/sand/resolve.go`'s
  `providerForProfile`/`targetConfigFor` exactly.
- **List row**: `profileRowText` (~:684) gains a "Proxmox" kind and a
  `host:node/pool` target (matching `profiles.proxmoxTarget`).
- **connectionFieldsEqual** (~:348): compare the Proxmox fields too.
- **rebuildMember** (~:215): the "connecting to …" log should treat proxmox as
  remote (it is).

## Input Dependencies

None (the backend — `profiles.TypeProxmox` + fields, `provider.NewProxmox`,
`ProxmoxProviderID`, `profiles.validate`'s proxmox rules — already exists on this
branch).

## Output Artifacts

Updated `internal/ui/profilesview.go` (+ `model.go` if needed) and new
behavioural tests in `internal/ui/profilesview_management_test.go` /
`profilesview_test.go` — consumed by tasks 2, 3, 4.

## Implementation Notes

<details>

**Field indices.** The current block (profilesview.go ~:52) is
`pfName, pfHost, pfUser, pfPort, pfIdentityPath, pfLimaHome`. Do **not** overload
these for proxmox — the field sets differ. Prefer a per-type layout: either a
second index block for proxmox (e.g. `ppName, ppHost, ppNode, ppPool, ppStorage,
ppBridge, ppTokenFile, ppInsecure, ppCAFile`) plus a per-type label slice, or a
small `formField{label string, kind fieldKind}` descriptor list selected by
`profileFormType`. The descriptor approach scales better to the checkbox and to
future types; pick whichever keeps the text path unchanged.

**The checkbox — smallest viable approach.** The form is `[]textinput.Model`
today and the whole update/focus/view path assumes it. Introduce the minimum
that lets ONE focusable element be a boolean. Two workable shapes:

1. Keep `[]textinput.Model` for the text fields and store `insecure bool` +
   `insecureFocused` separately, special-casing the single focus index that maps
   to the checkbox in focus-next/prev, the key loop, and the view.
2. Model the form as a slice of a `field` interface/struct with a `kind`
   (text|bool), holding either a `textinput.Model` or a bool.

Prefer (2) if it stays small; it removes the "which index is the checkbox"
special-casing. Whichever you pick:
- Focus: the checkbox participates in traversal like any field.
- Update: when the checkbox is focused, **space** and **enter** toggle it;
  arrow/character keys do nothing to it (they still move focus via the existing
  up/down handling). Text keys must never reach a text input while the checkbox
  is focused.
- View: render `[x] Insecure` when true, `[ ] Insecure` when false, using the
  same `focusedLabelStyle`/`labelStyle` selection the text rows use so the
  focused row highlights consistently (styles.go ~:32).

**Save mapping** (submitProfileForm, a value receiver returning `(tea.Model,
tea.Cmd)`):

```go
p := profiles.Profile{ID: m.profileFormID, Name: name, Type: m.profileFormType, Enabled: true}
switch m.profileFormType {
case profiles.TypeRemoteSSH:
    // ... existing mapping, unchanged ...
case profiles.TypeProxmox:
    p.Host = strings.TrimSpace(inputs[ppHost])
    p.Node = strings.TrimSpace(inputs[ppNode])
    p.Pool = strings.TrimSpace(inputs[ppPool])
    p.Storage = strings.TrimSpace(inputs[ppStorage])
    p.Bridge = strings.TrimSpace(inputs[ppBridge])
    p.TokenFile = strings.TrimSpace(inputs[ppTokenFile]) // a PATH, never the token
    p.CAFile = strings.TrimSpace(inputs[ppCAFile])
    p.Insecure = m.insecureChecked()
    for _, req := range []struct{ v, name string }{
        {p.Host, "host"}, {p.Node, "node"}, {p.Pool, "pool"}, {p.TokenFile, "token file"},
    } {
        if req.v == "" {
            m.profileFormErr = fmt.Errorf("%s is required", req.name)
            return m, nil
        }
    }
}
```

Then the existing create/edit tail (`Store.Add` / `submitProfileEdit`) is shared
— no change needed there; the store validates the rest.

**buildProfileProvider** — mirror resolve.go's proxmox case:

```go
if p.Type == profiles.TypeProxmox {
    cfg := provider.TargetConfig{
        Provider: provider.ProxmoxProviderID,
        Host:     p.Host, User: p.User, Node: p.Node, Pool: p.Pool,
        Storage:  p.Storage, Bridge: p.Bridge,
        TokenFile: p.TokenFile, Insecure: p.Insecure, CAFile: p.CAFile,
    }
    prov, err := provider.NewProxmox(cfg)
    if err != nil { return nil, registry.Scope{}, err }
    return prov, cfg.Scope(), nil
}
```

Check the exact `TargetConfig` field names in `internal/provider/select.go` and
the resolve.go mapping; keep all three in agreement (add the same
"keep in agreement" note the file already carries for remote-ssh).

**connectionFieldsEqual** (~:348) currently compares only
Host/User/Port/IdentityPath/LimaHome. Add Node/Pool/Storage/Bridge/TokenFile/
Insecure/CAFile. Without this, editing a proxmox profile's pool is treated as a
pure rename and the member is never rebuilt against the new pool — a real bug.

**profileRowText** (~:684): for `TypeProxmox`, kind `"Proxmox"`, target
`fmt.Sprintf("%s:%s/%s", p.Host, p.Node, p.Pool)` (match `proxmoxTarget`).

**Tests.** Add a `seedProxmoxProfile` helper mirroring `seedRemoteProfile`
(profilesview_test.go ~:279). Drive the edit form with keystrokes (the
management tests at profilesview_management_test.go ~:90 are the pattern),
assert focus order and the saved Profile. Cover the required-field error and the
`connectionFieldsEqual` cases as unit-level tests on the helpers where possible
(they are cheaper than full teatest walks). Follow the repo's "write a few
tests, mostly integration" convention: one create/edit walk plus the two
targeted logic tests, not a test per field.

</details>
