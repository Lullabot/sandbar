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
	Search     key.Binding
	StopAll    key.Binding
	Profiles   key.Binding
	Back       key.Binding
	Quit       key.Binding
	Help       key.Binding
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
		// The ONLY footer that shows this binding is the search bar, where enter keeps
		// the filter. Its help used to read "detail", for the VM screen — which no
		// longer exists — so the search footer advertised a screen that could not be
		// opened. The board's own enter (on the empty slot) is advertised separately as
		// ghostEnter, because there it means something else entirely.
		Enter: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "keep filter")),
		New:   key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
		// There is no managed-only TOGGLE any more: the board shows sand's managed
		// clones and nothing else, unconditionally (see board.go). 'f' is gone with
		// it — a filter you can turn off is a filter that can show a base image, and
		// a base image is not a workspace.
		Search:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
		StopAll: key.NewBinding(key.WithKeys("X"), key.WithHelp("X", "stop all")),
		// Profiles opens the connection-profiles management screen
		// (profilesview.go) from the board. Deliberately NOT added to
		// boardHelp's footer bindings: every existing board golden pins that
		// footer's exact text, and the profiles screen was added without
		// altering the board's own rendered output. It is still reachable
		// (this binding fires from updateBoard) and documented on the `?`
		// screen (help.go's boardKeys) — just not advertised in the footer.
		Profiles: key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "profiles")),
		Back:     key.NewBinding(key.WithKeys("esc", "backspace"), key.WithHelp("esc", "back")),
		Quit:     key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "keys")),
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
