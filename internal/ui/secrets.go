package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lullabot/sandbar/internal/secrets"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

// openSecrets opens the secrets editor for the named VM, seeding the textarea
// with its current KEY=VALUE pairs (one per line, keys sorted ascending).
// Deliberately callable regardless of the VM's running status — secrets live
// on the host and only reach the guest on its next start, so there is no
// reason to gate editing on it being up.
func (m *model) openSecrets(name string) tea.Cmd {
	ta := textarea.New()
	ta.SetValue(renderPairsForEditor(m.sec.Get(name)))
	ta.SetWidth(max(20, m.width-8))
	ta.SetHeight(max(5, m.height-14))
	m.secretsArea = ta
	m.secretsVM = name
	m.secretsErr = nil
	m.view = viewSecrets
	return ta.Focus()
}

// renderPairsForEditor formats pairs as "KEY=VALUE\n" lines, keys sorted
// ascending, for the secrets EDITOR textarea. This is a different
// serialization from secrets.Render, which emits shell-quoted
// `export KEY='VALUE'` lines for the guest to source; this form is the plain,
// human-editable text that parseSecrets later re-parses. Do not conflate the
// two — they serve different consumers (a human vs. a guest shell) with
// different escaping needs.
func renderPairsForEditor(pairs map[string]string) string {
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + pairs[k] + "\n")
	}
	return b.String()
}

// parseSecrets turns the editor buffer into pairs. Blank lines and #-comments
// are ignored. A line splits on its FIRST '=', so a value may contain '='.
// The key is trimmed; the value is not, since a trailing space can be
// significant. Any bad line aborts the whole parse — a partial save would
// silently drop a secret the user typed. Kept free of model state so it is
// trivially testable on its own.
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

// updateSecrets handles keys on the secrets editor. ctrl+s (Save) parses and
// validates the buffer and, on success, persists it via Store.Set and returns
// to the detail view; esc discards the buffer and returns to the detail view
// without writing anything. Both must be handled BEFORE any key reaches the
// textarea, which otherwise consumes ctrl+s/esc as ordinary text.
func (m model) updateSecrets(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyEsc:
		m.view = viewDetail
		m.secretsErr = nil
		return m, nil
	case key.Matches(msg, m.keys.Save):
		pairs, err := parseSecrets(m.secretsArea.Value())
		if err != nil {
			// Stay on the editor; nothing is persisted.
			m.secretsErr = err
			return m, nil
		}
		if err := m.sec.Set(m.secretsVM, pairs); err != nil {
			m.secretsErr = err
			return m, nil
		}
		m.secretsErr = nil
		m.status = "secrets saved for " + m.secretsVM + " — they apply on next start"
		m.view = viewDetail
		return m, nil
	}
	var cmd tea.Cmd
	m.secretsArea, cmd = m.secretsArea.Update(msg)
	return m, cmd
}

// secretsView renders the secrets editor: a title naming the VM, the textarea,
// any pending error, and a cleartext-storage warning.
func (m model) secretsView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Secrets: " + m.secretsVM))
	b.WriteString("\n\n")
	b.WriteString(m.secretsArea.View())
	b.WriteString("\n")

	if m.secretsErr != nil {
		b.WriteString("\n" + errStyle.Render("Error: "+m.secretsErr.Error()) + "\n")
	}

	b.WriteString("\n" + warnStyle.Render(
		"Values are shown in cleartext and stored unencrypted on this host (0600).\n"+
			"They are written into the VM on its next start.") + "\n")

	b.WriteString("\n" + m.help.ShortHelpView(m.viewHelp()))
	return appStyle.Render(b.String())
}
