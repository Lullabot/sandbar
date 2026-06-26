package ui

import "github.com/charmbracelet/lipgloss"

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
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))

	labelStyle        = lipgloss.NewStyle().Width(18).Foreground(lipgloss.Color("245"))
	focusedLabelStyle = lipgloss.NewStyle().Width(18).Bold(true).Foreground(lipgloss.Color("63"))

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63")).
			Padding(0, 1)
)
