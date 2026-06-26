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

// newTable builds the VM list table with its column layout. Name stays the
// first column so SelectedRow()[0] is always the instance name. The trailing
// "Managed" column flags VMs claude-vm created, which is what makes recreate
// safe to gate.
func newTable() table.Model {
	cols := []table.Column{
		{Title: "Name", Width: 20},
		{Title: "Status", Width: 10},
		{Title: "CPUs", Width: 6},
		{Title: "Memory", Width: 14},
		{Title: "Disk", Width: 14},
		{Title: "Managed", Width: 8},
	}
	t := table.New(
		table.WithColumns(cols),
		table.WithFocused(true),
		table.WithHeight(10),
	)
	return t
}

// refreshRows rebuilds the table from m.vms, applying the managed-only filter
// and marking each VM's managed status. Call it after loading or toggling the
// filter.
func (m *model) refreshRows() {
	rows := make([]table.Row, 0, len(m.vms))
	for _, v := range m.vms {
		managed := m.reg.IsManaged(v.Name)
		if m.managedOnly && !managed {
			continue
		}
		owner := "no"
		if managed {
			owner = "yes"
		}
		rows = append(rows, table.Row{
			v.Name, v.Status, strconv.Itoa(v.CPUs), v.Memory, v.Disk, owner,
		})
	}
	m.table.SetRows(rows)
	// SetRows only clamps the cursor downward, so emptying the list (e.g. the
	// managed-only filter matching nothing) leaves it at -1; refilling never
	// reseats it, leaving the selection — and every action key — dead. Reseat it.
	if len(rows) > 0 && m.table.Cursor() < 0 {
		m.table.SetCursor(0)
	}
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

	case key.Matches(msg, m.keys.Filter):
		m.managedOnly = !m.managedOnly
		m.refreshRows()
		if m.managedOnly {
			m.status = "showing claude-vm-managed VMs only"
		} else {
			m.status = "showing all VMs"
		}
		return m, nil

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
			// Recreate clones from a Claude base, so it is only offered for VMs we
			// created — otherwise it would replace an unrelated VM with a sandbox.
			m.confirmBase = ""
			if m.reg.IsManaged(n) {
				if base := m.reg.Base(n); base != "" {
					m.confirmBase = base
				} else {
					m.confirmBase = vm.DefaultCreateConfig().BaseName
				}
			}
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
		// Recreate is gated to claude-vm-managed VMs (confirmBase is set only for
		// them); ignore it otherwise so a stray 'r' can't replace an unrelated VM.
		if m.confirmBase == "" {
			m.confirming = false
			m.status = "recreate is only available for claude-vm-managed VMs"
			return m, nil
		}
		name := m.confirmName
		m.confirming = false
		// Reproduce the VM from its recorded config (sizing, hostname, identity)
		// rather than resetting to defaults. The clone token is not stored, so a
		// VM that cloned a private repo will need it re-supplied. Fall back to a
		// minimal config if no snapshot exists (e.g. a pre-snapshot index entry).
		cfg, ok := m.reg.Config(name)
		if !ok || cfg.Name == "" {
			cfg = vm.DefaultCreateConfig()
			cfg.Name = name
			cfg.GitName = hostGit("user.name")
			cfg.GitEmail = hostGit("user.email")
		}
		cfg.BaseName = m.confirmBase
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
		prompt := fmt.Sprintf("Delete %q?  [y] yes", m.confirmName)
		if m.confirmBase != "" {
			prompt += fmt.Sprintf("   [r] recreate from %s", m.confirmBase)
		}
		prompt += "   [n] cancel"
		b.WriteString("\n" + errStyle.Render(prompt))
	case m.status != "":
		b.WriteString("\n" + statusStyle.Render(m.status))
	}

	b.WriteString("\n\n" + m.help.ShortHelpView(m.viewHelp()))
	return appStyle.Render(b.String())
}
