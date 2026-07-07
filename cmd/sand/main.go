// Command sand is the interactive TUI for managing Claude Code development
// VMs: list/inspect instances, create new ones (streaming the provisioner), and
// run lifecycle actions (start/stop/restart/delete/recreate).
package main

import (
	"fmt"
	"os"

	"github.com/lullabot/sandbar/internal/lima"
	"github.com/lullabot/sandbar/internal/provision"
	"github.com/lullabot/sandbar/internal/ui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	cli := lima.New(lima.NewExecRunner())
	if err := cli.Preflight(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	dir, err := provision.LocatePlaybook()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	prov := &provision.Provisioner{Lima: cli, PlaybookDir: dir}

	if _, err := tea.NewProgram(ui.New(cli, prov), tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
