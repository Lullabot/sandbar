// This file implements the secrets panel (issue #3): a masked list of the
// selected VM's host-stored secrets, an add/edit form, and a dedicated
// "refresh GitHub token" action that persists the new token via
// internal/secrets and applies it live through provision.Provisioner's
// RenderSecrets (task 5's render-into-running-VM entry point). Every render
// path here goes through secrets.RedactedEntry — which structurally carries
// no cleartext field — so this panel cannot echo a secret's value back, and
// value inputs are entered with textinput.EchoPassword so they are masked as
// typed. Nothing here ever logs a cleartext value.
package ui

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/lullabot/sandbar/internal/registry"
	"github.com/lullabot/sandbar/internal/secrets"
	"github.com/lullabot/sandbar/internal/vm"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// Secret-form field slots. Which of these are shown/focusable depends on the
// active category (and secretRefreshMode) — see secretFormFields.
const (
	sfCategory = iota
	sfScope
	sfName
	sfValue
)

// secretCategoryOrder is the cycle order the category selector steps through
// with left/right in the (non-refresh) add/edit form.
var secretCategoryOrder = []secrets.Category{
	secrets.CategoryGlobal,
	secrets.CategoryDirEnv,
	secrets.CategoryGitHub,
}

// githubEffectNote mirrors `sand secret sync`'s honest effect summary for a
// GitHub/git secret (cmd/sand/secret.go's effectSummaryLines): the file-backed
// git credential store takes effect on the very next git/gh call, unlike an
// env-var secret which needs a new shell. Duplicated here (rather than
// imported — it is unexported in package main) so the TUI's refresh action
// surfaces the exact same honest claim the CLI does.
const githubEffectNote = "GitHub/git secrets updated — effective immediately (next git/gh call)."

// envEffectNote is the honest counterpart for an environment-variable secret:
// rendering it into the VM does not change any already-running process's
// environment, so the user must reconnect active sessions (a new shell) to see
// it. Mirrors cmd/sand/secret.go's effectSummaryLines env-var line.
const envEffectNote = "Environment-variable secret applied — take effect in NEW shells only; reconnect active sessions (reopen 'limactl shell', restart tools like claude) to pick it up."

// secretLine renders one row of the secrets panel list from a redacted
// entry — the panel's ONLY path to displaying a secret, mirroring
// cmd/sand/secret.go's formatSecretLine. Because e carries only a masked
// value (never cleartext), this function cannot leak a secret by
// construction.
func secretLine(e secrets.RedactedEntry) string {
	switch e.Category {
	case secrets.CategoryGlobal:
		return fmt.Sprintf("[global]  %s = %s", e.Name, e.Masked)
	case secrets.CategoryGitHub:
		scope := e.Scope
		if scope == "" {
			scope = "(default)"
		}
		return fmt.Sprintf("[github]  %s = %s", scope, e.Masked)
	case secrets.CategoryDirEnv:
		return fmt.Sprintf("[dir_env] %s:%s = %s", e.Scope, e.Name, e.Masked)
	default:
		return fmt.Sprintf("[%s] %s:%s = %s", e.Category, e.Scope, e.Name, e.Masked)
	}
}

// categoryLabel renders a Category for the form's category selector.
func categoryLabel(cat secrets.Category) string {
	switch cat {
	case secrets.CategoryGlobal:
		return "global"
	case secrets.CategoryDirEnv:
		return "dir_env"
	case secrets.CategoryGitHub:
		return "github"
	default:
		return string(cat)
	}
}

// nextCategory/prevCategory cycle the category selector through
// secretCategoryOrder, wrapping around.
func nextCategory(cat secrets.Category) secrets.Category {
	for i, c := range secretCategoryOrder {
		if c == cat {
			return secretCategoryOrder[(i+1)%len(secretCategoryOrder)]
		}
	}
	return secretCategoryOrder[0]
}

func prevCategory(cat secrets.Category) secrets.Category {
	for i, c := range secretCategoryOrder {
		if c == cat {
			return secretCategoryOrder[(i-1+len(secretCategoryOrder))%len(secretCategoryOrder)]
		}
	}
	return secretCategoryOrder[0]
}

// secretFormFields returns the ordered, applicable field slots for the
// active category and mode: refresh mode narrows straight to scope+value
// (category is locked to github, no name), otherwise the set follows the
// same category routing as `sand secret set` (global: name only; dir_env:
// scope+name; github: scope only, no name).
func secretFormFields(cat secrets.Category, refresh bool) []int {
	if refresh {
		return []int{sfScope, sfValue}
	}
	switch cat {
	case secrets.CategoryDirEnv:
		return []int{sfCategory, sfScope, sfName, sfValue}
	case secrets.CategoryGitHub:
		return []int{sfCategory, sfScope, sfValue}
	default: // CategoryGlobal
		return []int{sfCategory, sfName, sfValue}
	}
}

func containsField(fields []int, f int) bool {
	for _, x := range fields {
		if x == f {
			return true
		}
	}
	return false
}

// secretsSyncConfig resolves the vm.CreateConfig RenderSecrets needs for
// name: the VM's recorded managed-registry config when one exists (so
// cfg.User matches the VM's actual identity, required by the secrets role's
// getent lookup), falling back to sand's own defaults otherwise. Mirrors
// cmd/sand/secret.go's syncConfig for the same RenderSecrets entry point.
func secretsSyncConfig(reg *registry.Registry, name string) vm.CreateConfig {
	cfg := vm.DefaultCreateConfig()
	cfg.Name = name
	cfg.User = hostUser()
	if reg != nil {
		if stored, ok := reg.Config(name); ok {
			cfg = stored
		}
	}
	cfg.Name = name
	return cfg
}

// openSecretsPanel opens the secrets panel for the currently viewed detail
// VM, loading its masked list.
func (m *model) openSecretsPanel() tea.Cmd {
	m.secretsVM = m.detail.Name
	m.secretsStatus = ""
	m.secretsCursor = 0
	m.loadSecretsList()
	m.view = viewSecrets
	return nil
}

// loadSecretsList (re)loads the redacted secrets list for m.secretsVM.
func (m *model) loadSecretsList() {
	store, err := secrets.Load(m.secretsVM)
	if err != nil {
		m.secretsErr = err
		m.secretsEntries = nil
		return
	}
	m.secretsErr = nil
	m.secretsEntries = store.Redacted()
	if m.secretsCursor >= len(m.secretsEntries) {
		m.secretsCursor = len(m.secretsEntries) - 1
	}
	if m.secretsCursor < 0 {
		m.secretsCursor = 0
	}
}

// updateSecrets handles keys on the secrets panel.
func (m model) updateSecrets(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// An armed delete is resolved first: the delete/confirm key commits it,
	// anything else disarms it (and then falls through to normal handling), so
	// an accidental keystroke never destroys a secret.
	if m.secretDeletePending {
		if key.Matches(msg, m.keys.Delete) || key.Matches(msg, m.keys.Confirm) {
			return m.deleteSelectedSecret()
		}
		m.secretDeletePending = false
		m.secretsStatus = ""
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Back):
		m.view = viewDetail
		return m, nil
	case key.Matches(msg, m.keys.AddSecret):
		return m, m.openSecretForm(false)
	case key.Matches(msg, m.keys.EditSecret):
		return m, m.openEditSecretForm()
	case key.Matches(msg, m.keys.Delete):
		if len(m.secretsEntries) == 0 {
			return m, nil
		}
		m.secretDeletePending = true
		m.secretsStatus = "delete " + secretLine(m.secretsEntries[m.secretsCursor]) + "?  press d again to confirm, any other key cancels"
		return m, nil
	case key.Matches(msg, m.keys.RefreshToken):
		return m, m.openSecretForm(true)
	case key.Matches(msg, m.keys.Up):
		if m.secretsCursor > 0 {
			m.secretsCursor--
		}
		return m, nil
	case key.Matches(msg, m.keys.Down):
		if m.secretsCursor < len(m.secretsEntries)-1 {
			m.secretsCursor++
		}
		return m, nil
	}
	return m, nil
}

// openEditSecretForm opens the add/edit form pre-filled from the highlighted
// secret (its current value included, masked on screen) so the value — or the
// scope/name — can be changed. The original key is recorded so that changing
// the key on submit is treated as a rename (the old entry is removed) rather
// than leaving an orphan behind.
func (m *model) openEditSecretForm() tea.Cmd {
	if len(m.secretsEntries) == 0 {
		m.secretsStatus = "no secret to edit"
		return nil
	}
	e := m.secretsEntries[m.secretsCursor]

	value := ""
	if store, err := secrets.Load(m.secretsVM); err == nil {
		if v, ok := store.Value(e.Category, e.Scope, e.Name); ok {
			value = v
		}
	}

	m.openSecretForm(false)
	m.secretEditing = true
	m.secretEditOrigCat = e.Category
	m.secretEditOrigScope = e.Scope
	m.secretEditOrigName = e.Name
	m.secretCategory = e.Category
	m.secretScopeInput.SetValue(e.Scope)
	m.secretNameInput.SetValue(e.Name)
	m.secretValueInput.SetValue(value)

	// Start focus on the value field — the common edit — if the category has
	// one in its field set.
	fields := secretFormFields(m.secretCategory, false)
	for i, f := range fields {
		if f == sfValue {
			m.secretFieldFocus = i
			return m.secretFocusField(sfValue)
		}
	}
	m.secretFieldFocus = 0
	return m.secretFocusField(fields[0])
}

// deleteSelectedSecret removes the highlighted secret from the host store and,
// when the VM is running, re-renders so it disappears from the VM too (the
// secrets role prunes the corresponding file/credential); otherwise it drops
// on the next create/start/sync. The list is reloaded before any render so the
// panel reflects the removal immediately on return.
func (m model) deleteSelectedSecret() (tea.Model, tea.Cmd) {
	m.secretDeletePending = false
	if len(m.secretsEntries) == 0 {
		return m, nil
	}
	e := m.secretsEntries[m.secretsCursor]

	store, err := secrets.Load(m.secretsVM)
	if err != nil {
		m.secretsErr = err
		return m, nil
	}
	if !store.RemoveSecret(e.Category, e.Scope, e.Name) {
		m.secretsStatus = "secret not found"
		return m, nil
	}
	if err := store.Save(m.secretsVM); err != nil {
		m.secretsErr = err
		return m, nil
	}
	m.loadSecretsList()

	if m.detail.Status == "Running" {
		vmName := m.secretsVM
		cfg := secretsSyncConfig(m.reg, vmName)
		prov := m.prov
		run := func(ctx context.Context, out io.Writer) error {
			return prov.RenderSecrets(ctx, vmName, cfg, out)
		}
		return m, m.beginStream("Removing secret from "+vmName, viewSecrets, run)
	}

	m.secretsStatus = "deleted from host store — drops from the VM on next start, or run 'sand secret sync'"
	return m, nil
}

// secretsView renders the masked secrets list.
func (m model) secretsView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Secrets: " + m.secretsVM))
	b.WriteString("\n\n")

	switch {
	case m.secretsErr != nil:
		b.WriteString(errStyle.Render("Error: "+m.secretsErr.Error()) + "\n")
	case len(m.secretsEntries) == 0:
		b.WriteString(statusStyle.Render("no secrets stored") + "\n")
	default:
		for i, e := range m.secretsEntries {
			line := secretLine(e)
			if i == m.secretsCursor {
				b.WriteString(focusedLabelStyle.Render("> "+line) + "\n")
			} else {
				b.WriteString("  " + line + "\n")
			}
		}
	}

	if m.secretsStatus != "" {
		b.WriteString("\n" + statusStyle.Render(m.secretsStatus) + "\n")
	}

	b.WriteString("\n" + m.help.ShortHelpView(m.viewHelp()))
	return appStyle.Render(b.String())
}

// openSecretForm initialises the add/edit form. refresh=true is the
// dedicated "refresh GitHub token" action: it locks the category to github
// and starts focus on the scope field (no category cycling, no name field).
func (m *model) openSecretForm(refresh bool) tea.Cmd {
	m.secretRefreshMode = refresh
	m.secretEditing = false
	m.secretFormErr = nil
	m.secretCategory = secrets.CategoryGlobal
	if refresh {
		m.secretCategory = secrets.CategoryGitHub
	}

	scope := textinput.New()
	scope.CharLimit = 256
	scope.Width = 40
	scope.Placeholder = "e.g. github.com/acme (blank = default)"

	name := textinput.New()
	name.CharLimit = 128
	name.Width = 40
	name.Placeholder = "VAR_NAME"

	value := textinput.New()
	value.CharLimit = 512
	value.Width = 40
	value.EchoMode = textinput.EchoPassword // secret values are always masked as typed

	m.secretScopeInput = scope
	m.secretNameInput = name
	m.secretValueInput = value
	m.secretFieldFocus = 0
	m.view = viewSecretForm

	fields := secretFormFields(m.secretCategory, m.secretRefreshMode)
	return m.secretFocusField(fields[0])
}

// secretBlurAll blurs every text input in the secret form; called before
// moving focus so only the newly focused field shows a cursor.
func (m *model) secretBlurAll() {
	m.secretScopeInput.Blur()
	m.secretNameInput.Blur()
	m.secretValueInput.Blur()
}

// secretFocusField focuses the text input for field slot f (sfCategory has
// no input of its own, so it's a no-op — category is changed with left/right).
func (m *model) secretFocusField(f int) tea.Cmd {
	m.secretBlurAll()
	switch f {
	case sfScope:
		return m.secretScopeInput.Focus()
	case sfName:
		return m.secretNameInput.Focus()
	case sfValue:
		return m.secretValueInput.Focus()
	}
	return nil
}

// secretFocusNext/secretFocusPrev move focus among the fields applicable to
// the current category/mode, wrapping around.
func (m *model) secretFocusNext() tea.Cmd {
	fields := secretFormFields(m.secretCategory, m.secretRefreshMode)
	m.secretFieldFocus = (m.secretFieldFocus + 1) % len(fields)
	return m.secretFocusField(fields[m.secretFieldFocus])
}

func (m *model) secretFocusPrev() tea.Cmd {
	fields := secretFormFields(m.secretCategory, m.secretRefreshMode)
	m.secretFieldFocus = (m.secretFieldFocus - 1 + len(fields)) % len(fields)
	return m.secretFocusField(fields[m.secretFieldFocus])
}

// updateSecretForm handles keys on the add/edit/refresh secret form.
func (m model) updateSecretForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyEsc:
		m.view = viewSecrets
		return m, nil
	case key.Matches(msg, m.keys.Submit):
		return m.submitSecretForm()
	}

	fields := secretFormFields(m.secretCategory, m.secretRefreshMode)
	cur := fields[m.secretFieldFocus]

	// The category selector (add/edit mode only) cycles with left/right
	// instead of taking text input.
	if cur == sfCategory && !m.secretRefreshMode {
		switch msg.String() {
		case "left":
			m.secretCategory = prevCategory(m.secretCategory)
			m.secretFieldFocus = 0
			m.secretBlurAll()
			return m, nil
		case "right":
			m.secretCategory = nextCategory(m.secretCategory)
			m.secretFieldFocus = 0
			m.secretBlurAll()
			return m, nil
		}
	}

	switch {
	case key.Matches(msg, m.keys.ShiftTab), key.Matches(msg, m.keys.Up):
		return m, m.secretFocusPrev()
	case key.Matches(msg, m.keys.Down), key.Matches(msg, m.keys.Tab):
		return m, m.secretFocusNext()
	}

	switch cur {
	case sfScope:
		var cmd tea.Cmd
		m.secretScopeInput, cmd = m.secretScopeInput.Update(msg)
		return m, cmd
	case sfName:
		var cmd tea.Cmd
		m.secretNameInput, cmd = m.secretNameInput.Update(msg)
		return m, cmd
	case sfValue:
		var cmd tea.Cmd
		m.secretValueInput, cmd = m.secretValueInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

// submitSecretForm validates and persists the form. The refresh-mode branch
// additionally applies the token live via RenderSecrets; the ordinary
// add/edit branch is a plain local store write (no VM interaction), so it
// returns straight to the (reloaded) secrets list.
func (m model) submitSecretForm() (tea.Model, tea.Cmd) {
	scope := strings.TrimSpace(m.secretScopeInput.Value())
	name := strings.TrimSpace(m.secretNameInput.Value())
	value := m.secretValueInput.Value()

	cat := m.secretCategory
	if m.secretRefreshMode {
		cat = secrets.CategoryGitHub
	}

	if cat != secrets.CategoryGitHub && name == "" {
		m.secretFormErr = fmt.Errorf("name is required for a %s secret", categoryLabel(cat))
		return m, nil
	}
	if value == "" {
		m.secretFormErr = fmt.Errorf("value is required")
		return m, nil
	}

	if m.secretRefreshMode {
		return m.submitRefreshToken(scope, value)
	}

	store, err := secrets.Load(m.secretsVM)
	if err != nil {
		m.secretFormErr = err
		return m, nil
	}
	// Editing with a changed key is a rename: drop the original entry so the
	// old one doesn't linger alongside the renamed one.
	if m.secretEditing && (m.secretEditOrigCat != cat || m.secretEditOrigScope != scope || m.secretEditOrigName != name) {
		store.RemoveSecret(m.secretEditOrigCat, m.secretEditOrigScope, m.secretEditOrigName)
	}
	store.SetSecret(cat, scope, name, value)
	if err := store.Save(m.secretsVM); err != nil {
		m.secretFormErr = err
		return m, nil
	}
	m.secretFormErr = nil
	m.secretEditing = false
	m.loadSecretsList()

	// Apply live when the VM is running so the secret is usable without a
	// separate `sand secret sync`, mirroring the CLI's `set`. When it isn't
	// running, the value stays on the host and renders on the next
	// create/start; just confirm the save.
	if m.detail.Status == "Running" {
		vmName := m.secretsVM
		cfg := secretsSyncConfig(m.reg, vmName)
		prov := m.prov
		note := envEffectNote
		if cat == secrets.CategoryGitHub {
			note = githubEffectNote
		}
		run := func(ctx context.Context, out io.Writer) error {
			if err := prov.RenderSecrets(ctx, vmName, cfg, out); err != nil {
				return err
			}
			fmt.Fprintln(out, note)
			return nil
		}
		return m, m.beginStream("Applying secret to "+vmName, viewSecrets, run)
	}

	m.secretsStatus = "saved to host store — applies on next start, or run 'sand secret sync' when running"
	m.view = viewSecrets
	return m, nil
}

// submitRefreshToken implements the dedicated "refresh GitHub token" action:
// it requires the target VM to already be running (RenderSecrets — task 5's
// entry point — targets an already-running VM and never starts one, so we
// refuse clearly rather than surfacing a confusing failure deep inside the
// shell call, mirroring `sand secret sync`'s guard), persists the new token
// via internal/secrets, then delegates the live apply to
// provision.Provisioner.RenderSecrets through the panel's shared streaming
// progress view — never reimplementing the render itself.
func (m model) submitRefreshToken(scope, token string) (tea.Model, tea.Cmd) {
	if m.detail.Status != "Running" {
		m.secretFormErr = fmt.Errorf("%q must be running to sync the refreshed token live (press s on the VM to start it)", m.secretsVM)
		return m, nil
	}

	store, err := secrets.Load(m.secretsVM)
	if err != nil {
		m.secretFormErr = err
		return m, nil
	}
	store.SetSecret(secrets.CategoryGitHub, scope, "", token)
	if err := store.Save(m.secretsVM); err != nil {
		m.secretFormErr = err
		return m, nil
	}

	name := m.secretsVM
	cfg := secretsSyncConfig(m.reg, name)
	prov := m.prov
	run := func(ctx context.Context, out io.Writer) error {
		if err := prov.RenderSecrets(ctx, name, cfg, out); err != nil {
			return err
		}
		fmt.Fprintln(out, githubEffectNote)
		return nil
	}

	m.secretFormErr = nil
	cmd := m.beginStream("Refreshing GitHub token for "+name, viewSecrets, run)
	return m, cmd
}

// secretFormView renders the add/edit/refresh form.
func (m model) secretFormView() string {
	var b strings.Builder
	title := "Add / edit secret"
	if m.secretRefreshMode {
		title = "Refresh GitHub token"
	}
	b.WriteString(titleStyle.Render(title + " — " + m.secretsVM))
	b.WriteString("\n\n")

	fields := secretFormFields(m.secretCategory, m.secretRefreshMode)
	cur := fields[m.secretFieldFocus]

	if m.secretRefreshMode {
		b.WriteString(labelStyle.Render("Category:") + " github (locked)\n")
	} else {
		ls := labelStyle
		if cur == sfCategory {
			ls = focusedLabelStyle
		}
		b.WriteString(ls.Render("Category:") + " " + categoryLabel(m.secretCategory) + "  (←/→ to change)\n")
	}

	if containsField(fields, sfScope) {
		ls := labelStyle
		if cur == sfScope {
			ls = focusedLabelStyle
		}
		label := "Scope (dir)"
		if cat := m.secretCategory; m.secretRefreshMode || cat == secrets.CategoryGitHub {
			label = "Scope (blank = default token)"
		}
		b.WriteString(ls.Render(label+":") + " " + m.secretScopeInput.View() + "\n")
	}

	if containsField(fields, sfName) {
		ls := labelStyle
		if cur == sfName {
			ls = focusedLabelStyle
		}
		b.WriteString(ls.Render("Name:") + " " + m.secretNameInput.View() + "\n")
	}

	ls := labelStyle
	if cur == sfValue {
		ls = focusedLabelStyle
	}
	valueLabel := "Value"
	if m.secretRefreshMode {
		valueLabel = "New token"
	}
	b.WriteString(ls.Render(valueLabel+":") + " " + m.secretValueInput.View() + "\n")

	if m.secretFormErr != nil {
		b.WriteString("\n" + errStyle.Render("Error: "+m.secretFormErr.Error()) + "\n")
	}
	if m.secretRefreshMode {
		b.WriteString("\n" + fieldInfoStyle.Render(githubEffectNote) + "\n")
	}

	b.WriteString("\n" + m.help.ShortHelpView(m.viewHelp()))
	return appStyle.Render(b.String())
}
