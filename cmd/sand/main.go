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
	// Subcommand dispatch: bare `sand` (no args) launches the TUI, unchanged;
	// `sand create ...` runs the headless, non-interactive provisioning path
	// (see create.go); any other first argument is an unknown subcommand.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "create":
			if err := runCreate(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		default:
			fmt.Fprintf(os.Stderr, "sand: unknown subcommand %q\n\nUsage:\n  sand              interactive TUI\n  sand create ...   headless create (see 'sand create -h')\n", os.Args[1])
			os.Exit(2)
		}
	}

	runTUI()
}

// runTUI launches the interactive Bubble Tea program: the original (and still
// default) `sand` entrypoint.
func runTUI() {
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
