package ui

import "github.com/charmbracelet/bubbles/key"

// keyMap holds every keybinding the TUI reacts to. Bindings are reused across
// views (e.g. the same Enter/Quit) and surfaced in the help bar via viewHelp.
type keyMap struct {
	Enter     key.Binding
	New       key.Binding
	Start     key.Binding
	Stop      key.Binding
	Restart   key.Binding
	Delete    key.Binding
	Filter    key.Binding
	Search    key.Binding
	Shell     key.Binding
	Upload    key.Binding
	Download  key.Binding
	Back      key.Binding
	Quit      key.Binding
	Tab       key.Binding
	ShiftTab  key.Binding
	Up        key.Binding
	Down      key.Binding
	Submit    key.Binding
	Confirm   key.Binding
	Recreate  key.Binding
	Cancel    key.Binding
	Interrupt key.Binding

	// Secrets panel (opened from the detail view) and its add/edit/refresh form.
	Secrets      key.Binding
	AddSecret    key.Binding
	EditSecret   key.Binding
	RefreshToken key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		Enter:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "detail")),
		New:     key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
		Start:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "start")),
		Stop:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "stop")),
		Restart: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restart")),
		Delete:  key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
		Filter:  key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "filter managed")),
		Search:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
		Shell:   key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "shell")),
		// Upload/Download live only on the detail view. 'd' is free there (delete
		// lives on the list's confirm overlay, not the detail view).
		Upload:   key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "upload")),
		Download: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "download")),
		Back:     key.NewBinding(key.WithKeys("esc", "backspace"), key.WithHelp("esc", "back")),
		Quit:     key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Tab:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
		ShiftTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev field")),
		Up:       key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "prev field")),
		Down:     key.NewBinding(key.WithKeys("down", "enter"), key.WithHelp("↓/enter", "next field")),
		Submit:   key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "create")),
		Confirm:  key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "delete")),
		Recreate: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "recreate")),
		Cancel:   key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n", "cancel")),
		// ctrl+c is intercepted in Update (not matched here); this binding is for
		// the progress-view help bar while a build is running.
		Interrupt: key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "cancel")),
		// Secrets lives only on the detail view. 's' is free there (Start lives
		// on the list, same reuse pattern as Delete/Download both on 'd').
		Secrets: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "secrets")),
		// AddSecret/EditSecret/RefreshToken live only on the secrets panel;
		// Delete ('d') is reused there to delete the highlighted secret.
		AddSecret:    key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add secret")),
		EditSecret:   key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		RefreshToken: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh gh token")),
	}
}

// viewHelp returns the bindings shown in the help bar for the active view.
func (m model) viewHelp() []key.Binding {
	switch m.view {
	case viewForm:
		// 'q' is a text character in the form, so Quit is intentionally omitted
		// (only ctrl+c quits). Up/Down/enter move between fields; ctrl+s creates.
		return []key.Binding{m.keys.Up, m.keys.Down, m.keys.Submit, m.keys.Back}
	case viewDetail:
		return []key.Binding{m.keys.Upload, m.keys.Download, m.keys.Secrets, m.keys.Back, m.keys.Quit}
	case viewBrowse:
		// The browser draws its own enter/select/filter hint line; esc backs out.
		return []key.Binding{m.keys.Back}
	case viewDest:
		return []key.Binding{m.keys.Submit, m.keys.Back}
	case viewSecrets:
		return []key.Binding{m.keys.AddSecret, m.keys.EditSecret, m.keys.Delete, m.keys.RefreshToken, m.keys.Up, m.keys.Down, m.keys.Back, m.keys.Quit}
	case viewSecretForm:
		return []key.Binding{m.keys.Up, m.keys.Down, m.keys.Submit, m.keys.Back}
	case viewProgress:
		// While a build runs, ctrl+c cancels it; q/esc are inert until it finishes.
		if m.running {
			return []key.Binding{m.keys.Interrupt}
		}
		return []key.Binding{m.keys.Back, m.keys.Quit}
	default: // viewList
		if m.confirming {
			// Recreate is only shown (and accepted) for managed VMs.
			if m.confirmBase != "" {
				return []key.Binding{m.keys.Confirm, m.keys.Recreate, m.keys.Cancel}
			}
			return []key.Binding{m.keys.Confirm, m.keys.Cancel}
		}
		if m.searching {
			// esc clears/exits, enter commits the filter.
			return []key.Binding{m.keys.Back, m.keys.Enter}
		}
		return []key.Binding{
			m.keys.Enter, m.keys.Shell, m.keys.New, m.keys.Start, m.keys.Stop,
			m.keys.Restart, m.keys.Delete, m.keys.Filter, m.keys.Search, m.keys.Quit,
		}
	}
}
