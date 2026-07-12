package ui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// updateDetail handles keys on the detail (VM) screen. The list only selects a
// VM; every per-VM lifecycle action — start/stop/restart/reset/shell/delete —
// plus the existing upload/download derive from the command registry in
// commandreg.go: a key fires iff its command's enabledFor returns true for
// the focused VM, which is what keeps this dispatcher and detailHelp's footer
// from disagreeing (the bug the registry replaced: the old per-view help
// switch used to offer every verb unconditionally).
func (m model) updateDetail(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.confirm != nil {
		return m.updateConfirm(msg)
	}

	for _, c := range detailCommands {
		if key.Matches(msg, c.binding) && c.enabledFor(m, m.detail) {
			cmd := c.action(&m, m.detail)
			return m, cmd
		}
	}
	for _, c := range detailChrome {
		if key.Matches(msg, c.binding) && c.enabledFor(m) {
			cmd := c.action(&m)
			return m, cmd
		}
	}
	return m, nil
}

// detailHelp returns the bindings shown in the VM screen's help bar, derived
// from the exact same registry (and the exact same enabledFor calls) that
// updateDetail dispatches from above — so it shows a verb iff pressing that
// verb's key would do something.
func (m model) detailHelp() []key.Binding {
	if m.confirm != nil {
		return []key.Binding{m.keys.Confirm, m.keys.Cancel}
	}
	bindings := make([]key.Binding, 0, len(detailCommands)+len(detailChrome))
	for _, c := range detailCommands {
		if c.enabledFor(m, m.detail) {
			bindings = append(bindings, c.binding)
		}
	}
	for _, c := range detailChrome {
		if c.enabledFor(m) {
			bindings = append(bindings, c.binding)
		}
	}
	return bindings
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
		managed = "yes (sand)"
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
	// can't proceed; the confirm prompt takes priority when a destructive
	// action (delete) is pending.
	switch {
	case m.confirm != nil:
		b.WriteString("\n" + m.confirmView() + "\n")
	case m.acting:
		// A lifecycle action (start/stop/restart/delete) is in flight — lead the
		// status with the live spinner, matching the list screen.
		status := m.status
		if status == "" {
			status = "working…"
		}
		b.WriteString("\n" + statusStyle.Render(m.spinner.View()+" "+status) + "\n")
	case m.status != "":
		b.WriteString("\n" + statusStyle.Render(m.status) + "\n")
	}

	b.WriteString("\n" + m.help.ShortHelpView(m.detailHelp()))
	return appStyle.Render(b.String())
}
