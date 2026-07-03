package ui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// updateDetail handles keys on the detail view: Back/Enter return to the list,
// and Upload/Download start a file transfer for a running VM.
func (m model) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Upload):
		return m.startTransfer(true) // host → guest
	case key.Matches(msg, m.keys.Download):
		return m.startTransfer(false) // guest → host
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
	switch {
	case m.isBaseImage(v.Name):
		managed = "base image (clone source)"
	case m.reg.IsManaged(v.Name):
		managed = "yes (claude-vm)"
	}
	fields := [][2]string{
		{"Name", v.Name},
		{"Status", v.Status},
		{"CPUs", strconv.Itoa(v.CPUs)},
		{"Memory", humanizeBytes(v.Memory)},
		{"Maximum Disk Size", humanizeBytes(v.Disk)},
		{"Disk Used (allocated)", humanizeBytes(v.DiskUsed)},
		{"Arch", v.Arch},
		{"Dir", v.Dir},
		{"Managed", managed},
	}
	for _, f := range fields {
		b.WriteString(labelStyle.Render(f[0]+":") + " " + f[1] + "\n")
	}

	// A transient status line surfaces the running-VM guard when Upload/Download
	// can't proceed.
	if m.status != "" {
		b.WriteString("\n" + statusStyle.Render(m.status) + "\n")
	}

	b.WriteString("\n" + m.help.ShortHelpView(m.viewHelp()))
	return appStyle.Render(b.String())
}
