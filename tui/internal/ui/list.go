package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lullabot/sandbar/tui/internal/vm"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
)

// newTable builds the VM list table with its column layout. Name stays the
// first column so SelectedRow()[0] is always the instance name. The trailing
// "Managed" column flags VMs sand created, which is what makes recreate
// safe to gate.
func newTable() table.Model {
	cols := []table.Column{
		{Title: "Name", Width: 20},
		{Title: "Status", Width: 10},
		{Title: "CPUs", Width: 6},
		{Title: "Memory", Width: 12},
		{Title: "Max Disk", Width: 10},
		{Title: "Disk Used", Width: 10},
		{Title: "Managed", Width: 8},
	}
	t := table.New(
		table.WithColumns(cols),
		table.WithFocused(true),
		table.WithHeight(10),
	)
	return t
}

// isBaseImage reports whether name is a sand base image: a clone source for
// a managed VM, or the default base name even before any clone exists. Base
// images are the heavy, identity-free images each VM is cloned from, so the list
// marks them distinctly from ordinary VMs.
func (m model) isBaseImage(name string) bool {
	return m.reg.IsBase(name) || name == vm.DefaultCreateConfig().BaseName
}

// refreshRows rebuilds the table from m.vms, applying the managed-only filter
// and marking each VM as a managed clone, a base image, or unrelated. Call it
// after loading or toggling the filter.
func (m *model) refreshRows() {
	rows := make([]table.Row, 0, len(m.vms))
	for _, v := range m.vms {
		managed := m.reg.IsManaged(v.Name)
		base := m.isBaseImage(v.Name)
		// The managed-only view shows sand's own instances: managed clones
		// and the base image(s) they are cloned from.
		if m.managedOnly && !managed && !base {
			continue
		}
		// The name search composes with the managed-only filter: both must pass.
		if m.searchQuery != "" &&
			!strings.Contains(strings.ToLower(v.Name), strings.ToLower(m.searchQuery)) {
			continue
		}
		owner := "no"
		switch {
		case base:
			owner = "base"
		case managed:
			owner = "yes"
		}
		rows = append(rows, table.Row{
			v.Name, v.Status, strconv.Itoa(v.CPUs),
			humanizeBytes(v.Memory), humanizeBytes(v.Disk), humanizeBytes(v.DiskUsed), owner,
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

// beginAction marks a quick list lifecycle action (start/stop/restart/delete) as
// in flight and batches its command with the spinner tick, so the list shows a
// live spinner beside the status line until the matching actionDoneMsg clears it.
// The tick is only kicked when no action is already running, so a second key
// press can't stack tick loops and spin the animation at double speed.
func (m *model) beginAction(cmd tea.Cmd) tea.Cmd {
	if m.acting {
		return cmd
	}
	m.acting = true
	return tea.Batch(cmd, m.spinner.Tick)
}

// updateList handles keys while the list (or its confirm overlay) is active.
func (m model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirming {
		return m.updateConfirm(msg)
	}

	// While searching, typed keys build searchQuery and filter the table live,
	// taking priority over every action binding and the table's own navigation.
	// ctrl+c never reaches here — Update intercepts it before updateList — so it
	// still quits.
	if m.searching {
		switch msg.Type {
		case tea.KeyEsc:
			m.searching = false
			m.searchQuery = ""
			m.refreshRows()
			return m, nil
		case tea.KeyEnter:
			m.searching = false // keep the query; return to normal table navigation
			return m, nil
		case tea.KeyBackspace:
			if m.searchQuery != "" {
				r := []rune(m.searchQuery)
				m.searchQuery = string(r[:len(r)-1])
				m.refreshRows()
			}
			return m, nil
		case tea.KeyRunes, tea.KeySpace:
			m.searchQuery += string(msg.Runes)
			m.refreshRows()
			return m, nil
		}
		// Swallow any other key (arrows, tab, …) so it neither navigates nor acts.
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.Back):
		// esc clears a committed name filter — the only place esc is meaningful in
		// the list. With no active filter the case falls through to the table below,
		// where esc/backspace stay inert, so this never eats a useful key.
		if m.searchQuery != "" {
			m.searchQuery = ""
			m.status = ""
			m.refreshRows()
			return m, nil
		}

	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit

	case key.Matches(msg, m.keys.Filter):
		m.managedOnly = !m.managedOnly
		m.refreshRows()
		if m.managedOnly {
			m.status = "showing sand instances only (managed + base)"
		} else {
			m.status = "showing all VMs"
		}
		// Don't claim "showing all VMs" while a name filter is still narrowing them.
		if m.searchQuery != "" {
			m.status += fmt.Sprintf(" (name filter %q also active)", m.searchQuery)
		}
		return m, nil

	case key.Matches(msg, m.keys.Search):
		m.searching = true
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
		m.status = "" // start the detail view clean (its status is transfer guards)
		m.view = viewDetail
		return m, nil

	case key.Matches(msg, m.keys.Start):
		if n := m.selectedName(); n != "" {
			m.status = "starting " + n + "…"
			return m, m.beginAction(startCmd(m.cli, n))
		}
		return m, nil

	case key.Matches(msg, m.keys.Stop):
		if n := m.selectedName(); n != "" {
			m.status = "stopping " + n + "…"
			return m, m.beginAction(stopCmd(m.cli, n))
		}
		return m, nil

	case key.Matches(msg, m.keys.Restart):
		if n := m.selectedName(); n != "" {
			m.status = "restarting " + n + "…"
			return m, m.beginAction(restartCmd(m.cli, n))
		}
		return m, nil

	case key.Matches(msg, m.keys.Shell):
		if n := m.selectedName(); n != "" {
			// limactl shell needs a running instance; guard so the key gives a
			// clear message instead of a raw limactl error.
			if m.vmByName(n).Status != "Running" {
				m.status = n + " must be running to open a shell (press s to start it)"
				return m, nil
			}
			m.status = "opening a shell in " + n + " — the TUI resumes when you exit"
			return m, shellCmd(n)
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
		return m, m.beginAction(deleteCmd(m.cli, name))

	case "r":
		// Recreate is gated to sand-managed VMs (confirmBase is set only for
		// them); ignore it otherwise so a stray 'r' can't replace an unrelated VM.
		if m.confirmBase == "" {
			m.confirming = false
			m.status = "recreate is only available for sand-managed VMs"
			return m, nil
		}
		name := m.confirmName
		m.confirming = false
		// Pre-fill the reset form from the VM's recorded config (sizing, hostname,
		// identity) rather than resetting to defaults. The clone token is not
		// stored, so a VM that cloned a private repo will need it re-supplied. Fall
		// back to a minimal config if no snapshot exists (e.g. a pre-snapshot index
		// entry).
		cfg, ok := m.reg.Config(name)
		if !ok || cfg.Name == "" {
			cfg = vm.DefaultCreateConfig()
			cfg.Name = name
			cfg.User = hostUser()
			cfg.GitName = hostGit("user.name")
			cfg.GitEmail = hostGit("user.email")
		}
		cfg.BaseName = m.confirmBase
		// Open the editable reset form (with preserve toggles) instead of
		// provisioning immediately; submit dispatches provision.Reset.
		return m, m.openResetForm(name, cfg)

	case "n", "esc":
		m.confirming = false
		return m, nil
	}
	return m, nil
}

// listView renders the table, status line, optional confirm prompt, and help.
func (m model) listView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("sand"))
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
		status := m.status
		// While a lifecycle action runs, lead the status with the live spinner.
		if m.acting {
			status = m.spinner.View() + " " + status
		}
		b.WriteString("\n" + statusStyle.Render(status))
	}

	// Surface the name filter so it never hides VMs invisibly: a live prompt while
	// typing, and a persistent indicator (with the key to clear it) once committed
	// with enter — otherwise a committed filter silently drops rows on every reload.
	switch {
	case m.searching:
		b.WriteString("\n" + statusStyle.Render("/"+m.searchQuery+"   enter: apply · esc: clear"))
	case m.searchQuery != "":
		b.WriteString("\n" + statusStyle.Render(fmt.Sprintf("name filter: %q   / edit · esc clear", m.searchQuery)))
	}

	b.WriteString("\n\n" + m.help.ShortHelpView(m.viewHelp()))
	return appStyle.Render(b.String())
}
