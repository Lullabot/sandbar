---
id: 3
group: "browser"
dependencies: [2]
status: "pending"
created: 2026-07-03
skills:
  - go
  - bubble-tea
complexity_score: 5
complexity_notes: "A reusable bubbles/list component with async DirLister loading, a distinct navigate-vs-select affordance, path join/parent handling, and a fuzzy filter; single package, one focused unit test with a fake lister."
---
# Build the reusable `bubbles/list` file browser over `DirLister`

## Objective
Build ONE source-agnostic file browser (used for both host and guest) on
`bubbles/list` — chosen for its built-in fuzzy filtering. It renders `DirEntry`
items from an injected `DirLister`, navigates into directories, and offers a
**distinct select affordance** so choosing a directory as a recursive-copy source
never collides with entering it. It loads directory contents asynchronously (via
a `tea.Cmd`, since the guest lister shells out) and exposes the chosen absolute
path to the caller.

## Skills Required
- **go**, **bubble-tea**: `bubbles/list` item model, `tea.Cmd`/`tea.Msg`, key handling.

## Acceptance Criteria
- [ ] In `tui/internal/browse`, `type Browser` wraps a `list.Model` whose items are `DirEntry`s from a `DirLister`; a `NewBrowser(lister DirLister, title string) Browser` constructor and an `Open(path string) tea.Cmd` that (re)lists at `path` are provided.
- [ ] Listing runs off the Update goroutine: `Open`/navigation return a `tea.Cmd` that calls `lister.List(ctx, path)` and yields a browser message; the browser's `Update(msg) (Browser, tea.Cmd)` applies loaded entries (and surfaces a load error as a visible status, not a panic).
- [ ] **Enter** on a highlighted **directory** navigates into it (re-lists at the joined child path); Enter on a **file** selects it. A separate, documented **select key** (e.g. `ctrl+s`, help text "select") selects the *highlighted* entry regardless of type — selecting a directory marks the transfer as recursive. A `..` affordance navigates to the parent path.
- [ ] The browser exposes the outcome: `Selected() (path string, isDir bool, ok bool)` (or an equivalent message the root model can read), returning the absolute path of the chosen entry. Path joining uses POSIX `/` semantics (host targets are macOS/Linux, guests are POSIX); the parent of `/a/b` is `/a`, and the parent of `/` is `/`.
- [ ] The built-in fuzzy filter works (list's `/` filter) and does not swallow the navigate/select keys while filtering (delegate to `list.Update` when `FilterState()==list.Filtering`).
- [ ] A unit test drives the `Browser` with a **fake `DirLister`** (in-memory tree, no real VM): `Open` populates items; entering a directory issues a load `tea.Cmd` for the child path; pressing the select key on a directory reports `ok`, the right absolute path, and `isDir==true`.
- [ ] `cd tui && gofmt -l . && go build ./... && go vet ./... && go test ./...` all pass.

Use your internal Todo tool to track these and keep on track.

## Technical Requirements
- Use `github.com/charmbracelet/bubbles/list` (already a dependency, v1.0.0). Define a `list.Item` wrapper for `DirEntry` implementing `Title()`, `Description()` (e.g. humanized size or `<dir>`), and `FilterValue()` (the name).
- The `Browser` value must be copy-safe (it is embedded in the root model, which is passed by value through Update): store the `DirLister` (interface), a `list.Model`, the current path string, and a small selection result — no `strings.Builder`, no mutexes.
- Do NOT block Update: every `List` call is wrapped in a `tea.Cmd`; the browser handles its own `dirLoadedMsg{path string; entries []DirEntry; err error}` type.

## Input Dependencies
- Task 2: `DirLister`, `DirEntry`, and the two lister constructors (`NewLocalLister`, `NewGuestLister`).

## Output Artifacts
- `tui/internal/browse/browser.go` — `Browser`, its item wrapper, `Open`, `Update`, `View`, `Selected`, and the `dirLoadedMsg` type.
- `tui/internal/browse/browser_test.go` — fake-lister-driven navigation/selection test.

## Implementation Notes

<details>
<summary>Detailed implementation steps</summary>

1. Item wrapper:
   ```go
   type item struct{ e DirEntry }
   func (i item) Title() string {
       if i.e.IsDir { return i.e.Name + "/" }
       return i.e.Name
   }
   func (i item) Description() string {
       if i.e.IsDir { return "<dir>" }
       return humanizeSize(i.e.Size) // small local helper, or fmt bytes
   }
   func (i item) FilterValue() string { return i.e.Name }
   ```

2. `Browser` struct and constructor:
   ```go
   type Browser struct {
       lister   DirLister
       list     list.Model
       path     string           // current absolute dir
       selPath  string           // last selection
       selDir   bool
       selected bool
       err      error
   }
   func NewBrowser(lister DirLister, title string) Browser {
       l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
       l.Title = title
       return Browser{lister: lister, list: l}
   }
   ```

3. Async load command + message:
   ```go
   type dirLoadedMsg struct {
       path    string
       entries []DirEntry
       err     error
   }
   func (b Browser) Open(path string) tea.Cmd {
       lister, p := b.lister, path
       return func() tea.Msg {
           entries, err := lister.List(context.Background(), p)
           return dirLoadedMsg{path: p, entries: entries, err: err}
       }
   }
   ```
   In `Update`, on `dirLoadedMsg`: if `err != nil` set `b.err` and keep the old
   items; else set `b.path = msg.path`, build `[]list.Item` (prepend a synthetic
   `..` item unless already at `/`), and `b.list.SetItems(items)`.

4. Key handling in `Update` (before delegating to `list.Update`):
   - If `b.list.FilterState() == list.Filtering`, delegate straight to `list.Update` so typing filters.
   - On the **select** key (bind `ctrl+s`): read the highlighted `item`; set `selPath = join(b.path, name)` (or `b.path` semantics for `..`), `selDir = e.IsDir`, `selected = true`; return no reload cmd (the root model reacts to `Selected()`).
   - On **enter**: if highlighted is `..`, return `b.Open(parent(b.path))`; if a directory, return `b.Open(join(b.path, name))`; if a file, behave like select.
   - Otherwise delegate to `list.Update` (arrow keys, filter start, paging).
   - Provide `join(dir, name)` and `parent(dir)` using `path` (POSIX) not `path/filepath`, so guest paths are handled identically on any host OS.

5. `Selected() (string, bool, bool) { return b.selPath, b.selDir, b.selected }`.
   Add a `Reset()`/clearing of `selected` if the same Browser value is reused.

6. `View() string { return b.list.View() }` (plus an error line when `b.err != nil`).
   Size the inner list from the root model's WindowSizeMsg via a `SetSize(w, h)` method.

7. Test with a fake in-memory lister:
   ```go
   type fakeLister map[string][]DirEntry
   func (f fakeLister) List(_ context.Context, p string) ([]DirEntry, error) { return f[p], nil }
   ```
   Seed `{"/root": {{Name:"sub",IsDir:true},{Name:"a.txt",Size:3}}, "/root/sub": {...}}`.
   Drive: `b := NewBrowser(f, "test")`; run the `Open("/root")` cmd, feed its
   `dirLoadedMsg` to `Update`; assert items include `sub/` and `a.txt`. Move the
   cursor to `sub`, press the select key, assert `Selected()` == `("/root/sub", true, true)`.
   Separately assert Enter on `sub` returns a non-nil cmd whose message targets
   `/root/sub`.
</details>

### Meaningful Test Strategy Guidelines
Your critical mantra for test generation is: "write a few tests, mostly integration".
- **DO** test YOUR logic: path join/parent, navigate-vs-select disambiguation, and that Enter into a directory schedules a reload — driven by a fake `DirLister`.
- **DON'T** test `bubbles/list` internals (filtering, rendering) or spin up a VM.
