package ui

import (
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"

	tea "github.com/charmbracelet/bubbletea"
)

// secretLine must never leak a cleartext value — every rendered line comes
// from a secrets.RedactedEntry (which structurally carries no cleartext
// field), and this test additionally asserts the exact masked format for
// each category so the panel's list rendering is pinned down the same way
// format_test.go pins humanizeBytes.
func TestSecretLineMasksValues(t *testing.T) {
	const (
		globalSecret = "supersecretvalue"
		githubSecret = "ghp_verysecrettoken"
		dirEnvSecret = "dirsecretvalue"
	)
	store := &secrets.Store{}
	store.SetSecret(secrets.CategoryGlobal, "", "API_KEY", globalSecret)
	store.SetSecret(secrets.CategoryGitHub, "github.com/acme", "", githubSecret)
	store.SetSecret(secrets.CategoryGitHub, "", "", "default-token-value")
	store.SetSecret(secrets.CategoryDirEnv, "some/dir", "TOKEN", dirEnvSecret)

	entries := store.Redacted()
	if len(entries) != 4 {
		t.Fatalf("store.Redacted() returned %d entries, want 4", len(entries))
	}

	var lines []string
	for _, e := range entries {
		line := secretLine(e)
		lines = append(lines, line)
		for _, leak := range []string{globalSecret, githubSecret, dirEnvSecret, "default-token-value"} {
			if strings.Contains(line, leak) {
				t.Errorf("secretLine leaked a cleartext value into %q", line)
			}
		}
		if !strings.Contains(line, "****") {
			t.Errorf("secretLine(%+v) = %q, want a masked value", e, line)
		}
	}

	want := []string{
		"[global]  API_KEY = ****",
		"[github]  github.com/acme = ****",
		"[github]  (default) = ****",
		"[dir_env] some/dir:TOKEN = ****",
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%v", len(lines), len(want), lines)
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d = %q, want %q", i, lines[i], w)
		}
	}
}

// Pressing 's' on the detail view opens the secrets panel for that VM and
// lists its stored secrets — masked (the panel's view string must never
// contain a stored cleartext value, only the "****" mask).
func TestSecretsKeyOpensPanelWithMaskedList(t *testing.T) {
	m := newTestModel(t)
	m.view = viewDetail
	m.detail = vm.VM{Name: "claude", Status: "Running"}

	store, err := secrets.Load("claude")
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}
	store.SetSecret(secrets.CategoryGlobal, "", "MY_VAR", "topsecretvalue")
	if err := store.Save("claude"); err != nil {
		t.Fatalf("secrets.Save: %v", err)
	}

	after, _ := m.Update(runeKey('s'))
	m = after.(model)

	if m.view != viewSecrets {
		t.Fatalf("'s' on the detail view should open the secrets panel, view=%v", m.view)
	}
	if m.secretsVM != "claude" {
		t.Fatalf("secretsVM = %q, want claude", m.secretsVM)
	}
	if len(m.secretsEntries) != 1 {
		t.Fatalf("expected 1 loaded secret entry, got %d", len(m.secretsEntries))
	}
	view := m.secretsView()
	if strings.Contains(view, "topsecretvalue") {
		t.Fatalf("secrets panel view leaked a cleartext value:\n%s", view)
	}
	if !strings.Contains(view, "****") {
		t.Fatalf("secrets panel view should show a masked entry:\n%s", view)
	}
}

// Adding a global secret through the form persists it to the host store via
// internal/secrets and returns to the (reloaded) list.
func TestAddSecretFormPersistsGlobalSecret(t *testing.T) {
	m := newTestModel(t)
	m.view = viewDetail
	m.detail = vm.VM{Name: "claude", Status: "Stopped"}

	opened, _ := m.Update(runeKey('s'))
	m = opened.(model)

	formOpened, _ := m.Update(runeKey('a'))
	m = formOpened.(model)
	if m.view != viewSecretForm {
		t.Fatalf("'a' should open the add-secret form, view=%v", m.view)
	}
	if m.secretRefreshMode {
		t.Fatalf("the plain add form must not be in refresh mode")
	}

	// Default category is global: only name+value are focusable.
	m.secretNameInput.SetValue("MY_VAR")
	m.secretValueInput.SetValue("a-fresh-value")

	submitted, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = submitted.(model)

	if m.secretFormErr != nil {
		t.Fatalf("a valid submit must not surface a form error, got %v", m.secretFormErr)
	}
	if m.view != viewSecrets {
		t.Fatalf("a valid submit should return to the secrets list, view=%v", m.view)
	}

	store, err := secrets.Load("claude")
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}
	if len(store.Global) != 1 || store.Global[0].Name != "MY_VAR" || store.Global[0].Value != "a-fresh-value" {
		t.Fatalf("global secret not persisted as expected, got %+v", store.Global)
	}
}

// The "refresh GitHub token" action must refuse to apply live against a VM
// that isn't running (RenderSecrets — task 5's entry point — never starts a
// VM), surfacing a clear form error rather than launching the progress view.
func TestRefreshTokenRequiresRunningVM(t *testing.T) {
	m := newTestModel(t)
	m.view = viewDetail
	m.detail = vm.VM{Name: "claude", Status: "Stopped"}

	opened, _ := m.Update(runeKey('s'))
	m = opened.(model)

	formOpened, _ := m.Update(runeKey('r'))
	m = formOpened.(model)
	if m.view != viewSecretForm || !m.secretRefreshMode {
		t.Fatalf("'r' should open the refresh-token form, view=%v refreshMode=%v", m.view, m.secretRefreshMode)
	}

	m.secretValueInput.SetValue("ghp_newtoken")
	submitted, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = submitted.(model)

	if m.view != viewSecretForm {
		t.Fatalf("refresh on a stopped VM must not proceed, view=%v", m.view)
	}
	if m.secretFormErr == nil || !strings.Contains(m.secretFormErr.Error(), "must be running") {
		t.Fatalf("expected a running-VM guard error, got %v", m.secretFormErr)
	}
	if m.running {
		t.Fatalf("refresh on a stopped VM must not start the streaming progress view")
	}
}

// On a running VM, the refresh action persists the new token to the host
// store and launches the streaming progress view (which delegates to
// provision.Provisioner.RenderSecrets) rather than reimplementing the apply
// itself.
func TestRefreshTokenAppliesLiveWhenRunning(t *testing.T) {
	m := newTestModel(t)
	m.view = viewDetail
	m.detail = vm.VM{Name: "claude", Status: "Running"}

	opened, _ := m.Update(runeKey('s'))
	m = opened.(model)

	formOpened, _ := m.Update(runeKey('r'))
	m = formOpened.(model)

	m.secretScopeInput.SetValue("github.com/acme")
	m.secretValueInput.SetValue("ghp_newtoken")
	submitted, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = submitted.(model)

	if m.secretFormErr != nil {
		t.Fatalf("a valid refresh submit must not surface a form error, got %v", m.secretFormErr)
	}
	if m.view != viewProgress || !m.running {
		t.Fatalf("a valid refresh should switch to the streaming progress view, view=%v running=%v", m.view, m.running)
	}
	if cmd == nil {
		t.Fatal("a valid refresh should issue the streaming commands")
	}
	if m.progressBack != viewSecrets {
		t.Fatalf("progressBack = %v, want viewSecrets so esc returns to the panel", m.progressBack)
	}

	store, err := secrets.Load("claude")
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}
	if len(store.GitHub) != 1 || store.GitHub[0].Scope != "github.com/acme" || store.GitHub[0].Token != "ghp_newtoken" {
		t.Fatalf("github token not persisted as expected, got %+v", store.GitHub)
	}
}

// Deleting a secret is a two-step confirm: the first delete key arms it (does
// NOT remove anything), the second commits. On a stopped VM the removal is a
// host-store-only write (no live render).
func TestDeleteSecretTwoStepConfirm(t *testing.T) {
	m := newTestModel(t)
	m.view = viewDetail
	m.detail = vm.VM{Name: "claude", Status: "Stopped"}

	store, err := secrets.Load("claude")
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}
	store.SetSecret(secrets.CategoryGlobal, "", "GONE", "v")
	if err := store.Save("claude"); err != nil {
		t.Fatalf("secrets.Save: %v", err)
	}

	opened, _ := m.Update(runeKey('s'))
	m = opened.(model)
	if len(m.secretsEntries) != 1 {
		t.Fatalf("want 1 loaded entry, got %d", len(m.secretsEntries))
	}

	// First 'd' arms the confirm; nothing is deleted yet.
	armed, _ := m.Update(runeKey('d'))
	m = armed.(model)
	if !m.secretDeletePending {
		t.Fatal("first 'd' should arm the delete confirm")
	}
	if st, _ := secrets.Load("claude"); len(st.Global) != 1 {
		t.Fatalf("secret must NOT be deleted on the first 'd', got %+v", st.Global)
	}

	// Second 'd' commits.
	committed, _ := m.Update(runeKey('d'))
	m = committed.(model)
	if m.secretDeletePending {
		t.Fatal("pending should clear after the confirming key")
	}
	if st, _ := secrets.Load("claude"); len(st.Global) != 0 {
		t.Fatalf("secret should be deleted after confirm, got %+v", st.Global)
	}
	if m.view != viewSecrets {
		t.Fatalf("after a stopped-VM delete the panel stays on viewSecrets, got %v", m.view)
	}
}

// An armed delete is cancelled by any non-confirming key.
func TestDeleteSecretCancelledByOtherKey(t *testing.T) {
	m := newTestModel(t)
	m.view = viewDetail
	m.detail = vm.VM{Name: "claude", Status: "Stopped"}

	store, _ := secrets.Load("claude")
	store.SetSecret(secrets.CategoryGlobal, "", "KEEP", "v")
	if err := store.Save("claude"); err != nil {
		t.Fatalf("secrets.Save: %v", err)
	}

	opened, _ := m.Update(runeKey('s'))
	m = opened.(model)
	armed, _ := m.Update(runeKey('d'))
	m = armed.(model)
	if !m.secretDeletePending {
		t.Fatal("'d' should arm the confirm")
	}

	// A non-confirm key (down arrow) cancels the pending delete.
	cancelled, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = cancelled.(model)
	if m.secretDeletePending {
		t.Fatal("a non-confirming key should cancel the pending delete")
	}
	if st, _ := secrets.Load("claude"); len(st.Global) != 1 {
		t.Fatalf("secret must NOT be deleted when the confirm is cancelled, got %+v", st.Global)
	}
}

// Editing pre-fills the form from the highlighted secret (name + current value)
// and, when the key changes, treats it as a rename: the old entry is removed so
// no orphan is left behind.
func TestEditSecretPrefillsAndRenames(t *testing.T) {
	m := newTestModel(t)
	m.view = viewDetail
	m.detail = vm.VM{Name: "claude", Status: "Stopped"}

	store, _ := secrets.Load("claude")
	store.SetSecret(secrets.CategoryGlobal, "", "OLD", "oldval")
	if err := store.Save("claude"); err != nil {
		t.Fatalf("secrets.Save: %v", err)
	}

	opened, _ := m.Update(runeKey('s'))
	m = opened.(model)

	edited, _ := m.Update(runeKey('e'))
	m = edited.(model)
	if m.view != viewSecretForm {
		t.Fatalf("'e' should open the secret form, view=%v", m.view)
	}
	if !m.secretEditing {
		t.Fatal("edit mode flag should be set")
	}
	if m.secretNameInput.Value() != "OLD" {
		t.Fatalf("name should be pre-filled, got %q", m.secretNameInput.Value())
	}
	if m.secretValueInput.Value() != "oldval" {
		t.Fatalf("value should be pre-filled with the current cleartext, got %q", m.secretValueInput.Value())
	}

	// Rename OLD -> NEW and submit.
	m.secretNameInput.SetValue("NEW")
	submitted, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = submitted.(model)

	st, err := secrets.Load("claude")
	if err != nil {
		t.Fatalf("secrets.Load: %v", err)
	}
	if len(st.Global) != 1 {
		t.Fatalf("rename should not leave an orphan, got %+v", st.Global)
	}
	if v, ok := st.Value(secrets.CategoryGlobal, "", "NEW"); !ok || v != "oldval" {
		t.Fatalf("NEW should carry the old value, got %q ok=%v", v, ok)
	}
	if _, ok := st.Value(secrets.CategoryGlobal, "", "OLD"); ok {
		t.Fatal("OLD should be gone after the rename")
	}
}
