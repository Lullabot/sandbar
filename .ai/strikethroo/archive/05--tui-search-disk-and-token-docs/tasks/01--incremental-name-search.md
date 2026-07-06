---
id: 1
group: "search"
dependencies: []
status: "completed"
created: "2026-07-03"
skills:
  - "go"
  - "bubbletea"
---
# Incremental name-search mode bound to `/`

## Objective
Add an incremental name-search mode to the `claude-vm` list view: pressing `/` enters a "searching" state where typed characters build a query that filters the table by case-insensitive name substring, live, as the user types. The mode composes with (does not replace) the existing `f` managed-only filter, `esc` clears the query and exits, `enter` keeps the filter and returns to normal table navigation, and every action-letter rune (`s x d r S f n q` plus `j`/`k`) edits the query instead of firing its action while searching. The new `/` binding appears in the list help bar.

## Skills Required
- `go`: model/state changes and key-routing logic in the `ui` package.
- `bubbletea`: correct interception of `tea.KeyMsg` ahead of the `bubbles/table` component's own navigation handling and the existing action bindings.

## Acceptance Criteria
- [ ] `keys.go` defines a `Search` binding on `/` (`key.WithHelp("/", "search")`) and it is surfaced in the list-view help bar (`viewHelp`).
- [ ] The `model` struct carries a `searching bool` and a `searchQuery string` field.
- [ ] Pressing `/` in the list (when not confirming) enters searching mode.
- [ ] While searching, typed runes append to `searchQuery`; `backspace` removes the last rune; each edit re-runs `refreshRows` so the table filters live.
- [ ] While searching, every action-letter rune (`s`, `x`, `d`, `r`, `S`, `f`, `n`, `q`, upper and lower case) and navigation runes (`j`, `k`) edit the query and do **not** fire actions or move the cursor.
- [ ] `esc` while searching clears `searchQuery`, exits searching mode, and refreshes the (now unfiltered) rows.
- [ ] `enter` while searching exits searching mode but **keeps** the current query/filter, returning to normal table navigation.
- [ ] `ctrl+c` still quits while searching (it is handled earlier in `Update` and must not be captured by the search interception).
- [ ] `refreshRows` applies a case-insensitive substring match on `v.Name` **in addition to** the `managedOnly` predicate (the two compose), and the existing empty-list cursor-reseat guard is preserved.
- [ ] The list view renders the active query near the status line while searching (e.g. a `/claude` prompt line).
- [ ] `go build ./...` and `go vet ./...` pass in the `tui/` module.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Package: `github.com/deviantintegral/claude-code-ansible/tui/internal/ui`.
- Files touched: `tui/internal/ui/keys.go`, `tui/internal/ui/model.go`, `tui/internal/ui/list.go`.
- Uses `github.com/charmbracelet/bubbles/key` and `github.com/charmbracelet/bubbletea` (both already imported). `strings` is already imported in `list.go`.
- The `model` is passed **by value** through `Update`, so only value-safe fields (a `bool` and a `string`) may be added — do not add pointers or `strings.Builder`.

## Input Dependencies
None. This is a self-contained view-layer feature.

## Output Artifacts
- A working `/` search mode in the list view.
- A `searching`/`searchQuery` pair on the `model` and a `Search` keybinding, both consumed later by the docs task (keybinding row) and exercised by the unit-test task.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**1. `keys.go` — add the binding and show it in help.**

- In the `keyMap` struct (around line 7-27), add a field:
  ```go
  Search key.Binding
  ```
- In `defaultKeys()` (around line 30-52), add:
  ```go
  Search: key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
  ```
- In `viewHelp()` `default:` (viewList) branch (around line 70-82), in the **non-confirming** return, add `m.keys.Search`. Prefer a contextual help: while searching, show a compact esc/enter help instead of the full action list. For example:
  ```go
  default: // viewList
      if m.confirming {
          // ... unchanged ...
      }
      if m.searching {
          // esc clears/exits, enter commits the filter
          return []key.Binding{m.keys.Back, m.keys.Enter}
      }
      return []key.Binding{
          m.keys.Enter, m.keys.Shell, m.keys.New, m.keys.Start, m.keys.Stop,
          m.keys.Restart, m.keys.Delete, m.keys.Filter, m.keys.Search, m.keys.Quit,
      }
  ```
  (`m.keys.Back` renders as `esc back`; `m.keys.Enter` as `enter detail` — acceptable as a compact in-search hint. Do not over-engineer new bindings just for help text.)

**2. `model.go` — add the state fields.**

- In the `model` struct (around line 38-94), in the "List + detail." group (near `managedOnly`), add:
  ```go
  searching   bool   // when true, typed keys edit searchQuery instead of firing actions
  searchQuery string // case-insensitive name substring filter, applied in refreshRows
  ```
- No import changes are needed here for this file.

**3. `list.go` — intercept keys while searching, add the entry key, and filter rows.**

- In `updateList` (starts around line 111), **before** the existing `switch` and **after** the `if m.confirming { return m.updateConfirm(msg) }` guard, insert the search interception block so it takes priority over every action binding and the table fall-through:
  ```go
  if m.searching {
      switch msg.Type {
      case tea.KeyEsc:
          m.searching = false
          m.searchQuery = ""
          m.refreshRows()
          return m, nil
      case tea.KeyEnter:
          m.searching = false // keep the query; return to normal table navigation
          return m, nil
      case tea.KeyBackspace:
          if m.searchQuery != "" {
              r := []rune(m.searchQuery)
              m.searchQuery = string(r[:len(r)-1])
              m.refreshRows()
          }
          return m, nil
      case tea.KeyRunes, tea.KeySpace:
          m.searchQuery += string(msg.Runes)
          m.refreshRows()
          return m, nil
      }
      // Swallow any other key (arrows, tab, …) so it neither navigates nor acts.
      return m, nil
  }
  ```
  Note: `ctrl+c` never reaches here — it is intercepted in `Update` (`model.go` ~line 224) before `updateList` is called, so it still quits.
- Add a `case` to the existing `switch` in `updateList` to **enter** search mode (place it anywhere among the action cases; `/` collides with no other binding):
  ```go
  case key.Matches(msg, m.keys.Search):
      m.searching = true
      return m, nil
  ```
- In `refreshRows` (around line 47-76), add the name filter alongside the `managedOnly` predicate. After the existing `if m.managedOnly && !managed && !base { continue }` block, add:
  ```go
  if m.searchQuery != "" &&
      !strings.Contains(strings.ToLower(v.Name), strings.ToLower(m.searchQuery)) {
      continue
  }
  ```
  Leave the trailing cursor-reseat guard (`if len(rows) > 0 && m.table.Cursor() < 0 { m.table.SetCursor(0) }`) intact — it protects against a query that matches nothing and then refills.
- In `listView` (around line 245-271), render the active query. In the `switch` that picks confirm/status output (or immediately after it), add a branch so the query line shows while searching, e.g.:
  ```go
  if m.searching {
      b.WriteString("\n" + statusStyle.Render("/"+m.searchQuery))
  }
  ```
  Keep it near the status line; `statusStyle` already exists in `styles.go`.

**4. Behaviour checks to self-verify (no code, just reasoning):**
- Typing `sxd` while searching must leave `searchQuery == "sxd"` and must NOT start/stop/delete any VM.
- `enter` while searching must leave `searching == false` with `searchQuery` unchanged and the table still filtered; `enter` when **not** searching must still open the detail view (unchanged existing behaviour, since the interception only runs when `m.searching`).
- Filtering never mutates `m.vms` — it only affects which rows `refreshRows` emits — so an action still targets the highlighted (visible) VM.
</details>
