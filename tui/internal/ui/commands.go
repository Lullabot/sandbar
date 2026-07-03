package ui

import (
	"os/exec"
	"strconv"

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
	// actionDoneMsg reports a lifecycle action (start/stop/restart/delete). name
	// is the affected instance, so the model can update the managed registry.
	actionDoneMsg struct {
		action string
		name   string
		err    error
	}
	// provisionOutputMsg is one chunk of streamed provisioner output.
	provisionOutputMsg string
	// provisionDoneMsg signals the provisioner goroutine finished.
	provisionDoneMsg struct{ err error }
)

// listCmd loads the VM list off the Update goroutine, and measures each VM's
// real disk consumption here in the command — a blocking per-VM stat that must
// NOT run in Update, so an unresponsive mount (stale NFS, sleeping USB, autofs)
// can't stall the Bubble Tea event loop. A non-positive result leaves DiskUsed
// empty so the cell renders blank.
func listCmd(cli *lima.Client) tea.Cmd {
	return func() tea.Msg {
		vms, err := cli.List()
		if err == nil {
			for i := range vms {
				if n := diskUsedBytes(vms[i].Dir); n > 0 {
					vms[i].DiskUsed = strconv.FormatInt(n, 10)
				}
			}
		}
		return vmsLoadedMsg{vms: vms, err: err}
	}
}

// startCmd boots a stopped VM.
func startCmd(cli *lima.Client, name string) tea.Cmd {
	return func() tea.Msg {
		return actionDoneMsg{action: "start", name: name, err: cli.Start(name)}
	}
}

// stopCmd shuts a running VM down.
func stopCmd(cli *lima.Client, name string) tea.Cmd {
	return func() tea.Msg {
		return actionDoneMsg{action: "stop", name: name, err: cli.Stop(name)}
	}
}

// restartCmd stops then starts a VM, surfacing the first failure.
func restartCmd(cli *lima.Client, name string) tea.Cmd {
	return func() tea.Msg {
		if err := cli.Stop(name); err != nil {
			return actionDoneMsg{action: "restart", name: name, err: err}
		}
		return actionDoneMsg{action: "restart", name: name, err: cli.Start(name)}
	}
}

// deleteCmd force-removes a VM.
func deleteCmd(cli *lima.Client, name string) tea.Cmd {
	return func() tea.Msg {
		return actionDoneMsg{action: "delete", name: name, err: cli.Delete(name, true)}
	}
}

// shellCmd opens an interactive shell inside a VM. It uses tea.ExecProcess to
// suspend the TUI and hand the real terminal to `limactl shell <name>`, then
// resumes the TUI when the shell exits. This deliberately bypasses lima.Runner
// (which captures output) — an interactive session needs the actual TTY, which
// only the real process attached to stdin/stdout can provide.
func shellCmd(name string) tea.Cmd {
	c := exec.Command("limactl", "shell", name)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return actionDoneMsg{action: "shell", name: name, err: err}
	})
}
