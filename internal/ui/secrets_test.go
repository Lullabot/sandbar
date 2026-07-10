package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// parseSecrets is pure custom validation logic: table-driven coverage of the
// blank/comment skip, first-'='-only split, key validation, and duplicate
// rejection the acceptance criteria call out.
func TestSecretsParsePairs(t *testing.T) {
	pairs, err := parseSecrets("A=1\n\n# c\nB=x=y\n")
	if err != nil {
		t.Fatalf("parseSecrets returned an unexpected error: %v", err)
	}
	want := map[string]string{"A": "1", "B": "x=y"}
	if len(pairs) != len(want) {
		t.Fatalf("parseSecrets() = %v, want %v", pairs, want)
	}
	for k, v := range want {
		if got, ok := pairs[k]; !ok || got != v {
			t.Errorf("pairs[%q] = %q, want %q", k, got, v)
		}
	}
}

// A line with no '=' at all names the offending line and content.
func TestSecretsParseNoEqualsSign(t *testing.T) {
	_, err := parseSecrets("nokeyvalue")
	if err == nil {
		t.Fatal("parseSecrets should reject a line with no '='")
	}
	if !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "nokeyvalue") {
		t.Fatalf("error %q should mention line 1 and the offending content", err.Error())
	}
}

// An invalid key names the offending line and key, and the whole line is
// rejected before anything downstream could persist it.
func TestSecretsParseInvalidKeyNamesLineAndKey(t *testing.T) {
	_, err := parseSecrets("2BAD=x")
	if err == nil {
		t.Fatal("parseSecrets should reject an invalid key")
	}
	if !strings.Contains(err.Error(), "2BAD") {
		t.Fatalf("error %q should mention the offending key %q", err.Error(), "2BAD")
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("error %q should mention line 1", err.Error())
	}
}

// A duplicate key aborts the whole parse (last-wins would silently discard a
// secret the user typed) and names the key.
func TestSecretsParseDuplicateKey(t *testing.T) {
	_, err := parseSecrets("A=1\nA=2")
	if err == nil {
		t.Fatal("parseSecrets should reject a duplicate key")
	}
	if !strings.Contains(err.Error(), "A") {
		t.Fatalf("error %q should name the duplicate key %q", err.Error(), "A")
	}
}

// openSecretsViaKey drives the real 'e' key on the detail screen, mirroring a
// real session rather than calling openSecrets directly.
func openSecretsViaKey(m model, name, status string) model {
	m.view = viewDetail
	m.detail.Name = name
	m.detail.Status = status
	after, _ := m.Update(runeKey('e'))
	return after.(model)
}

// 'e' opens the secrets editor regardless of VM status — the whole point of
// the feature is that secrets live on the host and are editable whether or
// not the VM happens to be running.
func TestSecretsEditorOpensRegardlessOfStatus(t *testing.T) {
	m := newTestModel(t)
	m = openSecretsViaKey(m, "claude", "Stopped")

	if m.view != viewSecrets {
		t.Fatalf("'e' on a stopped VM should open the secrets editor, view = %v", m.view)
	}
	if m.secretsVM != "claude" {
		t.Fatalf("secretsVM = %q, want %q", m.secretsVM, "claude")
	}
}

// esc discards the buffer: it returns to the detail view and does not call
// Store.Set (verified by checking nothing was persisted).
func TestSecretsEditorEscDiscards(t *testing.T) {
	m := newTestModel(t)
	m = openSecretsViaKey(m, "claude", "Running")
	m.secretsArea.SetValue("A=1\n")

	after, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = after.(model)

	if m.view != viewDetail {
		t.Fatalf("esc should return to the detail view, got %v", m.view)
	}
	if got := m.sec.Get("claude"); len(got) != 0 {
		t.Fatalf("esc must not persist anything, Store.Get returned %v", got)
	}
}

// ctrl+s on a valid buffer persists via Store.Set, returns to the detail
// view, and sets a status line explaining the edit is not live until the next
// start (the only place the UI says so).
func TestSecretsEditorSaveValidPersists(t *testing.T) {
	m := newTestModel(t)
	m = openSecretsViaKey(m, "claude", "Stopped")
	m.secretsArea.SetValue("A=1\nB=2\n")

	after, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = after.(model)

	if m.view != viewDetail {
		t.Fatalf("a valid save should return to the detail view, got %v", m.view)
	}
	got := m.sec.Get("claude")
	if got["A"] != "1" || got["B"] != "2" {
		t.Fatalf("Store.Get(%q) = %v, want {A:1 B:2}", "claude", got)
	}
	if !strings.Contains(m.status, "claude") || !strings.Contains(m.status, "next start") {
		t.Fatalf("status %q should name the VM and note it applies on next start", m.status)
	}
}

// An invalid buffer (bad key) aborts the save: the editor stays open with an
// error, and Store.Set is never reached — nothing is persisted.
func TestSecretsEditorSaveInvalidStaysAndDoesNotPersist(t *testing.T) {
	m := newTestModel(t)
	m = openSecretsViaKey(m, "claude", "Stopped")
	m.secretsArea.SetValue("2BAD=x\n")

	after, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = after.(model)

	if m.view != viewSecrets {
		t.Fatalf("an invalid save must stay on the editor, view = %v", m.view)
	}
	if m.secretsErr == nil {
		t.Fatal("an invalid save should surface a parse error")
	}
	if got := m.sec.Get("claude"); len(got) != 0 {
		t.Fatalf("an invalid save must not persist anything, Store.Get returned %v", got)
	}
}
