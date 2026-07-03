package ui

import (
	"testing"

	"github.com/deviantintegral/claude-code-ansible/tui/internal/vm"

	tea "github.com/charmbracelet/bubbletea"
)

// rowNames extracts the Name column (row[0]) from every row currently in the
// table, so a test can assert exactly which VMs survived the filters.
func rowNames(m model) []string {
	rows := m.table.Rows()
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r[0])
	}
	return names
}

// contains reports whether names includes want.
func contains(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// While searching, every action-letter key must edit the query rather than fire
// its binding: typing the action runs 's'(start) 'x'(stop) 'd'(delete)
// 'r'(restart) 'S'(shell) 'f'(filter) 'n'(new) 'q'(quit) must accumulate into
// searchQuery and open no overlay/action. esc then clears the query and exits.
func TestSearchCapturesActionKeys(t *testing.T) {
	m := newTestModel(t)

	// A populated list makes any stray action (e.g. delete-confirm) observable.
	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Running", CPUs: 2},
	}})
	m = loaded.(model)

	// Enter search mode with '/'.
	mi, _ := m.Update(runeKey('/'))
	m = mi.(model)
	if !m.searching {
		t.Fatal("expected searching mode after '/'")
	}

	for _, r := range []rune{'s', 'x', 'd', 'r', 'S', 'f', 'n', 'q'} {
		mi, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mi.(model)
	}

	if m.searchQuery != "sxdrSfnq" {
		t.Fatalf("query = %q, want the typed action letters %q", m.searchQuery, "sxdrSfnq")
	}
	// No action may have fired while searching.
	if m.confirming {
		t.Fatal("an action fired while searching (delete confirm opened)")
	}
	if m.acting {
		t.Fatal("a lifecycle action fired while searching")
	}
	if m.view != viewList {
		t.Fatalf("searching must stay on the list, view = %v", m.view)
	}

	// esc clears the query and exits search.
	mi, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mi.(model)
	if m.searching || m.searchQuery != "" {
		t.Fatalf("esc should clear the query and exit search (searching=%v query=%q)", m.searching, m.searchQuery)
	}
}

// enter exits search mode but keeps the committed query, so the rows stay
// filtered for normal table navigation.
func TestSearchEnterKeepsFilter(t *testing.T) {
	m := newTestModel(t)

	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Running", CPUs: 2},
		{Name: "claude-two", Status: "Stopped", CPUs: 2},
		{Name: "other", Status: "Running", CPUs: 2},
	}})
	m = loaded.(model)

	// Search for a fragment that matches exactly one VM.
	mi, _ := m.Update(runeKey('/'))
	m = mi.(model)
	for _, r := range []rune{'t', 'w', 'o'} {
		mi, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mi.(model)
	}

	// Commit with enter: leave search but keep the query and the filtered rows.
	mi, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = mi.(model)
	if m.searching {
		t.Fatal("enter should exit search mode")
	}
	if m.searchQuery != "two" {
		t.Fatalf("enter must keep the query, got %q", m.searchQuery)
	}
	names := rowNames(m)
	if len(names) != 1 || names[0] != "claude-two" {
		t.Fatalf("rows should stay filtered to the match, got %v", names)
	}
}

// Search filtering matches names case-insensitively and composes (logical AND)
// with the managed-only filter. Here "claude" is registered managed while
// "claude-two"/"other" are not, so search alone yields both claude* rows but
// intersecting with managed-only narrows to just the managed "claude".
func TestSearchFilterComposesWithManaged(t *testing.T) {
	m := newTestModel(t)

	// Register one managed VM so the managed-only filter has something to keep.
	if err := m.reg.Add(vm.CreateConfig{Name: "claude", BaseName: "claude-base"}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	loaded, _ := m.Update(vmsLoadedMsg{vms: []vm.VM{
		{Name: "claude", Status: "Running", CPUs: 2},
		{Name: "claude-two", Status: "Stopped", CPUs: 2},
		{Name: "other", Status: "Running", CPUs: 2},
	}})
	m = loaded.(model)

	// Type a fragment with different casing than the names to prove the match is
	// case-insensitive: "CLAUDE" matches "claude" and "claude-two", not "other".
	mi, _ := m.Update(runeKey('/'))
	m = mi.(model)
	for _, r := range []rune{'C', 'L', 'A', 'U', 'D', 'E'} {
		mi, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mi.(model)
	}

	names := rowNames(m)
	if len(names) != 2 || !contains(names, "claude") || !contains(names, "claude-two") {
		t.Fatalf("case-insensitive search should match both claude* rows, got %v", names)
	}
	if contains(names, "other") {
		t.Fatalf("search must exclude the non-matching VM, got %v", names)
	}

	// Compose with the managed-only filter: only "claude" is managed, so the
	// intersection of (name contains "claude") AND (managed|base) is just it.
	m.managedOnly = true
	m.refreshRows()
	names = rowNames(m)
	if len(names) != 1 || names[0] != "claude" {
		t.Fatalf("managed-only should intersect the search to the managed VM, got %v", names)
	}
}
