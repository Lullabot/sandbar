---
id: 8
group: "tui-secrets"
dependencies: [2, 7]
status: "completed"
created: 2026-07-09
model: "sonnet"
effort: "high"
complexity_score: 7
complexity_notes: "A new Bubble Tea view with its own parse/validate cycle, wired into a value-passed model, plus a store handle the model must carry copy-safely. Key validation must reject before anything persists."
skills:
  - go
  - bubbletea
---
# Secrets editor screen

## Objective

Add a `viewSecrets` screen, opened with `e` from the VM screen, that edits a VM's
`KEY=VALUE` secrets in a `bubbles/textarea` and persists them to the host store —
**whether the VM is running or stopped**. Reject malformed keys before anything is
written.

## Skills Required

- **go** — parsing, validation, error messages that name the offending input.
- **bubbletea** — adding a view to the `Update`/`View`/`forward` dispatch, and the
  `textarea` component.

## Acceptance Criteria

- [ ] `internal/ui` gains a `viewSecrets` view constant, wired into `Update`'s key
      routing switch, `View()`, and `forward()` (so the textarea receives its
      blink/tick messages).
- [ ] The model carries a `*secrets.Store`, loaded in `New()` with the same
      tolerant posture as the registry: a corrupt file surfaces as a `m.status`
      warning and yields a usable empty store, never a crash. The handle is a
      pointer, so the value-passed model stays copy-safe.
- [ ] `e` on the VM screen opens the editor **regardless of VM status** — no
      running-VM guard. It seeds the textarea with the VM's current pairs, one
      `KEY=VALUE` per line, keys sorted ascending.
- [ ] `ctrl+s` parses the buffer, validates, and on success persists via
      `Store.Set(vmName, pairs)`, returns to `viewDetail`, and sets a status line.
      `esc` discards and returns to `viewDetail` without writing.
- [ ] Parsing: blank lines and lines whose first non-space character is `#` are
      ignored. A line is split on the **first** `=` only, so a value may contain
      `=`. The key is trimmed of surrounding whitespace; the value is **not**
      trimmed (a trailing space may be significant).
- [ ] A line with no `=` at all, or whose key fails `secrets.ValidKey`, aborts the
      save with an error naming the offending line number and its content, and
      **nothing is persisted**.
- [ ] A duplicate key aborts the save with an error naming the key. (Last-wins
      would silently discard a secret the user typed.)
- [ ] Values render in cleartext. The screen carries a short warning saying so, and
      that they are stored unencrypted on the host.
- [ ] Verification: `go test ./internal/ui/... -v` passes, including:
      ```
      go test ./internal/ui/... -run 'SecretsEditor|SecretsParse' -v
      ```
      Expected `PASS`, with tests asserting: (a) `e` on a `Stopped` VM sets
      `m.view == viewSecrets`; (b) parsing `A=1\n\n# c\nB=x=y\n` yields exactly
      `{"A":"1","B":"x=y"}`; (c) parsing `2BAD=x` returns an error mentioning
      `2BAD` and line `1`, and `Store.Set` was never called; (d) parsing
      `A=1\nA=2` returns a duplicate-key error mentioning `A`; (e) `esc` from the
      editor returns to `viewDetail` and does not call `Store.Set`.
- [ ] Verification: `go build ./... && go vet ./...` succeed.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements

- New file `internal/ui/secrets.go` (view + update + parse), plus edits to
  `internal/ui/model.go` (view constant, store field, `New()`, `Update`, `View`,
  `forward`) and `internal/ui/detail.go` (the `e` handler).
- **Do not modify** `internal/ui/keys.go` — task 5 bound `Secrets` to `e` and
  added it to `viewDetail`'s help.
- `github.com/charmbracelet/bubbles/textarea` is available in the module cache at
  `bubbles v1.0.0`; it is a new *import*, not a new *dependency*.
- `textarea.Model` is copy-safe in the same way the other bubbles components the
  model already embeds by value are (`textinput.Model`, `viewport.Model`). Embed
  it by value, matching the existing style.
- `secrets.ValidKey` and `secrets.Store` come from task 2.

## Input Dependencies

- **Task 2**: `internal/secrets` — `Store`, `Load`, `Get`, `Set`, `ValidKey`.
- **Task 7** (and transitively 5, 6): the `Secrets` key binding, `viewDetail`'s
  help entry, and the restructured `updateDetail` — with confirm-overlay routing
  already at its head — that this task adds a case to. Task 7 is a dependency for
  file-contention reasons as much as logical ones: it and this task both edit
  `internal/ui/model.go`, and agents share one working tree.

## Output Artifacts

- `viewSecrets` screen and the `*secrets.Store` handle on the model — consumed by
  task 9, which reads `m.sec.Get(name)` when applying secrets on start.
- A `parseSecrets(text string) (map[string]string, error)` helper.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Read first:** `internal/ui/model.go` (the `view` const block, the `model` struct,
`New()`, `Update`'s view switch, `View()`, `forward()`), `internal/ui/transfer.go`
(the closest analogue — a self-contained sub-screen with its own update/view), and
task 2's `internal/secrets` API.

**Step 1 — the view constant.** Append `viewSecrets` to the `const` block. Append,
do not insert: the constants are `iota`-based and nothing persists them, but
appending keeps diffs small.

**Step 2 — the model fields.**

```go
// Secrets editor. sec is the host-side store (a pointer, so the value-passed
// model stays cheap to copy). secretsArea holds the KEY=VALUE buffer and
// secretsVM the VM it belongs to.
sec         *secrets.Store
secretsArea textarea.Model
secretsVM   string
secretsErr  error
```

In `New()`, mirror the registry's load:

```go
sec, secErr := secrets.Load()
if sec == nil {
    sec = secrets.NewEmpty()
}
```

and fold `secErr` into the same `m.status` warning path the registry's `loadErr`
already uses. ⚠️ `New()` currently sets `m.status` from `loadErr` only; if both
fail you must not silently drop one. Join them.

**Step 3 — the `e` handler** in `updateDetail`:

```go
case key.Matches(msg, m.keys.Secrets):
    // Deliberately no running-VM guard: secrets live on the host, so they are
    // editable whether or not the VM is up. They reach the guest on next start.
    return m, m.openSecrets(m.detail.Name)
```

`openSecrets` seeds the textarea:

```go
func (m *model) openSecrets(name string) tea.Cmd {
    ta := textarea.New()
    ta.SetValue(renderPairs(m.sec.Get(name))) // "KEY=VALUE\n", keys sorted
    ta.SetWidth(max(20, m.width-8))
    ta.SetHeight(max(5, m.height-14))
    m.secretsArea = ta
    m.secretsVM = name
    m.secretsErr = nil
    m.view = viewSecrets
    return ta.Focus()
}
```

`renderPairs` here is the *editor's* `KEY=VALUE` form — **not** `secrets.Render`,
which emits `export KEY='…'` for the guest shell. Two different serializations for
two different consumers; do not conflate them. Name them so nobody does.

⚠️ **`textarea` and `ctrl+s`/`esc`.** The textarea consumes most keys as text.
Handle `ctrl+s` and `esc` *before* forwarding to it, exactly as `updateForm` does
for the create form (`internal/ui/form.go:448-459`).

**Step 4 — parsing.** Keep it pure and testable, free of any model state:

```go
// parseSecrets turns the editor buffer into pairs. Blank lines and #-comments are
// ignored. A line splits on its FIRST '=', so a value may contain '='. The key is
// trimmed; the value is not, since a trailing space can be significant. Any bad
// line aborts the whole parse — a partial save would silently drop a secret.
func parseSecrets(text string) (map[string]string, error) {
    pairs := map[string]string{}
    for i, line := range strings.Split(text, "\n") {
        trimmed := strings.TrimSpace(line)
        if trimmed == "" || strings.HasPrefix(trimmed, "#") {
            continue
        }
        k, v, ok := strings.Cut(line, "=")
        if !ok {
            return nil, fmt.Errorf("line %d: expected KEY=VALUE, got %q", i+1, line)
        }
        k = strings.TrimSpace(k)
        if !secrets.ValidKey(k) {
            return nil, fmt.Errorf("line %d: %q is not a valid environment variable name (use letters, digits, underscore; not starting with a digit)", i+1, k)
        }
        if _, dup := pairs[k]; dup {
            return nil, fmt.Errorf("line %d: duplicate key %q", i+1, k)
        }
        pairs[k] = v
    }
    return pairs, nil
}
```

Note `strings.Cut` splits on the first separator — exactly the semantics wanted.

**Step 5 — `ctrl+s`.** Parse, and on error set `m.secretsErr` and **stay** on the
screen (do not persist, do not navigate). On success call `m.sec.Set(...)`, and if
*that* errors surface it the same way. Only on a fully successful write set
`m.view = viewDetail` and `m.status = "secrets saved for " + name +
" — they apply on next start"`.

That status text matters: it is the only place the UI tells the user their edit is
not live yet.

**Step 6 — `View()` and `forward()`.** Add `case viewSecrets:` to both. `forward`
must deliver the textarea's cursor-blink messages:

```go
case viewSecrets:
    m.secretsArea, cmd = m.secretsArea.Update(msg)
    return m, cmd
```

Forgetting this leaves the cursor frozen — a symptom that looks like a hang.

**Step 7 — the view.** Title `Secrets: <vm>`, the textarea, the error if any, and
a warning rendered with `warnStyle`:

```
Values are shown in cleartext and stored unencrypted on this host (0600).
They are written into the VM on its next start.
```

Then `m.help.ShortHelpView(m.viewHelp())`. Add a `viewSecrets` arm to `viewHelp()`
returning `{m.keys.Submit, m.keys.Back}` — ⚠️ this *is* a `keys.go` edit. It is the
one exception: task 5 could not add a help arm for a view that did not exist.
Add only the `viewHelp` arm; do not touch the `keyMap` struct or `defaultKeys()`.

Note `m.keys.Submit`'s help text is `"ctrl+s", "create"` — wrong here. Either add a
`Save` binding (`ctrl+s`, "save") in this task, or accept the mismatch. Prefer
adding `Save`; it is a two-line change and "create" on a secrets screen is a bug.

**Testing philosophy.** Write a few tests, mostly integration. Meaningful tests
verify custom business logic, critical paths, and edge cases specific to this
application — test *your* code, not the framework or library.

Write tests for: custom business logic and algorithms; critical user workflows
and data transformations; edge cases and error conditions for core functionality;
integration points between components; complex validation logic or calculations.

Do NOT write tests for: third-party library functionality; framework features;
simple CRUD operations without custom logic; trivial getters/setters or static
configuration; obvious functionality that would break immediately if incorrect.

Here that means: `parseSecrets` is pure custom validation logic and gets a
table-driven test (the five cases in the Acceptance Criteria). The
open-on-stopped-VM and esc-discards behaviours are the critical user workflows.
Do not test `bubbles/textarea`'s editing.

</details>
