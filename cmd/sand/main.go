// Command sand is the interactive TUI for managing Claude Code development
// VMs: list/inspect instances, create new ones (streaming the provisioner), and
// run lifecycle actions (start/stop/restart/delete/recreate).
package main

import (
	"fmt"
	"os"

	"github.com/lullabot/sandbar/internal/ui"
	buildversion "github.com/lullabot/sandbar/internal/version"

	tea "charm.land/bubbletea/v2"
)

// version is the sand release version. It defaults to "dev" for local/source
// builds; GoReleaser stamps the real value at build time via
// `-ldflags "-X main.version={{.Version}}"`.
var version = "dev"

func main() {
	// Subcommand dispatch: bare `sand` (no args) launches the TUI, unchanged;
	// `sand create ...` runs the headless, non-interactive provisioning path
	// (see create.go); any other first argument is an unknown subcommand.
	// `--version`/`version` is handled first so it works without limactl.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "version":
			fmt.Println(buildversion.String(version))
			return
		case "create":
			if err := runCreate(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "shell":
			if err := runShell(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		default:
			fmt.Fprintf(os.Stderr, "sand: unknown subcommand %q\n\nUsage:\n  sand              interactive TUI\n  sand create ...   headless create (see 'sand create -h')\n  sand shell NAME   attach a shell to a VM (see 'sand shell -h')\n", os.Args[1])
			os.Exit(2)
		}
	}

	runTUI()
}

// runTUI launches the interactive Bubble Tea program: the original (and still
// default) `sand` entrypoint.
func runTUI() {
	// scope is the registry.Scope the resolved provider owns (registry.LocalScope
	// for the default, unconfigured local Lima) — see resolveSingle. The TUI
	// threads it through to its own manage.Reconcile/RecordSuccess bookkeeping
	// (internal/ui/model.go) so it stays in lockstep with the headless `sand
	// create` path (cmd/sand/create.go) on which entries belong to which
	// provider.
	p, scope, err := resolveSingle()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := p.Preflight(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Point the TUI's per-VM tile sampling (disk usage, up-since / last-used) at
	// the same host limactl runs on. For a remote provider that is the remote host
	// (p.HostFiles() — see provider.Provider.HostFiles); for local Lima it is the
	// local filesystem, unchanged. Without this the disk gauge stats the remote
	// instance dir on the laptop and renders "?".
	ui.SetHostFiles(p.HostFiles())

	// Tell the TUI which build it is, so the header can say so.
	ui.SetVersion(buildversion.String(version))
	if _, err := tea.NewProgram(ui.New(p, scope)).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
