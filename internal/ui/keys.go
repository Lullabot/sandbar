package ui

import "charm.land/bubbles/v2/key"

// keyMap holds the chrome/navigation bindings shared across screens (quit,
// back, form navigation, confirm/cancel, …). The VM screen's per-VM verbs
// (start/stop/restart/reset/shell/delete/upload/download/secrets) live in
// their own registry — see commandreg.go — rather than as fields here, so
// there is exactly one place that defines each of those keys, its help text,
// and when it applies.
type keyMap struct {
	Enter      key.Binding
	New        key.Binding
	Filter     key.Binding
	Search     key.Binding
	StopAll    key.Binding
	Back       key.Binding
	Quit       key.Binding
	Tab        key.Binding
	ShiftTab   key.Binding
	Up         key.Binding
	Down       key.Binding
	Submit     key.Binding
	Save       key.Binding
	Confirm    key.Binding
	Cancel     key.Binding
	Interrupt  key.Binding
	Background key.Binding
}

// newKeyMap builds the shared chrome/navigation keyMap.
func newKeyMap() keyMap {
	return keyMap{
		Enter:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "detail")),
		New:      key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
		Filter:   key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "filter managed")),
		Search:   key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
		StopAll:  key.NewBinding(key.WithKeys("X"), key.WithHelp("X", "stop all")),
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
		// Background is Back's spelling on a RUNNING progress screen. Same key, and
		// deliberately a separate binding: leaving a build no longer abandons or
		// cancels it (the job runs on in the registry), and the help bar has to say
		// so — otherwise the one affordance this whole task exists to create is
		// invisible, and users keep sitting through builds they could walk away from.
		Background: key.NewBinding(key.WithKeys("esc", "backspace"), key.WithHelp("esc", "back (keeps building)")),
	}
}
