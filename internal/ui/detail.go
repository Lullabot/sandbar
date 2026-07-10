package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lullabot/sandbar/internal/manage"
	"github.com/lullabot/sandbar/internal/vm"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// updateDetail handles keys on the detail (VM) screen. The list only selects a
// VM; every per-VM lifecycle action — start/stop/restart/reset/shell/delete —
// plus the existing upload/download live here.
func (m model) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirm != nil {
		return m.updateConfirm(msg)
	}

	switch {
	case key.Matches(msg, m.keys.Start):
		m.status = "starting " + m.detail.Name + "…"
		user, scopes := m.secretsFor(m.detail.Name)
		return m, m.beginAction(startCmd(m.cli, m.detail.Name, user, scopes))

	case key.Matches(msg, m.keys.Stop):
		m.status = "stopping " + m.detail.Name + "…"
		return m, m.beginAction(stopCmd(m.cli, m.detail.Name))

	case key.Matches(msg, m.keys.Restart):
		m.status = "restarting " + m.detail.Name + "…"
		user, scopes := m.secretsFor(m.detail.Name)
		return m, m.beginAction(restartCmd(m.cli, m.detail.Name, user, scopes))

	case key.Matches(msg, m.keys.Shell):
		// limactl shell needs a running instance; guard so the key gives a clear
		// message instead of a raw limactl error.
		if m.detail.Status != "Running" {
			m.status = m.detail.Name + " must be running to open a shell (press s to start it)"
			return m, nil
		}
		m.status = "opening a shell in " + m.detail.Name + " — the TUI resumes when you exit"
		return m, shellCmd(m.detail.Name)

	case key.Matches(msg, m.keys.Delete):
		name := m.detail.Name
		m.confirm = &confirmState{
			prompt:  fmt.Sprintf("Delete %q?", name),
			run:     deleteCmd(m.cli, name),
			working: "deleting " + name + "…",
		}
		return m, nil

	case key.Matches(msg, m.keys.Reset):
		name := m.detail.Name
		// Reset clones from a Claude base, so it is only offered for VMs we
		// created — otherwise it would replace an unrelated VM with a sandbox.
		// Shared with the headless `sand create` path (internal/manage) so the
		// two entrypoints cannot drift on the gate.
		base, ok := manage.RecreateBase(m.reg, name)
		if !ok {
			m.status = "reset is only available for sand-managed VMs"
			return m, nil
		}
		// Pre-fill the reset form from the VM's recorded config (sizing,
		// hostname, identity) rather than resetting to defaults. The clone
		// token is not stored, so a VM that cloned a private repo will need it
		// re-supplied. Fall back to a minimal config if no snapshot exists
		// (e.g. a pre-snapshot index entry).
		cfg, found := m.reg.Config(name)
		if !found || cfg.Name == "" {
			cfg = vm.DefaultCreateConfig()
			cfg.Name = name
			cfg.User = hostUser()
			cfg.GitName = hostGit("user.name")
			cfg.GitEmail = hostGit("user.email")
		}
		cfg.BaseName = base
		// Open the editable reset form (with preserve toggles) instead of
		// provisioning immediately; submit dispatches provision.Reset.
		return m, m.openResetForm(name, cfg)

	case key.Matches(msg, m.keys.Secrets):
		// Deliberately no running-VM guard: secrets live on the host, so they are
		// editable whether or not the VM is up. They reach the guest on next start.
		return m, m.openSecrets(m.detail.Name)

	case key.Matches(msg, m.keys.Upload):
		return m.startTransfer(true) // host → guest

	case key.Matches(msg, m.keys.Download):
		return m.startTransfer(false) // guest → host

	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit

	// Back/Enter return to the list. Matched after every action case above so
	// neither Enter nor Back is ever swallowed by a newly added binding.
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

	b.WriteString("\n" + m.help.ShortHelpView(m.viewHelp()))
	return appStyle.Render(b.String())
}
