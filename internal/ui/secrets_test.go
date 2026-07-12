package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The editor must fit the terminal and its guidance must reflow to the content
// width, not collapse into a narrow ribbon. Before the fix the scoping tip was
// styled with the fixed 18-column labelStyle, so it wrapped into ~9 short lines
// that pushed the whole view past the terminal height and scrolled the title
// off the top (the PR feedback: "The secrets manager rendering is broken").
func TestSecretsViewFitsAndTipReflows(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 100, 30)
	m.openSecrets("claude")

	view := m.secretsView()
	lines := strings.Split(view, "\n")
	if len(lines) > m.height {
		t.Fatalf("secretsView rendered %d lines, exceeding the %d-row terminal — it will scroll the title off the top", len(lines), m.height)
	}

	// The tip must wrap to roughly the content width, not the old 18-column label
	// column: find its widest line and require it to be well past 18 columns.
	widest := 0
	for _, ln := range lines {
		if strings.Contains(ln, "scope git auth") || strings.Contains(ln, "Tip:") {
			if w := lipgloss.Width(ln); w > widest {
				widest = w
			}
		}
	}
	if widest <= 18 {
		t.Fatalf("the scoping tip wrapped to %d columns, want it reflowed to the content width (not the 18-col label column)", widest)
	}
}

// parseSecrets is pure custom validation logic: table-driven coverage of the
// blank/comment skip, first-'='-only split, key validation, and duplicate
// rejection the acceptance criteria call out.
func TestSecretsParsePairs(t *testing.T) {
	scopes, err := parseSecrets("A=1\n\n# c\nB=x=y\n")
	if err != nil {
		t.Fatalf("parseSecrets returned an unexpected error: %v", err)
	}
	pairs := scopes[""]
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

// renderPairsForEditor -> parseSecrets round-trips for a multi-scope VM: the
// global section renders headerless first, then each non-empty scope as a
// sorted [scope] section with sorted keys, and re-parsing reproduces the
// original scope map exactly.
func TestSecretsRoundTripMultiScope(t *testing.T) {
	scopes := map[string]map[string]string{
		"":                {"EDITOR": "vim", "A": "1"},
		"github.com/acme": {"GH_TOKEN": "ghp_xxx", "OTHER": "value"},
		"gitlab.com/team": {"GITLAB_TOKEN": "glpat_yyy"},
	}

	text := renderPairsForEditor(scopes)
	got, err := parseSecrets(text)
	if err != nil {
		t.Fatalf("parseSecrets(renderPairsForEditor(scopes)) returned an unexpected error: %v\ntext:\n%s", err, text)
	}

	if len(got) != len(scopes) {
		t.Fatalf("round trip scope count = %d, want %d (got %v)", len(got), len(scopes), got)
	}
	for scope, pairs := range scopes {
		gotPairs, ok := got[scope]
		if !ok {
			t.Fatalf("round trip missing scope %q", scope)
		}
		if len(gotPairs) != len(pairs) {
			t.Fatalf("scope %q: got %v, want %v", scope, gotPairs, pairs)
		}
		for k, v := range pairs {
			if gotPairs[k] != v {
				t.Errorf("scope %q key %q = %q, want %q", scope, k, gotPairs[k], v)
			}
		}
	}

	// Byte-stable: rendering again produces identical text.
	if again := renderPairsForEditor(scopes); again != text {
		t.Fatalf("renderPairsForEditor is not byte-stable:\nfirst:\n%s\nsecond:\n%s", text, again)
	}
}

// The global section is headerless and rendered first, before any [scope]
// section — the recommended grammar's defining property.
func TestSecretsRenderGlobalFirstHeaderless(t *testing.T) {
	scopes := map[string]map[string]string{
		"":         {"A": "1"},
		"some/dir": {"B": "2"},
	}
	text := renderPairsForEditor(scopes)
	if !strings.HasPrefix(text, "A=1\n") {
		t.Fatalf("expected global section headerless and first, got:\n%s", text)
	}
	if idx := strings.Index(text, "[some/dir]"); idx <= 0 {
		t.Fatalf("expected a [some/dir] header after the global section, got:\n%s", text)
	}
}

// A [scope] header with an invalid scope aborts the whole parse with a
// per-line error naming the offending scope.
func TestSecretsParseInvalidScopeHeader(t *testing.T) {
	_, err := parseSecrets("[../escape]\nA=1\n")
	if err == nil {
		t.Fatal("parseSecrets should reject an invalid scope header")
	}
	if !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "../escape") {
		t.Fatalf("error %q should mention line 1 and the offending scope", err.Error())
	}
}

// A duplicate key is only rejected within the SAME scope; the same key name
// may appear once per scope.
func TestSecretsParseDuplicateKeyAcrossScopesAllowed(t *testing.T) {
	got, err := parseSecrets("A=1\n\n[org/dir]\nA=2\n")
	if err != nil {
		t.Fatalf("parseSecrets should allow the same key in different scopes, got error: %v", err)
	}
	if got[""]["A"] != "1" || got["org/dir"]["A"] != "2" {
		t.Fatalf("parseSecrets() = %v, want global A=1 and org/dir A=2", got)
	}
}

// A duplicate key within the SAME scope is still rejected.
func TestSecretsParseDuplicateKeySameScope(t *testing.T) {
	_, err := parseSecrets("[org/dir]\nA=1\nA=2\n")
	if err == nil {
		t.Fatal("parseSecrets should reject a duplicate key within the same scope")
	}
	if !strings.Contains(err.Error(), "A") {
		t.Fatalf("error %q should name the duplicate key", err.Error())
	}
}

// A buffer pasted with CRLF line endings must not leak a trailing "\r" into
// any parsed VALUE (or KEY). Before the fix, parseSecrets split on the raw
// (untrimmed) line, so every line's "\r" — left behind by
// strings.Split(text, "\n") — landed inside the value, and Render would
// single-quote it straight into the guest's secrets.env.
func TestSecretsParseCRLFDoesNotCorruptValues(t *testing.T) {
	scopes, err := parseSecrets("A=1\r\n[org/dir]\r\nGH_TOKEN=ghp_x\r\n")
	if err != nil {
		t.Fatalf("parseSecrets returned an unexpected error on a CRLF buffer: %v", err)
	}
	for scope, pairs := range scopes {
		for k, v := range pairs {
			if strings.ContainsRune(k, '\r') {
				t.Fatalf("scope %q key %q carries a trailing \\r", scope, k)
			}
			if strings.ContainsRune(v, '\r') {
				t.Fatalf("scope %q key %q value %q carries a trailing \\r", scope, k, v)
			}
		}
	}
	if scopes[""]["A"] != "1" {
		t.Fatalf(`scopes[""]["A"] = %q, want "1"`, scopes[""]["A"])
	}
	if scopes["org/dir"]["GH_TOKEN"] != "ghp_x" {
		t.Fatalf(`scopes["org/dir"]["GH_TOKEN"] = %q, want "ghp_x"`, scopes["org/dir"]["GH_TOKEN"])
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

// typeInto feeds s through the model's Update one rune at a time, exactly as a
// real keyboard would, so it exercises the whole key-routing path (Update ->
// updateSecrets -> textarea) rather than poking the textarea directly. Newlines
// are sent as the Enter key, which the textarea turns into a line break.
func typeInto(m model, s string) model {
	for _, r := range s {
		var msg tea.KeyMsg
		if r == '\n' {
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		} else {
			msg = runeKey(r)
		}
		after, _ := m.Update(msg)
		m = after.(model)
	}
	return m
}

// The editor must be focused the moment it opens, or the textarea silently
// drops every keystroke. This is the direct regression guard for the
// value-copy-then-Focus bug (Focus mutated a local copy, leaving the stored
// textarea blurred so the user could not type).
func TestSecretsEditorIsFocusedOnOpen(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 100, 30)
	m = openSecretsViaKey(m, "claude", "Stopped")

	if !m.secretsArea.Focused() {
		t.Fatal("the secrets editor must be focused on open, or keystrokes are dropped")
	}
}

// End-to-end: open the editor with 'e', TYPE a KEY=VALUE pair through the real
// key path, and ctrl+s. The typed text must reach the buffer and persist. Had
// the editor opened blurred, the buffer would stay empty and nothing would
// save — which is exactly the "I can't add a secret" report.
func TestSecretsEditorTypeInsertsAndSaves(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 100, 30)
	m = openSecretsViaKey(m, "claude", "Stopped")

	m = typeInto(m, "FOO=bar")
	if got := m.secretsArea.Value(); !strings.Contains(got, "FOO=bar") {
		t.Fatalf("typed text never reached the editor buffer, value = %q", got)
	}

	after, _ := m.Update(ctrlKey('s'))
	m = after.(model)

	if m.view != viewDetail {
		t.Fatalf("a valid save should return to the detail view, got %v", m.view)
	}
	if got := m.sec.Get("claude"); got["FOO"] != "bar" {
		t.Fatalf("typed secret was not persisted, Store.Get = %v", got)
	}
}

// A user can type a multi-line, scoped buffer (global pair, a [scope] header,
// and a scoped pair) and have every scope persist. Exercises Enter-as-newline
// plus the scope grammar through the real key path.
func TestSecretsEditorTypeMultiScopeAndSaves(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 120, 30)
	m = openSecretsViaKey(m, "claude", "Stopped")

	m = typeInto(m, "EDITOR=vim\n[github.com/acme]\nGH_TOKEN=ghp_x")

	after, _ := m.Update(ctrlKey('s'))
	m = after.(model)

	if m.secretsErr != nil {
		t.Fatalf("a valid multi-scope buffer should save cleanly, got error: %v", m.secretsErr)
	}
	scopes := m.sec.GetAll("claude")
	if scopes[""]["EDITOR"] != "vim" {
		t.Fatalf("global scope not persisted, got %v", scopes[""])
	}
	if scopes["github.com/acme"]["GH_TOKEN"] != "ghp_x" {
		t.Fatalf("directory scope not persisted, got %v", scopes["github.com/acme"])
	}
}

// Backspace edits the buffer (a further check that keys are actually routed
// into the focused textarea, not swallowed).
func TestSecretsEditorBackspaceEdits(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 100, 30)
	m = openSecretsViaKey(m, "claude", "Stopped")

	m = typeInto(m, "AB")
	after, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = after.(model)

	if got := m.secretsArea.Value(); got != "A" {
		t.Fatalf("backspace did not edit the buffer, value = %q, want %q", got, "A")
	}
}

// The opened editor must not paint the code-buffer chrome — the line-number
// gutter ("1") or the default "┃ " line prompt that tiled a full-height bar
// down every empty row (both flagged as "hanging" artifacts on the PR).
func TestSecretsEditorHasNoGutterOrPromptBar(t *testing.T) {
	m := newTestModel(t)
	m = resized(m, 100, 30)
	m = openSecretsViaKey(m, "claude", "Stopped")

	if m.secretsArea.ShowLineNumbers {
		t.Fatal("the secrets editor must not show a line-number gutter")
	}
	if m.secretsArea.Prompt != "" {
		t.Fatalf("the secrets editor must not draw a line prompt, got %q", m.secretsArea.Prompt)
	}
	// And the rendered editor must not contain the default vertical prompt bar.
	if strings.Contains(m.secretsArea.View(), "┃") {
		t.Fatal("the rendered editor still contains the ┃ prompt bar")
	}
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

	after, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
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

	after, _ := m.Update(ctrlKey('s'))
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

	after, _ := m.Update(ctrlKey('s'))
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
