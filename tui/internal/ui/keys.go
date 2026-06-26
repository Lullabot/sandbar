package ui

import "github.com/charmbracelet/bubbles/key"

// keyMap holds every keybinding the TUI reacts to. Bindings are reused across
// views (e.g. the same Enter/Quit) and surfaced in the help bar via viewHelp.
type keyMap struct {
	Enter    key.Binding
	New      key.Binding
	Start    key.Binding
	Stop     key.Binding
	Restart  key.Binding
	Delete   key.Binding
	Back     key.Binding
	Quit     key.Binding
	Tab      key.Binding
	ShiftTab key.Binding
	Submit   key.Binding
	Confirm  key.Binding
	Recreate key.Binding
	Cancel   key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		Enter:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "detail")),
		New:      key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
		Start:    key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "start")),
		Stop:     key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "stop")),
		Restart:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restart")),
		Delete:   key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
		Back:     key.NewBinding(key.WithKeys("esc", "backspace"), key.WithHelp("esc", "back")),
		Quit:     key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Tab:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
		ShiftTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev field")),
		Submit:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "create")),
		Confirm:  key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "delete")),
		Recreate: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "recreate")),
		Cancel:   key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n", "cancel")),
	}
}

// viewHelp returns the bindings shown in the help bar for the active view.
func (m model) viewHelp() []key.Binding {
	switch m.view {
	case viewForm:
		return []key.Binding{m.keys.Tab, m.keys.ShiftTab, m.keys.Submit, m.keys.Back, m.keys.Quit}
	case viewDetail:
		return []key.Binding{m.keys.Back, m.keys.Quit}
	case viewProgress:
		return []key.Binding{m.keys.Back, m.keys.Quit}
	default: // viewList
		if m.confirming {
			return []key.Binding{m.keys.Confirm, m.keys.Recreate, m.keys.Cancel}
		}
		return []key.Binding{
			m.keys.Enter, m.keys.New, m.keys.Start, m.keys.Stop,
			m.keys.Restart, m.keys.Delete, m.keys.Quit,
		}
	}
}
