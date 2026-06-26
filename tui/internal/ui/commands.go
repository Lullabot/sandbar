package ui

import (
	"github.com/deviantintegral/claude-code-ansible/tui/internal/lima"
	"github.com/deviantintegral/claude-code-ansible/tui/internal/vm"

	tea "github.com/charmbracelet/bubbletea"
)

// Message types flowing through Update. Every blocking lima/provision call is
// wrapped in a tea.Cmd that returns one of these — Update itself never blocks.
type (
	// vmsLoadedMsg carries the result of a List refresh.
	vmsLoadedMsg struct {
		vms []vm.VM
		err error
	}
	// actionDoneMsg reports a lifecycle action (start/stop/restart/delete).
	actionDoneMsg struct {
		action string
		err    error
	}
	// provisionOutputMsg is one chunk of streamed provisioner output.
	provisionOutputMsg string
	// provisionDoneMsg signals the provisioner goroutine finished.
	provisionDoneMsg struct{ err error }
)

// listCmd loads the VM list off the Update goroutine.
func listCmd(cli *lima.Client) tea.Cmd {
	return func() tea.Msg {
		vms, err := cli.List()
		return vmsLoadedMsg{vms: vms, err: err}
	}
}

// startCmd boots a stopped VM.
func startCmd(cli *lima.Client, name string) tea.Cmd {
	return func() tea.Msg {
		return actionDoneMsg{action: "start " + name, err: cli.Start(name)}
	}
}

// stopCmd shuts a running VM down.
func stopCmd(cli *lima.Client, name string) tea.Cmd {
	return func() tea.Msg {
		return actionDoneMsg{action: "stop " + name, err: cli.Stop(name)}
	}
}

// restartCmd stops then starts a VM, surfacing the first failure.
func restartCmd(cli *lima.Client, name string) tea.Cmd {
	return func() tea.Msg {
		if err := cli.Stop(name); err != nil {
			return actionDoneMsg{action: "restart " + name, err: err}
		}
		return actionDoneMsg{action: "restart " + name, err: cli.Start(name)}
	}
}

// deleteCmd force-removes a VM.
func deleteCmd(cli *lima.Client, name string) tea.Cmd {
	return func() tea.Msg {
		return actionDoneMsg{action: "delete " + name, err: cli.Delete(name, true)}
	}
}
