package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/deviantintegral/claude-code-ansible/tui/internal/vm"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
)

// newTable builds the VM list table with its column layout.
func newTable() table.Model {
	cols := []table.Column{
		{Title: "Name", Width: 20},
		{Title: "Status", Width: 10},
		{Title: "CPUs", Width: 6},
		{Title: "Memory", Width: 14},
		{Title: "Disk", Width: 14},
	}
	t := table.New(
		table.WithColumns(cols),
		table.WithFocused(true),
		table.WithHeight(10),
	)
	return t
}

// setTableRows reflects the loaded VMs into the table.
func (m *model) setTableRows(vms []vm.VM) {
	rows := make([]table.Row, 0, len(vms))
	for _, v := range vms {
		rows = append(rows, table.Row{
			v.Name, v.Status, strconv.Itoa(v.CPUs), v.Memory, v.Disk,
		})
	}
	m.table.SetRows(rows)
}

// selectedName returns the highlighted VM's name, or "" when the list is empty.
func (m model) selectedName() string {
	row := m.table.SelectedRow()
	if len(row) == 0 {
		return ""
	}
	return row[0]
}

// vmByName looks up a loaded VM record by name.
func (m model) vmByName(name string) vm.VM {
	for _, v := range m.vms {
		if v.Name == name {
			return v
		}
	}
	return vm.VM{Name: name}
}

// updateList handles keys while the list (or its confirm overlay) is active.
func (m model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirming {
		return m.updateConfirm(msg)
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit

	case key.Matches(msg, m.keys.New):
		cmd := m.openForm()
		return m, cmd

	case key.Matches(msg, m.keys.Enter):
		name := m.selectedName()
		if name == "" {
			return m, nil
		}
		m.detail = m.vmByName(name)
		m.view = viewDetail
		return m, nil

	case key.Matches(msg, m.keys.Start):
		if n := m.selectedName(); n != "" {
			m.status = "starting " + n + "…"
			return m, startCmd(m.cli, n)
		}
		return m, nil

	case key.Matches(msg, m.keys.Stop):
		if n := m.selectedName(); n != "" {
			m.status = "stopping " + n + "…"
			return m, stopCmd(m.cli, n)
		}
		return m, nil

	case key.Matches(msg, m.keys.Restart):
		if n := m.selectedName(); n != "" {
			m.status = "restarting " + n + "…"
			return m, restartCmd(m.cli, n)
		}
		return m, nil

	case key.Matches(msg, m.keys.Delete):
		if n := m.selectedName(); n != "" {
			m.confirming = true
			m.confirmName = n
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

// updateConfirm handles the destructive delete/recreate confirmation overlay.
func (m model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "d":
		name := m.confirmName
		m.confirming = false
		m.status = "deleting " + name + "…"
		return m, deleteCmd(m.cli, name)

	case "r":
		name := m.confirmName
		m.confirming = false
		cfg := vm.DefaultCreateConfig()
		cfg.Name = name
		cfg.GitName = hostGit("user.name")
		cfg.GitEmail = hostGit("user.email")
		cmd := m.beginProvision("Recreating "+name, m.prov.Recreate, cfg)
		return m, cmd

	case "n", "esc":
		m.confirming = false
		return m, nil
	}
	return m, nil
}

// listView renders the table, status line, optional confirm prompt, and help.
func (m model) listView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("claude-vm"))
	b.WriteString("\n\n")
	b.WriteString(m.table.View())
	b.WriteString("\n")

	switch {
	case m.confirming:
		b.WriteString("\n" + errStyle.Render(
			fmt.Sprintf("Delete %q?  [y] yes   [r] recreate   [n] cancel", m.confirmName)))
	case m.status != "":
		b.WriteString("\n" + statusStyle.Render(m.status))
	}

	b.WriteString("\n\n" + m.help.ShortHelpView(m.viewHelp()))
	return appStyle.Render(b.String())
}
