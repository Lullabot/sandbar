// Command sand is the interactive TUI for managing Claude Code development
// VMs: list/inspect instances, create new ones (streaming the provisioner), and
// run lifecycle actions (start/stop/restart/delete/recreate).
package main

import (
	"fmt"
	"os"

	"github.com/lullabot/sandbar/internal/profiles"
	"github.com/lullabot/sandbar/internal/provider"
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
		case "paste-image":
			if err := runPasteImage(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "land":
			if err := runLand(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		default:
			fmt.Fprintf(os.Stderr, "sand: unknown subcommand %q\n\nUsage:\n  sand              interactive TUI\n  sand create ...   headless create (see 'sand create -h')\n  sand shell NAME   attach a shell to a VM (see 'sand shell -h')\n  sand land NAME    list/land a VM's git checkouts (see 'sand land -h')\n  sand paste-image NAME   stage the clipboard image on a VM (see 'sand paste-image -h')\n", os.Args[1])
			os.Exit(2)
		}
	}

	runTUI()
}

// runTUI launches the interactive Bubble Tea program: the original (and still
// default) `sand` entrypoint. Unlike the single-provider `sand create`/`sand
// shell` paths (which resolve ONE profile — see resolveSingle), the TUI builds
// the WHOLE fleet: one sub-state per enabled connection profile, aggregated into
// one board. Each member preflights and lists ASYNCHRONOUSLY inside the model
// (see ui.New / Init), so — unlike the old path — startup never blocks on a
// remote profile's handshake, and a slow or unreachable remote surfaces as an
// error profile rather than a frozen program. The per-VM tile sampling reads
// through each member's OWN host-access seam (retiring the old ui.SetHostFiles
// process-global), so a remote VM's files are stat'd on the remote host.
func runTUI() {
	// profiles.Load quarantines a corrupt file and reseeds a usable (Local-only)
	// store rather than failing outright, so a load error is reported but not
	// fatal — the store it returns alongside the error is still safe to build from.
	store, err := profiles.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	fleet := provider.BuildFleet(store)
	if len(fleet) == 0 {
		fmt.Fprintln(os.Stderr, "no enabled connection profiles — enable at least one (the local profile is enabled by default)")
		os.Exit(1)
	}

	// Tell the TUI which build it is, so the header can say so.
	ui.SetVersion(buildversion.String(version))
	if _, err := tea.NewProgram(ui.New(fleet)).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
