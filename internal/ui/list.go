package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lullabot/sandbar/internal/vm"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
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

// stopAllTargets returns the sand-managed VMs that are currently running.
// Base images are excluded: they are kept stopped by design and are a clone
// source, not a workspace — though a base mid-build is running, which is
// exactly why the exclusion is explicit rather than incidental.
//
// This walks m.vms (every loaded VM), NOT the filtered table rows: a managed
// VM hidden by an active name filter ('/') or the managed-only toggle ('f')
// must still be stopped. That is a deliberate choice — 'X' means "stop all",
// not "stop what I can currently see" — because the opposite reading is
// defensible and a future reader will wonder.
func (m model) stopAllTargets() []string {
	var names []string
	for _, v := range m.vms {
		if v.Status != "Running" || !m.reg.IsManaged(v.Name) || m.isBaseImage(v.Name) {
			continue
		}
		names = append(names, v.Name)
	}
	return names
}

// summarizeNames renders up to a width-appropriate number of names for
// display in the confirm prompt, summarizing any remainder as "and N more".
// Display only: every target in names is still stopped regardless of how the
// list is truncated here. Falls back to a sane budget when the terminal size
// has not been reported yet (width == 0).
func summarizeNames(names []string, width int) string {
	budget := width - 40
	if budget < 20 {
		budget = 20
	}
	var b strings.Builder
	shown := 0
	for i, n := range names {
		sep := ""
		if i > 0 {
			sep = ", "
		}
		if b.Len()+len(sep)+len(n) > budget && shown > 0 {
			break
		}
		b.WriteString(sep)
		b.WriteString(n)
		shown++
	}
	if shown < len(names) {
		fmt.Fprintf(&b, " and %d more", len(names)-shown)
	}
	return b.String()
}

// selectedName returns the highlighted VM's name, or "" when the list is empty.
func (m model) selectedName() string {
	row := m.table.SelectedRow()
	if len(row) == 0 {
		return ""
	}
	return row[0]
}

// lookupVM looks up a loaded VM record by name, reporting whether it was found.
// The miss case is distinguishable from a real zero-value record, which the
// detail view's re-seed needs: "the VM is gone" must route back to the list
// rather than render a stale or blank record.
func (m model) lookupVM(name string) (vm.VM, bool) {
	for _, v := range m.vms {
		if v.Name == name {
			return v, true
		}
	}
	return vm.VM{Name: name}, false
}

// beginAction marks a quick list lifecycle action (start/stop/restart/delete) as
// in flight and batches its command with the spinner tick, so the list shows a
// live spinner beside the status line until the matching actionDoneMsg clears it.
// tickSpinner is what keeps a second key press — or a build already running on
// another VM — from stacking tick loops and spinning the animation at double
// speed.
func (m *model) beginAction(cmd tea.Cmd) tea.Cmd {
	m.acting = true
	return tea.Batch(cmd, m.tickSpinner())
}

// updateList handles keys while the list (or its confirm overlay) is active.
func (m model) updateList(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.confirm != nil {
		return m.updateConfirm(msg)
	}

	// While searching, typed keys build searchQuery and filter the table live,
	// taking priority over every action binding and the table's own navigation.
	// ctrl+c never reaches here — Update intercepts it before updateList — so it
	// still quits.
	if m.searching {
		switch {
		case msg.Code == tea.KeyEsc:
			m.searching = false
			m.searchQuery = ""
			m.refreshRows()
			return m, nil
		case msg.Code == tea.KeyEnter:
			m.searching = false // keep the query; return to normal table navigation
			return m, nil
		case msg.Code == tea.KeyBackspace:
			if m.searchQuery != "" {
				r := []rune(m.searchQuery)
				m.searchQuery = string(r[:len(r)-1])
				m.refreshRows()
			}
			return m, nil
		case msg.Text != "":
			m.searchQuery += msg.Text
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
		// where esc/backspace do nothing, so this never eats a useful key.
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
		// The name came from a selected row, so the lookup always hits.
		m.detail, _ = m.lookupVM(name)
		m.status = "" // start the detail view clean (its status is transfer guards)
		m.view = viewDetail
		return m, nil

	case key.Matches(msg, m.keys.StopAll):
		targets := m.stopAllTargets()
		if len(targets) == 0 {
			m.status = "no running sand VMs to stop"
			return m, nil
		}
		m.confirm = &confirmState{
			prompt:  fmt.Sprintf("Stop %d running sand VMs (%s)?", len(targets), summarizeNames(targets, m.width)),
			run:     stopAllCmd(m.cli, targets),
			working: fmt.Sprintf("stopping %d sand VMs…", len(targets)),
		}
		return m, nil

	}

	// Start/Stop/Restart/Shell/Delete now live on the detail (VM) screen — the
	// list only selects a VM, it no longer acts on one. Their keys (s/x/r/S/d)
	// are not bubbles/table bindings, so falling through here leaves them inert
	// rather than accidentally navigating the table.
	//
	// 'g' is a bubbles/table binding (GotoTop). On the list that is fine — we
	// want the table's 'g'. Download (also 'g') only matches in updateDetail,
	// which has no table, so the two never collide: updateList never checks
	// for a Download binding, and the detail screen's own help/dispatch (see
	// commandreg.go) scopes the download hint to the VM screen.
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

// listHelp returns the bindings shown in the list screen's help bar: the
// pending-confirm overlay and the live search prompt each replace the normal
// set of global/chrome keys with their own narrower one.
func (m model) listHelp() []key.Binding {
	if m.confirm != nil {
		return []key.Binding{m.keys.Confirm, m.keys.Cancel}
	}
	if m.searching {
		// esc clears/exits, enter commits the filter.
		return []key.Binding{m.keys.Back, m.keys.Enter}
	}
	return []key.Binding{
		m.keys.Enter, m.keys.New, m.keys.Filter, m.keys.Search,
		m.keys.StopAll, m.keys.Quit,
	}
}

// listView renders the table, status line, optional confirm prompt, and help.
func (m model) listView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("sand"))
	b.WriteString("\n\n")
	b.WriteString(m.table.View())
	b.WriteString("\n")

	switch {
	case m.confirm != nil:
		b.WriteString("\n" + m.confirmView())
	case m.acting:
		// While a lifecycle action (including a confirmed stop-all) runs, lead the
		// status with the live spinner — even if no status text was set, so the
		// action never looks frozen.
		status := m.status
		if status == "" {
			status = "working…"
		}
		b.WriteString("\n" + statusStyle.Render(m.spinner.View()+" "+status))
	case m.status != "":
		b.WriteString("\n" + statusStyle.Render(m.status))
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

	b.WriteString("\n\n" + m.help.ShortHelpView(m.listHelp()))
	return appStyle.Render(b.String())
}
