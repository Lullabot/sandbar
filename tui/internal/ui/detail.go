package ui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// updateDetail handles keys on the read-only detail view.
func (m model) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Back), key.Matches(msg, m.keys.Enter):
		m.view = viewList
		return m, nil
	}
	return m, nil
}

// detailView renders the highlighted VM's full record.
func (m model) detailView() string {
	v := m.detail
	var b strings.Builder
	b.WriteString(titleStyle.Render("VM: " + v.Name))
	b.WriteString("\n\n")

	managed := "no"
	if m.reg.IsManaged(v.Name) {
		managed = "yes (claude-vm)"
	}
	fields := [][2]string{
		{"Name", v.Name},
		{"Status", v.Status},
		{"CPUs", strconv.Itoa(v.CPUs)},
		{"Memory", v.Memory},
		{"Disk", v.Disk},
		{"Arch", v.Arch},
		{"IP", v.IP},
		{"Dir", v.Dir},
		{"Managed", managed},
	}
	for _, f := range fields {
		b.WriteString(labelStyle.Render(f[0]+":") + " " + f[1] + "\n")
	}

	b.WriteString("\n" + m.help.ShortHelpView(m.viewHelp()))
	return appStyle.Render(b.String())
}
