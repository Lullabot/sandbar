package ui

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/lullabot/sandbar/internal/secrets"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// secretsEditorChrome is the number of terminal rows secretsView spends on
// everything BUT the textarea: the title, the cleartext warning, the scoping
// tip, the help bar, and the blank line separating each (appStyle's own
// padding is already folded into layoutMode.ContentHeight). secretsEditorSize
// is the single place this is applied — called from both openSecrets and
// applySize — so the editor's width/height can never drift out of sync the
// way the old fixed row-count constant (previously duplicated between this
// file and model.go's WindowSizeMsg handler) could.
const secretsEditorChrome = 12

// secretsEditorSize derives the textarea's width/height from the layout
// mode, in place of a hardcoded terminal-size offset.
func secretsEditorSize(lm layoutMode) (width, height int) {
	h := lm.ContentHeight - secretsEditorChrome
	if h < minBudget {
		h = minBudget
	}
	return lm.ContentWidth, h
}

// openSecrets opens the secrets editor for the named VM, seeding the textarea
// with its current KEY=VALUE pairs (one per line, keys sorted ascending).
// Deliberately callable regardless of the VM's running status — secrets live
// on the host and only reach the guest on its next start, so there is no
// reason to gate editing on it being up.
func (m *model) openSecrets(name string) tea.Cmd {
	ta := textarea.New()
	// This is a plain KEY=VALUE / [scope] env editor, not a code buffer: drop the
	// line-number gutter (the stray "1") and the default "┃ " line prompt, which
	// otherwise paints a full-height vertical bar down every empty row.
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	// Drop the cursor-line highlight. bubbles/v2 gives the focused textarea's
	// current line a solid background (Color 255 on light, 0 on dark — see
	// textarea.DefaultStyles), which lands as an inverse bar across the line the
	// user is actually typing on and makes it the HARDEST line on screen to read.
	// The cursor already marks that line; it does not need a second, louder
	// indicator fighting the text. Blurred is cleared for the same reason — it
	// greys the line's foreground.
	styles := ta.Styles()
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Blurred.CursorLine = lipgloss.NewStyle()
	ta.SetStyles(styles)
	ta.SetValue(renderPairsForEditor(m.sec.GetAll(name)))
	w, h := secretsEditorSize(m.layout)
	ta.SetWidth(w)
	ta.SetHeight(h)
	m.secretsArea = ta
	m.secretsVM = name
	m.secretsErr = nil
	m.view = viewSecrets
	// Focus the STORED textarea, not the local copy: Focus has a pointer receiver,
	// so calling it on `ta` after the value-copy above would leave m.secretsArea
	// blurred — and a blurred textarea silently drops every keystroke, so the user
	// could not type a single character.
	return m.secretsArea.Focus()
}

// scopeHeaderRE matches a section header line: "[" + scope + "]" with no
// other content on the line (surrounding whitespace is trimmed by the caller
// before matching). "[]" is the explicit-global form.
var scopeHeaderRE = regexp.MustCompile(`^\[(.*)\]$`)

// renderPairsForEditor formats scopes (scope -> KEY -> VALUE) as sectioned
// text for the secrets EDITOR textarea: the global scope ("") renders first,
// headerless, then each non-empty scope as a "[scope]" section, scopes and
// keys both sorted ascending for a byte-stable result. This is a different
// serialization from secrets.Render, which emits shell-quoted
// `export KEY='VALUE'` lines for the guest to source; this form is the plain,
// human-editable text that parseSecrets later re-parses. Do not conflate the
// two — they serve different consumers (a human vs. a guest shell) with
// different escaping needs.
func renderPairsForEditor(scopes map[string]map[string]string) string {
	var b strings.Builder
	writeSection := func(pairs map[string]string) {
		keys := make([]string, 0, len(pairs))
		for k := range pairs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(k + "=" + pairs[k] + "\n")
		}
	}

	writeSection(scopes[""])

	others := make([]string, 0, len(scopes))
	for scope := range scopes {
		if scope == "" {
			continue
		}
		others = append(others, scope)
	}
	sort.Strings(others)
	for _, scope := range others {
		b.WriteString("[" + scope + "]\n")
		writeSection(scopes[scope])
	}
	return b.String()
}

// parseSecrets turns the editor buffer into a scope -> KEY -> VALUE map.
// Blank lines and #-comments are ignored. A line matching `^\[(.*)\]$` starts
// a new section: "[]" or the region before any header is the global scope
// (""); anything else is a directory scope, validated with
// secrets.ValidScope. Within a section, a line splits on its FIRST '=', so a
// value may contain '='. The split runs on the TRIMMED line, not the raw
// one — a leading/interior space in the value survives ("A= 1" -> " 1" is
// still significant), but a trailing carriage return does not: a buffer
// pasted with CRLF line endings (Split(text, "\n") leaves a "\r" on the end
// of every line) would otherwise land that "\r" inside every VALUE, and
// Render single-quotes it straight into the guest's secrets.env. The key is
// additionally trimmed on top of that; the VALUE is not, so a trailing space or
// tab the user typed on purpose survives. Any bad line aborts the whole parse —
// a partial save would silently drop a secret the user typed. A duplicate KEY
// is rejected only within the SAME scope; the same key may appear once per
// scope. Kept free of model state so it is trivially testable on its own.
func parseSecrets(text string) (map[string]map[string]string, error) {
	scopes := map[string]map[string]string{}
	scope := ""
	cur := map[string]string{}
	scopes[scope] = cur

	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if m := scopeHeaderRE.FindStringSubmatch(trimmed); m != nil {
			newScope := m[1]
			if !secrets.ValidScope(newScope) {
				return nil, fmt.Errorf("line %d: invalid scope %q", i+1, newScope)
			}
			scope = newScope
			existing, ok := scopes[scope]
			if !ok {
				existing = map[string]string{}
				scopes[scope] = existing
			}
			cur = existing
			continue
		}
		// Cut the line with ONLY its leading indent and its trailing carriage return
		// removed — NOT the TrimSpace'd form. TrimSpace eats trailing spaces and tabs
		// as well as the \r, so a value that legitimately ends in whitespace was
		// silently truncated: the editor showed it, the store recorded it shortened,
		// and the guest exported something the user never typed, with no error to say
		// so. The CRLF problem this guard exists for is a trailing \r and nothing else.
		k, v, ok := strings.Cut(strings.TrimLeft(strings.TrimRight(line, "\r"), " \t"), "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE, got %q", i+1, line)
		}
		k = strings.TrimSpace(k)
		if !secrets.ValidKey(k) {
			return nil, fmt.Errorf("line %d: %q is not a valid environment variable name (use letters, digits, underscore; not starting with a digit)", i+1, k)
		}
		if _, dup := cur[k]; dup {
			return nil, fmt.Errorf("line %d: duplicate key %q in scope %q", i+1, k, scope)
		}
		cur[k] = v
	}
	return scopes, nil
}

// updateSecrets handles keys on the secrets editor. ctrl+s (Save) parses and
// validates the buffer and, on success, persists it via Store.SetAll and
// returns to the detail view; esc discards the buffer and returns to the
// detail view without writing anything. Both must be handled BEFORE any key
// reaches the textarea, which otherwise consumes ctrl+s/esc as ordinary text.
//
// The host store is always the thing that gets written first — it is the
// source of truth "on next start" regardless of the VM's current status. But
// a RUNNING VM already has a guest to push the change into right now, so the
// save additionally batches applySecretsCmd (gated on m.lookupVM's Status, the
// same live snapshot the rest of the detail screen's actions key off of). A
// STOPPED VM has no guest to reach — its value legitimately waits for the
// VM's next start, which is the only case the status line may say so. Unlike
// the fire-and-forget shape this replaced, the apply's result is not
// swallowed: it is routed through the ordinary actionDoneMsg -> status-line
// path (see the actionDoneMsg case in model.go), so a guest that could not be
// reached surfaces as "apply secrets <name> failed: ...", never as success.
func (m model) updateSecrets(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Code == tea.KeyEsc:
		m.view = viewBoard
		m.secretsErr = nil
		return m, nil
	case key.Matches(msg, m.keys.Save):
		scopes, err := parseSecrets(m.secretsArea.Value())
		if err != nil {
			// Stay on the editor; nothing is persisted.
			m.secretsErr = err
			return m, nil
		}
		if err := m.sec.SetAll(m.secretsVM, scopes); err != nil {
			m.secretsErr = err
			return m, nil
		}
		m.secretsErr = nil
		name := m.secretsVM
		m.view = viewBoard
		if v, ok := m.lookupVM(name); ok && v.Status == limaRunning {
			m.logMsg("secrets saved for " + name + " — applying to the running VM…")
			user, liveScopes := m.secretsFor(name)
			return m, m.beginAction(applySecretsCmd(m.cli, name, user, liveScopes))
		}
		m.logMsg("secrets saved for " + name + " — they apply on next start")
		return m, nil
	}
	var cmd tea.Cmd
	m.secretsArea, cmd = m.secretsArea.Update(msg)
	return m, cmd
}

// secretsHelp returns the bindings shown in the secrets editor's help bar.
func (m model) secretsHelp() []key.Binding {
	return []key.Binding{m.keys.Save, m.keys.Back}
}

// secretsView renders the secrets editor: a title naming the VM, the textarea,
// any pending error, and a cleartext-storage warning.
func (m model) secretsView() string {
	cw := m.layout.ContentWidth
	var b strings.Builder
	b.WriteString(titleStyle.Render("Secrets: " + m.secretsVM))
	b.WriteString("\n\n")
	b.WriteString(m.secretsArea.View())
	b.WriteString("\n")

	if m.secretsErr != nil {
		b.WriteString("\n" + errStyle.Width(cw).Render("Error: "+m.secretsErr.Error()) + "\n")
	}

	// Both blurbs are Width-wrapped to the terminal: without it the tip (styled
	// with the fixed-width labelStyle) collapsed into an ~18-column ribbon and
	// the warning ran off the right edge.
	b.WriteString("\n" + warnStyle.Width(cw).Render(
		"Values are shown in cleartext and stored unencrypted on this host (0600). "+
			"Saving applies them to a RUNNING VM immediately; a stopped one gets them on its next start.") + "\n")

	b.WriteString("\n" + hintStyle.Width(cw).Render(
		"Tip: name a GitHub token GH_TOKEN and put it under an [org dir] section "+
			"(e.g. [github.com/acme]) to scope git auth to that subtree.") + "\n")

	b.WriteString("\n" + m.help.ShortHelpView(m.secretsHelp()))
	return appStyle.Render(b.String())
}
