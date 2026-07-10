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
	StopAll   key.Binding
	Reset     key.Binding
	Secrets   key.Binding
	Back      key.Binding
	Quit      key.Binding
	Tab       key.Binding
	ShiftTab  key.Binding
	Up        key.Binding
	Down      key.Binding
	Submit    key.Binding
	Save      key.Binding
	Confirm   key.Binding
	Cancel    key.Binding
	Interrupt key.Binding
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
		Upload:  key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "upload")),
		// 'd' stays delete on every screen: it is the most destructive key and its
		// meaning must never change under the user's fingers. Download took the rename.
		Download: key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "download")),
		StopAll:  key.NewBinding(key.WithKeys("X"), key.WithHelp("X", "stop all")),
		Reset:    key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "reset")),
		Secrets:  key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "secrets")),
		Back:     key.NewBinding(key.WithKeys("esc", "backspace"), key.WithHelp("esc", "back")),
		Quit:     key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Tab:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
		ShiftTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev field")),
		Up:       key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "prev field")),
		Down:     key.NewBinding(key.WithKeys("down", "enter"), key.WithHelp("↓/enter", "next field")),
		Submit:   key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "create")),
		// Save is a distinct binding from Submit (same key, different screen) so
		// the secrets editor's help bar reads "save" rather than the form's
		// "create".
		Save:    key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "save")),
		Confirm: key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "confirm")),
		Cancel:  key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n", "cancel")),
		// ctrl+c is intercepted in Update (not matched here); this binding is for
		// the progress-view help bar while a build is running.
		Interrupt: key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "cancel")),
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
		if m.confirm != nil {
			return []key.Binding{m.keys.Confirm, m.keys.Cancel}
		}
		return []key.Binding{
			m.keys.Start, m.keys.Stop, m.keys.Restart, m.keys.Reset, m.keys.Shell,
			m.keys.Delete, m.keys.Upload, m.keys.Download, m.keys.Secrets,
			m.keys.Back, m.keys.Quit,
		}
	case viewBrowse:
		// The browser draws its own enter/select/filter hint line; esc backs out.
		return []key.Binding{m.keys.Back}
	case viewDest:
		return []key.Binding{m.keys.Submit, m.keys.Back}
	case viewSecrets:
		return []key.Binding{m.keys.Save, m.keys.Back}
	case viewProgress:
		// While a build runs, ctrl+c cancels it; q/esc do nothing until it finishes.
		if m.running {
			return []key.Binding{m.keys.Interrupt}
		}
		return []key.Binding{m.keys.Back, m.keys.Quit}
	default: // viewList
		if m.confirm != nil {
			return []key.Binding{m.keys.Confirm, m.keys.Cancel}
		}
		if m.searching {
			// esc clears/exits, enter commits the filter.
			return []key.Binding{m.keys.Back, m.keys.Enter}
		}
		return []key.Binding{
			m.keys.Enter, m.keys.New, m.keys.Filter, m.keys.Search,
			m.keys.StopAll, m.keys.Quit,
		}
	}
}
