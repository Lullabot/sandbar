package ui

import "charm.land/lipgloss/v2"

// Shared lipgloss styles for the TUI. Colours are ANSI 256 indices so the UI
// degrades gracefully on limited terminals.
var (
	appStyle = lipgloss.NewStyle().Padding(1, 2)

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("231")).
			Background(lipgloss.Color("63")).
			Padding(0, 1)

	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))

	labelStyle        = lipgloss.NewStyle().Width(18).Foreground(lipgloss.Color("245"))
	focusedLabelStyle = lipgloss.NewStyle().Width(18).Bold(true).Foreground(lipgloss.Color("63"))

	// hintStyle is dim guidance text that must reflow to the full content width.
	// Unlike labelStyle it carries no fixed Width, so callers set one (via
	// .Width) to wrap it to the terminal rather than a form label's column.
	hintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63")).
			Padding(0, 1)

	// fieldInfoStyle renders the focused field's help under the create form: a
	// dim, left-bordered block so multi-line help (the GitHub token guidance)
	// reads as an aside rather than part of the form.
	fieldInfoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			BorderStyle(lipgloss.NormalBorder()).
			BorderLeft(true).
			BorderForeground(lipgloss.Color("63")).
			PaddingLeft(1)
)
