---
id: 4
group: "testing"
dependencies: [1, 2, 3]
status: "pending"
created: "2026-07-03"
skills:
  - "go"
  - "unit-testing"
---
# Unit tests for search key-routing and disk-usage measurement/formatting

## Objective
Add focused unit tests for the two pieces of new custom logic in this plan — the search key-routing/filtering behaviour (Task 1) and the disk-usage helper plus its blank-vs-`0 B` formatting/fallback (Tasks 2 & 3) — runnable locally with `go test ./...`. CI does not run the TUI's Go tests today (that wiring is deferred to a separate plan), so these tests protect the new logic locally, consistent with the package's existing coverage style.

## Skills Required
- `go`: table-driven and message-driven tests in the `ui` package.
- `unit-testing`: exercising a Bubble Tea model by feeding `tea.KeyMsg` through `Update` and asserting on resulting state, following the existing `model_test.go` patterns.

## Acceptance Criteria
- [ ] A search key-routing test enters search mode (`/`), feeds each action-letter rune (`s`, `x`, `d`, `r`, `S`, `f`, `n`, `q`) and asserts `searchQuery` accumulated them and **no** action fired (no confirm overlay, no lifecycle status/command triggered).
- [ ] A search test asserts `esc` clears `searchQuery` and exits searching, and that `enter` exits searching while **keeping** the query (rows stay filtered).
- [ ] A search filtering test loads a small set of VMs, types a name fragment, and asserts `refreshRows` shows only rows whose name matches (case-insensitive), and that it **composes** with the `f` managed-only filter.
- [ ] A disk-usage test creates a temp dir containing a `disk` file of known size, asserts `diskUsedBytes` returns a positive value, and asserts it returns `-1` when the dir has no `disk` file (and when `dir == ""`).
- [ ] A formatting/fallback test asserts that an unknown/empty `DiskUsed` renders **blank** (`humanizeBytes("") == ""`), distinct from a genuine `0 B`.
- [ ] `go test ./...` passes in the `tui/` module.

Use your internal Todo tool to track these and keep on track.

## Meaningful Test Strategy Guidelines

Your critical mantra for test generation is: "write a few tests, mostly integration".

**Definition of "Meaningful Tests":** Tests that verify custom business logic, critical paths, and edge cases specific to the application. Focus on testing YOUR code, not the framework or library functionality.

**When TO Write Tests:**
- Custom business logic and algorithms
- Critical user workflows and data transformations
- Edge cases and error conditions for core functionality
- Integration points between different system components
- Complex validation logic or calculations

**When NOT to Write Tests:**
- Third-party library functionality (already tested upstream)
- Framework features (React hooks, Express middleware, etc.)
- Simple CRUD operations without custom logic
- Getter/setter methods or basic property access
- Configuration files or static data
- Obvious functionality that would break immediately if incorrect

**Test Task Creation Rules:**
- Combine related test scenarios into single tasks (e.g., "Test user authentication flow" not separate tasks for login, logout, validation)
- Focus on integration and critical path testing over unit test coverage
- Avoid creating separate tasks for testing each CRUD operation individually
- Question whether simple functions need dedicated test tasks

Here, the meaningful targets are the **key-routing interception** (every action rune must edit the query, not act) and the **allocated-block measurement + blank fallback** (an unmeasurable disk must render blank, not `0 B`). Do **not** test `bubbles/table` navigation, `golang.org/x/sys/unix` itself, or the already-covered `humanizeBytes` number formatting beyond the empty-string case.

## Technical Requirements
- Package: `ui` (add tests to `tui/internal/ui/`, e.g. a new `search_test.go` and `diskusage_test.go`, or extend `model_test.go`/`format_test.go`).
- Reuse the existing `newTestModel(t)` helper (`model_test.go` ~line 25) and the pattern of driving the model with `m.Update(tea.KeyMsg{...})` then type-asserting the returned `tea.Model` back to `model`.
- Feed runes as `tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")}`; feed `esc`/`enter`/`backspace` as `tea.KeyMsg{Type: tea.KeyEsc}` / `tea.KeyEnter` / `tea.KeyBackspace`; feed `/` as `tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")}`.
- Use `t.TempDir()` and `os.WriteFile`/`os.Truncate` to build a `disk` file for the disk-usage test.

## Input Dependencies
- Task 1: search mode (`searching`, `searchQuery`, key routing, `refreshRows` filter).
- Task 2: `diskUsedBytes`.
- Task 3: `vm.VM.DiskUsed`, its population on `vmsLoadedMsg`, and the blank-cell rendering.

## Output Artifacts
- `*_test.go` files that pass under `go test ./...`, guarding the new logic against regression.

## Implementation Notes

<details>
<summary>Detailed implementation guidance</summary>

**Search key-routing test (follow `model_test.go` style):**
```go
func TestSearchCapturesActionKeys(t *testing.T) {
	m := newTestModel(t)
	// enter search
	mi, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = mi.(model)
	if !m.searching {
		t.Fatal("expected searching mode after '/'")
	}
	for _, r := range []rune{'s', 'x', 'd', 'r', 'S', 'f', 'n', 'q'} {
		mi, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mi.(model)
	}
	if m.searchQuery != "sxdrSfnq" {
		t.Fatalf("query = %q, want the typed action letters", m.searchQuery)
	}
	if m.confirming {
		t.Fatal("an action fired while searching (delete confirm opened)")
	}
	// esc clears + exits
	mi, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mi.(model)
	if m.searching || m.searchQuery != "" {
		t.Fatal("esc should clear the query and exit search")
	}
}
```

**Search filter + compose test:** seed `m.vms` with a couple of VMs (via the same mechanism `newTestModel` / existing tests use to set `m.vms`, or by sending a `vmsLoadedMsg` if that type is constructible in-package), type a fragment, call `m.refreshRows()` (or rely on the live refresh in the interception), and assert `m.table.Rows()` contains only the matching name. Then set `m.managedOnly = true` and assert the two filters intersect. Keep the VM set tiny.

**Disk-usage helper test:**
```go
func TestDiskUsedBytes(t *testing.T) {
	dir := t.TempDir()
	// empty dir → no disk file → -1
	if got := diskUsedBytes(dir); got != -1 {
		t.Fatalf("missing disk: got %d, want -1", got)
	}
	// create a disk file and expect a positive allocated size
	f := filepath.Join(dir, "disk")
	if err := os.WriteFile(f, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := diskUsedBytes(dir); got <= 0 {
		t.Fatalf("present disk: got %d, want > 0", got)
	}
	if got := diskUsedBytes(""); got != -1 {
		t.Fatalf("empty dir arg: got %d, want -1", got)
	}
}
```
(This test is inherently unix-only in behaviour; that is fine — the CI/dev host is Linux. If you want it to skip cleanly elsewhere, guard the positive-value assertion behind `runtime.GOOS`.)

**Formatting fallback:** a one-liner asserting `humanizeBytes("") == ""` (blank) versus `humanizeBytes("0") == "0"` documents that an unmeasured disk renders blank rather than `0 B`. `format_test.go` already covers the numeric cases; only add the blank-vs-zero intent if not already obvious.

Run `cd tui && go test ./...` to confirm everything passes.
</details>
